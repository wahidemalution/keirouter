package gateway

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/mydisha/keirouter/backend/internal/store"
	"github.com/mydisha/keirouter/backend/internal/tokenpkg"
)

func (s *Server) mountTokenPackages(r chi.Router) {
	r.Get("/token-packages/settings", s.adminGetTokenSettings)
	r.Put("/token-packages/settings", s.adminPutTokenSettings)
	r.Get("/token-packages/subscriptions", s.adminListTokenSubscriptions)
	r.Post("/token-packages/subscriptions/{sub}/key/toggle", s.adminToggleTokenKey)
	r.Get("/token-packages", s.adminListTokenPackages)
	r.Post("/token-packages", s.adminCreateTokenPackage)
	r.Put("/token-packages/{id}", s.adminUpdateTokenPackage)
	r.Delete("/token-packages/{id}", s.adminDeleteTokenPackage)
}

// isTokenPlan reports whether planID is the plan reserved for token package
// keys. Such a key carries a token budget and an expiry, so putting a PAYG
// key on that plan would leave it without either.
func (s *Server) isTokenPlan(ctx context.Context, planID string) (bool, error) {
	if planID == "" {
		return false, nil
	}
	st, err := tokenpkg.Load(ctx, s.db.Settings())
	if err != nil {
		return false, err
	}
	return planID == st.PlanID, nil
}

const tokenPlanReservedMsg = "this plan is reserved for token packages"

// refuseTokenKeyCredit writes 409 and reports true when keyID is a token
// package key: those keys carry a token budget and never PAYG credit.
func (s *Server) refuseTokenKeyCredit(ctx context.Context, w http.ResponseWriter, keyID string) bool {
	_, err := s.db.TokenSubscriptions().GetByKey(ctx, keyID)
	switch {
	case err == nil:
		writeError(w, http.StatusConflict, "credit cannot be added to a token package key")
		return true
	case errors.Is(err, store.ErrNotFound):
		return false
	default:
		writeError(w, http.StatusInternalServerError, sanitizeError(s.log, err, "internal server error"))
		return true
	}
}

func tokenPackageView(p store.TokenPackage) map[string]any {
	return map[string]any{
		"id": p.ID, "name": p.Name, "kind": string(p.Kind), "tokens": p.Tokens, "days": p.Days,
		"price_idr": p.PriceIDR, "enabled": p.Enabled, "featured": p.Featured, "sort": p.Sort,
	}
}

type tokenPackageRequest struct {
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Tokens   int64  `json:"tokens"`
	Days     int    `json:"days"`
	PriceIDR int64  `json:"price_idr"`
	Enabled  *bool  `json:"enabled"`
	Featured *bool  `json:"featured"`
	Sort     int    `json:"sort"`
}

// apply validates body and writes it onto p. A days package carries no
// tokens, so tokens is zeroed for it.
func (body tokenPackageRequest) apply(p *store.TokenPackage) error {
	name := strings.TrimSpace(body.Name)
	if name == "" {
		return errors.New("name is required")
	}
	if body.Days < 1 {
		return errors.New("days must be at least 1")
	}
	if body.PriceIDR <= 0 {
		return errors.New("price_idr must be positive")
	}
	kind := store.TokenPackageKind(body.Kind)
	switch kind {
	case store.TokenPackageTokens:
		if body.Tokens <= 0 {
			return errors.New("tokens must be positive for a tokens package")
		}
		p.Tokens = body.Tokens
	case store.TokenPackageDays:
		p.Tokens = 0
	default:
		return errors.New("kind must be tokens or days")
	}
	p.Name, p.Kind, p.Days, p.PriceIDR, p.Sort = name, kind, body.Days, body.PriceIDR, body.Sort
	if body.Enabled != nil {
		p.Enabled = *body.Enabled
	}
	if body.Featured != nil {
		p.Featured = *body.Featured
	}
	return nil
}

func (s *Server) adminListTokenPackages(w http.ResponseWriter, r *http.Request) {
	pkgs, err := s.db.TokenPackages().List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, sanitizeError(s.log, err, "internal server error"))
		return
	}
	out := make([]map[string]any, 0, len(pkgs))
	for _, p := range pkgs {
		out = append(out, tokenPackageView(p))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) adminCreateTokenPackage(w http.ResponseWriter, r *http.Request) {
	var body tokenPackageRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	now := time.Now()
	p := store.TokenPackage{ID: uuid.NewString(), TenantID: store.DefaultTenantID, CreatedAt: now, UpdatedAt: now}
	if err := body.apply(&p); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.db.TokenPackages().Create(r.Context(), p); err != nil {
		writeError(w, http.StatusInternalServerError, sanitizeError(s.log, err, "internal server error"))
		return
	}
	writeJSON(w, http.StatusCreated, tokenPackageView(p))
}

func (s *Server) adminUpdateTokenPackage(w http.ResponseWriter, r *http.Request) {
	var body tokenPackageRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	repo := s.db.TokenPackages()
	p, err := repo.Get(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "package not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, sanitizeError(s.log, err, "internal server error"))
		return
	}
	if err := body.apply(&p); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	p.UpdatedAt = time.Now()
	if err := repo.Update(r.Context(), p); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "package not found")
			return
		}
		writeError(w, http.StatusInternalServerError, sanitizeError(s.log, err, "internal server error"))
		return
	}
	writeJSON(w, http.StatusOK, tokenPackageView(p))
}

func (s *Server) adminDeleteTokenPackage(w http.ResponseWriter, r *http.Request) {
	err := s.db.TokenPackages().Delete(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "package not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, sanitizeError(s.log, err, "internal server error"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// tokenPlanModels returns the token plan's allowed models, never nil.
func tokenPlanModels(plan store.Plan) []string {
	if m := store.GetPlanAllowedModels(plan); m != nil {
		return m
	}
	return []string{}
}

func tokenSettingsView(st tokenpkg.Settings, plan store.Plan) map[string]any {
	return map[string]any{
		"enabled": st.Enabled, "allowed_models": tokenPlanModels(plan),
		"rpm_limit": plan.RPMLimit, "concurrency_limit": plan.ConcurrencyLimit,
	}
}

func (s *Server) adminGetTokenSettings(w http.ResponseWriter, r *http.Request) {
	st, err := tokenpkg.Load(r.Context(), s.db.Settings())
	if err != nil {
		writeError(w, http.StatusInternalServerError, sanitizeError(s.log, err, "internal server error"))
		return
	}
	plan, err := s.db.Plans().Get(r.Context(), st.PlanID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusInternalServerError, sanitizeError(s.log, err, "internal server error"))
		return
	}
	writeJSON(w, http.StatusOK, tokenSettingsView(st, plan))
}

// adminPutTokenSettings updates the sale switch and the token plan's model
// list and rate limits. Fields left out keep their value. The plan itself
// cannot be changed here.
func (s *Server) adminPutTokenSettings(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled          *bool     `json:"enabled"`
		AllowedModels    *[]string `json:"allowed_models"`
		RPMLimit         *int64    `json:"rpm_limit"`
		ConcurrencyLimit *int64    `json:"concurrency_limit"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	ctx := r.Context()
	st, err := tokenpkg.Load(ctx, s.db.Settings())
	if err != nil {
		writeError(w, http.StatusInternalServerError, sanitizeError(s.log, err, "internal server error"))
		return
	}
	plan, err := s.db.Plans().Get(ctx, st.PlanID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusBadRequest, "token plan not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, sanitizeError(s.log, err, "internal server error"))
		return
	}
	if body.Enabled != nil {
		st.Enabled = *body.Enabled
	}
	if body.AllowedModels != nil {
		models := make([]string, 0, len(*body.AllowedModels))
		seen := map[string]bool{}
		for _, m := range *body.AllowedModels {
			if m = strings.TrimSpace(m); m != "" && !seen[m] {
				seen[m] = true
				models = append(models, m)
			}
		}
		if len(models) == 0 {
			writeError(w, http.StatusBadRequest, "allowed_models must not be empty")
			return
		}
		plan.AllowedModels = store.SetPlanAllowedModels(models)
	}
	if body.RPMLimit != nil {
		if *body.RPMLimit < 0 {
			writeError(w, http.StatusBadRequest, "rpm_limit must not be negative")
			return
		}
		plan.RPMLimit = *body.RPMLimit
	}
	if body.ConcurrencyLimit != nil {
		if *body.ConcurrencyLimit < 0 {
			writeError(w, http.StatusBadRequest, "concurrency_limit must not be negative")
			return
		}
		plan.ConcurrencyLimit = *body.ConcurrencyLimit
	}
	if body.AllowedModels != nil || body.RPMLimit != nil || body.ConcurrencyLimit != nil {
		plan.UpdatedAt = time.Now()
		if err := s.db.Plans().Update(ctx, plan); err != nil {
			writeError(w, http.StatusInternalServerError, sanitizeError(s.log, err, "internal server error"))
			return
		}
	}
	if err := tokenpkg.Save(ctx, s.db.Settings(), st); err != nil {
		writeError(w, http.StatusInternalServerError, sanitizeError(s.log, err, "internal server error"))
		return
	}
	writeJSON(w, http.StatusOK, tokenSettingsView(st, plan))
}

func (s *Server) adminListTokenSubscriptions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	subs, err := s.db.TokenSubscriptions().List(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, sanitizeError(s.log, err, "internal server error"))
		return
	}
	users, err := s.db.PortalUsers().List(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, sanitizeError(s.log, err, "internal server error"))
		return
	}
	emails := make(map[string]string, len(users))
	for _, u := range users {
		emails[u.GoogleSub] = u.Email
	}
	now := time.Now()
	out := make([]map[string]any, 0, len(subs))
	for _, sub := range subs {
		key, err := s.identity.Get(ctx, sub.KeyID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, sanitizeError(s.log, err, "internal server error"))
			return
		}
		used, limit, err := s.tokenUsage(ctx, sub.KeyID, now)
		if err != nil {
			writeError(w, http.StatusInternalServerError, sanitizeError(s.log, err, "internal server error"))
			return
		}
		out = append(out, map[string]any{
			"google_sub": sub.GoogleSub, "email": emails[sub.GoogleSub], "key_id": sub.KeyID,
			"key_display": key.Display, "used_tokens": used, "limit_tokens": limit,
			"expires_at": sub.ExpiresAt.UTC().Format(time.RFC3339), "active": now.Before(sub.ExpiresAt),
			"disabled": key.Disabled,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// adminToggleTokenKey flips the disabled flag of a subscriber's token key.
func (s *Server) adminToggleTokenKey(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sub, err := s.db.TokenSubscriptions().GetBySub(ctx, chi.URLParam(r, "sub"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "token subscription not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, sanitizeError(s.log, err, "internal server error"))
		return
	}
	key, err := s.identity.Get(ctx, sub.KeyID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, sanitizeError(s.log, err, "internal server error"))
		return
	}
	disabled := !key.Disabled
	if err := s.identity.SetDisabled(ctx, sub.KeyID, disabled); err != nil {
		writeError(w, http.StatusInternalServerError, sanitizeError(s.log, err, "internal server error"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"disabled": disabled})
}
