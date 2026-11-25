package gateway

import (
	"encoding/json"
	"errors"
	"math"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/mydisha/keirouter/backend/internal/dispatch"
	"github.com/mydisha/keirouter/backend/internal/imagegen"
	"github.com/mydisha/keirouter/backend/internal/store"
)

const (
	maxImagePromptRunes = 4000
	maxImageBodyBytes   = 64 << 10
	maxImagesPerJob     = 4
	defaultImageList    = 50
	maxImageList        = 100
)

// servedImageTypes are the stored image types served as themselves; any
// other stored type is sent as an opaque download.
var servedImageTypes = map[string]bool{"image/png": true, "image/jpeg": true, "image/webp": true}

// gptImageSizes are the sizes a gpt-image request may ask for.
var gptImageSizes = map[string]bool{"1024x1024": true, "1536x1024": true, "1024x1536": true, "auto": true}

// imageAspectRatios are the aspect ratios each family may ask for; the
// portal page offers the same lists.
var imageAspectRatios = map[string]map[string]bool{
	"grok-imagine": {"1:1": true, "16:9": true, "9:16": true, "4:3": true, "3:4": true, "3:2": true, "2:3": true, "21:9": true},
	"gemini-image": {"1:1": true, "16:9": true, "9:16": true, "4:3": true, "3:4": true},
}

// mountPortalImageRoutes adds the portal image routes. The caller applies
// portalSessionMiddleware.
func (s *Server) mountPortalImageRoutes(r chi.Router) {
	r.Get("/portal/api/images/models", s.handlePortalImageModels)
	r.Post("/portal/api/images", s.handlePortalCreateImage)
	r.Get("/portal/api/images", s.handlePortalListImages)
	r.Get("/portal/api/images/files/{file_id}", s.handlePortalImageFile)
	r.Delete("/portal/api/images/{job_id}", s.handlePortalDeleteImage)
}

// imageWriteAllowed guards the routes that spend balance or delete jobs
// against cross-site requests. The body of a POST must be JSON, which a
// cross-site form cannot send without a preflight, and an Origin header,
// when the browser sends one, must name this server: the request Host, a
// proxy's X-Forwarded-Host, or the configured portal URL.
func (s *Server) imageWriteAllowed(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == http.MethodPost {
		mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mt != "application/json" {
			writeImageError(w, http.StatusUnsupportedMediaType, "json_required")
			return false
		}
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err == nil && u.Host != "" {
		host := strings.ToLower(u.Host)
		for _, allowed := range s.portalOriginHosts(r) {
			if host == allowed {
				return true
			}
		}
	}
	writeImageError(w, http.StatusForbidden, "bad_origin")
	return false
}

// portalOriginHosts lists the hosts a portal page may be served from.
func (s *Server) portalOriginHosts(r *http.Request) []string {
	hosts := []string{strings.ToLower(r.Host)}
	if fwd := r.Header.Get("X-Forwarded-Host"); fwd != "" {
		hosts = append(hosts, strings.ToLower(strings.TrimSpace(strings.Split(fwd, ",")[0])))
	}
	if s.cfg.PortalSSO.RedirectURL != "" {
		if u, err := url.Parse(s.cfg.PortalSSO.RedirectURL); err == nil && u.Host != "" {
			hosts = append(hosts, strings.ToLower(u.Host))
		}
	}
	return hosts
}

func writeImageError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"error": code})
}

// imageGenReady answers 503 when portal image generation is not wired.
func (s *Server) imageGenReady(w http.ResponseWriter) bool {
	if s.imageGen == nil {
		writeImageError(w, http.StatusServiceUnavailable, "image_generation_unavailable")
		return false
	}
	return true
}

// imageOwner is the stored owner of the session's image jobs: the Google
// subject, the same value for every image route.
func (s *Server) imageOwner(w http.ResponseWriter, r *http.Request) (string, bool) {
	sub, ok := s.portalSubject(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "portal session required")
		return "", false
	}
	return portalGoogleSub(sub), true
}

// portalImageChain is one image chain the key may call, with its priced
// image steps in fallback order.
type portalImageChain struct {
	chain   store.Chain
	targets []dispatch.Target
	// declared is targets in the chain's declared step order: the shown
	// price is its first priced step, whatever order the route runs in.
	declared []dispatch.Target
	family   string
	price    int64
	price2K  int64
	has2K    bool
}

// portalImageChains lists the chains the key may call that have at least one
// priced image step. Only the image steps are kept: the chain's final
// fallback and any token-priced step never run for a portal image.
func (s *Server) portalImageChains(r *http.Request, key store.APIKey, planID string) ([]portalImageChain, error) {
	chains, err := s.portalChains(r, key, planID)
	if err != nil {
		return nil, err
	}
	ctx := r.Context()
	var out []portalImageChain
	for _, c := range chains {
		if !chainHasImageStep(c) {
			continue
		}
		model := "chain:" + c.Name
		res, err := s.resolveTargets(ctx, tenantOf(key), model)
		if err != nil {
			var bad badModelError
			if asBadModel(err, &bad) {
				continue
			}
			return nil, err
		}
		allowed, err := s.filterAllowedTargets(ctx, key.ID, key.PlanID, model, true, res.Targets)
		if err != nil {
			return nil, err
		}
		var images []dispatch.Target
		for _, t := range allowed {
			if isImageStep(t) && !t.ChainFallback {
				images = append(images, t)
			}
		}
		// The admin's provider ceiling and the customer's Model Settings
		// apply here as on the API route.
		images, _ = filterStepsForKey(images, key, c.Name)
		declared := declaredOrder(c, images)
		price, ok := imagegen.ShownPriceMicro(declared, "")
		if !ok {
			continue
		}
		price2K, has2K := imagegen.ShownPriceMicro(declared, "2k")
		out = append(out, portalImageChain{
			chain: c, targets: images, declared: declared, family: imageFamily(images[0].Model),
			price: price, price2K: price2K, has2K: has2K,
		})
	}
	return out, nil
}

// portalImageKey loads the session's key as API auth would see it: a
// disabled key is refused with 403, and the key carries its provider routing
// settings. A routing load error fails the request rather than routing
// through providers the key may not use.
func (s *Server) portalImageKey(w http.ResponseWriter, r *http.Request, u store.PortalUser) (store.APIKey, bool) {
	key, ok := s.portalKey(w, r, u)
	if !ok {
		return store.APIKey{}, false
	}
	if key.Disabled {
		writeImageError(w, http.StatusForbidden, "key_disabled")
		return store.APIKey{}, false
	}
	routing, err := s.identity.Keys().KeyRouting(r.Context(), key.ID)
	if err != nil {
		s.log.Error("portal images: load key routing failed", "err", err)
		writeImageError(w, http.StatusInternalServerError, "internal_error")
		return store.APIKey{}, false
	}
	key.AllowedProviders = routing.AllowedProviders
	key.ModelDisabledProviders = routing.DisabledProviders
	key.ModelFinalFallback = routing.FinalFallback
	return key, true
}

// declaredOrder returns ts sorted by the position of their step in c, so a
// reordered route (latency, cost, market, round-robin) never changes the
// shown price. A target matches the first step with its provider, model and
// image price; steps that match alike have the same price.
func declaredOrder(c store.Chain, ts []dispatch.Target) []dispatch.Target {
	pos := func(t dispatch.Target) int {
		for _, st := range c.Steps {
			if st.Provider == t.Provider && st.Model == t.Model && st.StaticPerImage == t.ImagePrice {
				return st.Position
			}
		}
		return math.MaxInt
	}
	out := append([]dispatch.Target(nil), ts...)
	sort.SliceStable(out, func(i, j int) bool { return pos(out[i]) < pos(out[j]) })
	return out
}

func chainHasImageStep(c store.Chain) bool {
	for _, st := range c.Steps {
		if st.HasImagePrice() {
			return true
		}
	}
	return false
}

type portalImageModel struct {
	ChainID    string `json:"chain_id"`
	Name       string `json:"name"`
	Family     string `json:"family"`
	PriceMicro int64  `json:"price_micro"`
	// Price2KMicro is the 2K price of a grok-imagine chain; absent when
	// the chain cannot make 2K images.
	Price2KMicro *int64 `json:"price_2k_micro,omitempty"`
}

// handlePortalImageModels serves GET /portal/api/images/models.
func (s *Server) handlePortalImageModels(w http.ResponseWriter, r *http.Request) {
	if !s.imageGenReady(w) {
		return
	}
	u, ok := s.portalUserOrError(w, r)
	if !ok {
		return
	}
	key, ok := s.portalImageKey(w, r, u)
	if !ok {
		return
	}
	chains, err := s.portalImageChains(r, key, u.PlanID)
	if err != nil {
		s.log.Error("portal images: list models failed", "err", err)
		writeImageError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	out := make([]portalImageModel, 0, len(chains))
	for _, c := range chains {
		m := portalImageModel{ChainID: c.chain.ID, Name: c.chain.Name, Family: c.family, PriceMicro: c.price}
		// Only grok-imagine takes a resolution, so only it can be billed at
		// a 2K price.
		if c.family == "grok-imagine" && c.has2K {
			p := c.price2K
			m.Price2KMicro = &p
		}
		out = append(out, m)
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": out})
}

type createImageRequest struct {
	ChainID     string `json:"chain_id"`
	Prompt      string `json:"prompt"`
	N           *int   `json:"n"`
	Size        string `json:"size"`
	AspectRatio string `json:"aspect_ratio"`
	Resolution  string `json:"resolution"`
	// ExpectedPriceMicro is the per-image price the page showed.
	ExpectedPriceMicro *int64 `json:"expected_price_micro"`
}

// handlePortalCreateImage serves POST /portal/api/images. The chain and its
// price are re-resolved for the session's key; nothing priced comes from
// the client.
func (s *Server) handlePortalCreateImage(w http.ResponseWriter, r *http.Request) {
	if !s.imageGenReady(w) || !s.imageWriteAllowed(w, r) {
		return
	}
	var body createImageRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxImageBodyBytes)).Decode(&body); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeImageError(w, http.StatusRequestEntityTooLarge, "request_too_large")
			return
		}
		writeImageError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	prompt := strings.TrimSpace(body.Prompt)
	switch {
	case prompt == "":
		writeImageError(w, http.StatusBadRequest, "empty_prompt")
		return
	case utf8.RuneCountInString(prompt) > maxImagePromptRunes:
		writeImageError(w, http.StatusBadRequest, "prompt_too_long")
		return
	}
	n := 1
	if body.N != nil {
		n = *body.N
	}
	if n < 1 || n > maxImagesPerJob {
		writeImageError(w, http.StatusBadRequest, "invalid_n")
		return
	}
	chainID := strings.TrimSpace(body.ChainID)
	if chainID == "" {
		writeImageError(w, http.StatusBadRequest, "chain_required")
		return
	}

	owner, ok := s.imageOwner(w, r)
	if !ok {
		return
	}
	u, ok := s.portalUserOrError(w, r)
	if !ok {
		return
	}
	key, ok := s.portalImageKey(w, r, u)
	if !ok {
		return
	}
	chains, err := s.portalImageChains(r, key, u.PlanID)
	if err != nil {
		s.log.Error("portal images: resolve chains failed", "err", err)
		writeImageError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	var picked *portalImageChain
	for i := range chains {
		if chains[i].chain.ID == chainID {
			picked = &chains[i]
			break
		}
	}
	if picked == nil {
		writeImageError(w, http.StatusNotFound, "model_not_available")
		return
	}

	// Each family takes only its own options; the rest are dropped.
	var size, aspect, resolution string
	switch picked.family {
	case "gpt-image":
		size = strings.ToLower(strings.TrimSpace(body.Size))
		if size != "" && !gptImageSizes[size] {
			writeImageError(w, http.StatusBadRequest, "invalid_size")
			return
		}
	case "grok-imagine":
		aspect = strings.TrimSpace(body.AspectRatio)
		if aspect != "" && !imageAspectRatios[picked.family][aspect] {
			writeImageError(w, http.StatusBadRequest, "invalid_aspect_ratio")
			return
		}
		resolution = strings.ToLower(strings.TrimSpace(body.Resolution))
		if resolution != "" && resolution != "1k" && resolution != "2k" {
			writeImageError(w, http.StatusBadRequest, "invalid_resolution")
			return
		}
	case "gemini-image":
		aspect = strings.TrimSpace(body.AspectRatio)
		if aspect != "" && !imageAspectRatios[picked.family][aspect] {
			writeImageError(w, http.StatusBadRequest, "invalid_aspect_ratio")
			return
		}
	}

	// The job is billed at the server's shown price. It must be the price
	// the page showed: a price that changed since is refused.
	if body.ExpectedPriceMicro == nil || *body.ExpectedPriceMicro <= 0 {
		writeImageError(w, http.StatusConflict, "price_changed")
		return
	}

	lim, err := s.effectiveLimits(r.Context(), key)
	if err != nil {
		s.log.Error("portal images: load limits failed", "err", err)
		writeImageError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	job, err := s.imageGen.Submit(r.Context(), imagegen.SubmitInput{
		OwnerSub: owner, Key: key, ChainID: picked.chain.ID, Targets: picked.targets, Limits: lim,
		Prompt: prompt, N: n, Size: size, AspectRatio: aspect, Resolution: resolution,
		PriceOrder: picked.declared, ExpectedPriceMicro: *body.ExpectedPriceMicro,
	})
	if err != nil {
		s.writeSubmitError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"job": portalImageJobJSON(job)})
}

func (s *Server) writeSubmitError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, imagegen.ErrTooManyActive):
		writeImageError(w, http.StatusConflict, "too_many_active")
	case errors.Is(err, imagegen.ErrInsufficientBalance):
		writeImageError(w, http.StatusPaymentRequired, "insufficient_balance")
	case errors.Is(err, imagegen.ErrNoImageTarget):
		writeImageError(w, http.StatusNotFound, "model_not_available")
	case errors.Is(err, imagegen.ErrEmptyPrompt):
		writeImageError(w, http.StatusBadRequest, "empty_prompt")
	case errors.Is(err, imagegen.ErrPriceChanged):
		writeImageError(w, http.StatusConflict, "price_changed")
	case errors.Is(err, imagegen.ErrQueueFull):
		writeImageError(w, http.StatusServiceUnavailable, "queue_full")
	default:
		s.log.Error("portal images: submit failed", "err", err)
		writeImageError(w, http.StatusInternalServerError, "internal_error")
	}
}

type portalImageFile struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

// portalImageParams is the user-facing part of a job's stored params; the
// upstream model is left out.
type portalImageParams struct {
	ChainID     string `json:"chain_id"`
	Size        string `json:"size"`
	AspectRatio string `json:"aspect_ratio"`
	Resolution  string `json:"resolution"`
	N           int    `json:"n"`
}

type portalImageJob struct {
	ID         string            `json:"id"`
	Status     string            `json:"status"`
	Error      string            `json:"error"`
	Prompt     string            `json:"prompt"`
	Params     portalImageParams `json:"params"`
	ImageCount int               `json:"image_count"`
	CostMicro  int64             `json:"cost_micro"`
	CreatedAt  time.Time         `json:"created_at"`
	FinishedAt *time.Time        `json:"finished_at"`
	ExpiresAt  *time.Time        `json:"expires_at"`
	Files      []portalImageFile `json:"files"`
}

// portalImageJobJSON copies only the fields the owner may see: no key,
// owner, tenant, file path or upstream cost.
func portalImageJobJSON(j store.ImageJob) portalImageJob {
	var p portalImageParams
	_ = json.Unmarshal([]byte(j.Params), &p)
	files := make([]portalImageFile, 0, len(j.Files))
	for _, f := range j.Files {
		files = append(files, portalImageFile{ID: f.ID, URL: "/portal/api/images/files/" + f.ID})
	}
	return portalImageJob{
		ID: j.ID, Status: j.Status, Error: j.Error, Prompt: j.Prompt, Params: p,
		ImageCount: j.ImageCount, CostMicro: j.CostMicro, CreatedAt: j.CreatedAt,
		FinishedAt: j.FinishedAt, ExpiresAt: j.ExpiresAt, Files: files,
	}
}

// handlePortalListImages serves GET /portal/api/images: the session owner's
// newest jobs. Jobs past retention are left out; the sweeper removes them.
func (s *Server) handlePortalListImages(w http.ResponseWriter, r *http.Request) {
	if !s.imageGenReady(w) {
		return
	}
	owner, ok := s.imageOwner(w, r)
	if !ok {
		return
	}
	limit := defaultImageList
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 {
		limit = min(v, maxImageList)
	}
	jobs, err := s.db.ImageJobs().ListByOwner(r.Context(), owner, limit)
	if err != nil {
		s.log.Error("portal images: list jobs failed", "err", err)
		writeImageError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	now := time.Now()
	out := make([]portalImageJob, 0, len(jobs))
	for _, j := range jobs {
		if j.ExpiresAt != nil && !j.ExpiresAt.After(now) {
			continue
		}
		out = append(out, portalImageJobJSON(j))
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": out})
}

// handlePortalImageFile serves GET /portal/api/images/files/{file_id}. Any
// file that is not the session owner's live file is 404.
func (s *Server) handlePortalImageFile(w http.ResponseWriter, r *http.Request) {
	if !s.imageGenReady(w) {
		return
	}
	owner, ok := s.imageOwner(w, r)
	if !ok {
		return
	}
	f, err := s.db.ImageJobs().GetFile(r.Context(), owner, chi.URLParam(r, "file_id"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeImageError(w, http.StatusNotFound, "not_found")
			return
		}
		s.log.Error("portal images: load file failed", "err", err)
		writeImageError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	path, inside := s.imageGen.LocalPath(f)
	if !inside {
		s.log.Error("portal images: stored file outside the images directory", "file", f.ID)
		writeImageError(w, http.StatusNotFound, "not_found")
		return
	}
	file, err := os.Open(path)
	if err != nil {
		s.log.Warn("portal images: open file failed", "file", f.ID, "err", err)
		writeImageError(w, http.StatusNotFound, "not_found")
		return
	}
	defer file.Close()

	ctype := f.Mime
	if !servedImageTypes[ctype] {
		ctype = "application/octet-stream"
	}
	disposition := "inline"
	if r.URL.Query().Get("download") == "1" {
		disposition = "attachment"
	}
	h := w.Header()
	h.Set("Content-Type", ctype)
	h.Set("Cache-Control", "private, no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Disposition", disposition+`; filename="`+f.ID+filepath.Ext(path)+`"`)
	http.ServeContent(w, r, "", time.Time{}, file)
}

// handlePortalDeleteImage serves DELETE /portal/api/images/{job_id}.
func (s *Server) handlePortalDeleteImage(w http.ResponseWriter, r *http.Request) {
	if !s.imageGenReady(w) || !s.imageWriteAllowed(w, r) {
		return
	}
	owner, ok := s.imageOwner(w, r)
	if !ok {
		return
	}
	err := s.imageGen.Delete(r.Context(), owner, chi.URLParam(r, "job_id"))
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, store.ErrNotFound):
		writeImageError(w, http.StatusNotFound, "not_found")
	case errors.Is(err, store.ErrImageJobActive):
		writeImageError(w, http.StatusConflict, "job_active")
	default:
		s.log.Error("portal images: delete job failed", "err", err)
		writeImageError(w, http.StatusInternalServerError, "internal_error")
	}
}
