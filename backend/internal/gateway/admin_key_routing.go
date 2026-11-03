package gateway

import (
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/mydisha/keirouter/backend/internal/store"
)

// providerOption is one keirouter provider an admin may put on a key's
// allowed-providers ceiling.
type providerOption struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// adminKeyModelSettingsResponse is the portal's Model Settings page for a key
// plus what an admin needs to edit the key's provider ceiling.
type adminKeyModelSettingsResponse struct {
	modelSettingsResponse
	AllowedProviders []string         `json:"allowed_providers"`
	ProviderOptions  []providerOption `json:"provider_options"`
}

// adminKeyByID loads the key named by the {id} path parameter, answering 404
// for an unknown id.
func (s *Server) adminKeyByID(w http.ResponseWriter, r *http.Request) (store.APIKey, bool) {
	key, err := s.identity.Get(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "key not found")
			return store.APIKey{}, false
		}
		writeError(w, http.StatusInternalServerError, sanitizeError(s.log, err, "internal server error"))
		return store.APIKey{}, false
	}
	return key, true
}

// adminKeyModelTarget resolves the key and the {model} path parameter, which
// must be a chain the key may call, the same rows the key's owner sees.
func (s *Server) adminKeyModelTarget(w http.ResponseWriter, r *http.Request) (store.APIKey, store.Chain, bool) {
	key, ok := s.adminKeyByID(w, r)
	if !ok {
		return store.APIKey{}, store.Chain{}, false
	}
	chains, err := s.keyChains(r, key, key.PlanID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, sanitizeError(s.log, err, "failed to load models"))
		return store.APIKey{}, store.Chain{}, false
	}
	chain, ok := findKeyChain(w, r, chains)
	return key, chain, ok
}

// providerOptions lists the distinct providers used by steps of the tenant's
// chains, sorted by display name.
func (s *Server) providerOptions(r *http.Request) ([]providerOption, error) {
	chains, err := s.chains.ListByTenant(r.Context(), adminTenant)
	if err != nil {
		return nil, err
	}
	names := s.providerNames(r.Context())
	seen := map[string]bool{}
	out := []providerOption{}
	for _, c := range chains {
		for _, st := range c.Steps {
			if st.Provider == "" || seen[st.Provider] {
				continue
			}
			seen[st.Provider] = true
			out = append(out, providerOption{ID: st.Provider, Name: providerDisplayName(names, st.Provider)})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := strings.ToLower(out[i].Name), strings.ToLower(out[j].Name)
		if a != b {
			return a < b
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// adminKeyModelSettings serves GET /api/keys/{id}/model-settings.
func (s *Server) adminKeyModelSettings(w http.ResponseWriter, r *http.Request) {
	key, ok := s.adminKeyByID(w, r)
	if !ok {
		return
	}
	chains, err := s.keyChains(r, key, key.PlanID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, sanitizeError(s.log, err, "failed to load models"))
		return
	}
	resp, err := s.buildModelSettings(r.Context(), key, chains)
	if err != nil {
		writeError(w, http.StatusInternalServerError, sanitizeError(s.log, err, "failed to load model settings"))
		return
	}
	routing, err := s.identity.Keys().KeyRouting(r.Context(), key.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, sanitizeError(s.log, err, "failed to load model settings"))
		return
	}
	options, err := s.providerOptions(r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, sanitizeError(s.log, err, "failed to load providers"))
		return
	}
	allowed := routing.AllowedProviders
	if allowed == nil {
		allowed = []string{}
	}
	writeJSON(w, http.StatusOK, adminKeyModelSettingsResponse{
		modelSettingsResponse: resp,
		AllowedProviders:      allowed,
		ProviderOptions:       options,
	})
}

// adminSetAllowedProviders serves PUT /api/keys/{id}/allowed-providers. An
// empty list clears the ceiling, which means every provider.
func (s *Server) adminSetAllowedProviders(w http.ResponseWriter, r *http.Request) {
	key, ok := s.adminKeyByID(w, r)
	if !ok {
		return
	}
	var body struct {
		Providers []string `json:"providers"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	options, err := s.providerOptions(r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, sanitizeError(s.log, err, "failed to load providers"))
		return
	}
	known := make(map[string]bool, len(options))
	for _, o := range options {
		known[o.ID] = true
	}
	for _, p := range body.Providers {
		if p = strings.TrimSpace(p); p != "" && !known[p] {
			writeError(w, http.StatusBadRequest, "unknown provider: "+p)
			return
		}
	}
	if err := s.identity.Keys().SetAllowedProviders(r.Context(), key.ID, body.Providers); err != nil {
		writeError(w, http.StatusInternalServerError, sanitizeError(s.log, err, "failed to save allowed providers"))
		return
	}
	s.identity.InvalidateAuthCacheForKey(key.ID)
	routing, err := s.identity.Keys().KeyRouting(r.Context(), key.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, sanitizeError(s.log, err, "failed to load allowed providers"))
		return
	}
	allowed := routing.AllowedProviders
	if allowed == nil {
		allowed = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"allowed_providers": allowed})
}

// adminSetKeyModelProvider serves
// PUT /api/keys/{id}/model-settings/{model}/providers/{provider}.
func (s *Server) adminSetKeyModelProvider(w http.ResponseWriter, r *http.Request) {
	key, chain, ok := s.adminKeyModelTarget(w, r)
	if !ok {
		return
	}
	s.setModelProvider(w, r, key, chain, false)
}

// adminSetKeyModelFinalFallback serves
// PUT /api/keys/{id}/model-settings/{model}/final-fallback.
func (s *Server) adminSetKeyModelFinalFallback(w http.ResponseWriter, r *http.Request) {
	key, chain, ok := s.adminKeyModelTarget(w, r)
	if !ok {
		return
	}
	s.setModelFinalFallback(w, r, key, chain, false)
}
