package gateway

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/mydisha/keirouter/backend/internal/store"
)

// handlePortalModelSettings serves GET /portal/api/model-settings: the key's
// default and maximum minimum discount and, for every chain the key may call,
// its discount, provider toggles and final-fallback setting.
func (s *Server) handlePortalModelSettings(w http.ResponseWriter, r *http.Request) {
	u, ok := s.portalUserOrError(w, r)
	if !ok {
		return
	}
	key, ok := s.portalKey(w, r, u)
	if !ok {
		return
	}
	chains, err := s.portalChains(r, key, u.PlanID)
	if err != nil {
		s.log.Error("portal model settings: list models failed", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to load models")
		return
	}
	resp, err := s.buildModelSettings(r.Context(), key, chains)
	if err != nil {
		s.log.Error("portal model settings: build failed", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to load model settings")
		return
	}
	for i := range resp.Models {
		redactModelSettingsRow(&resp.Models[i])
	}
	writeJSON(w, http.StatusOK, resp)
}

// handlePortalSetModelProvider serves
// PUT /portal/api/model-settings/{model}/providers/{provider}.
func (s *Server) handlePortalSetModelProvider(w http.ResponseWriter, r *http.Request) {
	key, chain, ok := s.portalModelTarget(w, r)
	if !ok {
		return
	}
	s.setModelProvider(w, r, key, chain, true)
}

// handlePortalSetModelFinalFallback serves
// PUT /portal/api/model-settings/{model}/final-fallback.
func (s *Server) handlePortalSetModelFinalFallback(w http.ResponseWriter, r *http.Request) {
	key, chain, ok := s.portalModelTarget(w, r)
	if !ok {
		return
	}
	s.setModelFinalFallback(w, r, key, chain, true)
}

// setModelProvider turns one provider on or off for a key's model and answers
// with the model's updated row. The portal and the admin share it, so both
// refuse to enable a provider outside the key's admin ceiling. A portal
// request names the provider by its portal id and gets a redacted row back.
func (s *Server) setModelProvider(w http.ResponseWriter, r *http.Request, key store.APIKey, chain store.Chain, portal bool) {
	provider, found := pathParam(r, "provider"), false
	if portal {
		provider, found = resolvePortalProvider(chain, provider)
	} else {
		found = chainHasStepProvider(chain, provider)
	}
	if !found {
		writeError(w, http.StatusNotFound, "provider not in this model's chain")
		return
	}
	enabled, ok := decodeEnabled(w, r)
	if !ok {
		return
	}
	routing, err := s.identity.Keys().KeyRouting(r.Context(), key.ID)
	if err != nil {
		s.log.Error("model settings: load key routing failed", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to load model settings")
		return
	}
	key.AllowedProviders = routing.AllowedProviders
	if enabled && !key.ProviderAllowed(provider) {
		writeError(w, http.StatusForbidden, "this provider is locked by your administrator")
		return
	}
	if err := s.identity.Keys().SetModelProviderEnabled(r.Context(), key.ID, chain.Name, provider, enabled); err != nil {
		s.log.Error("model settings: save provider failed", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to save provider setting")
		return
	}
	s.identity.InvalidateAuthCacheForKey(key.ID)
	s.writeModelSettingsRow(w, r, key, chain, portal)
}

// setModelFinalFallback turns the final fallback on or off for a key's model
// and answers with the model's updated row.
func (s *Server) setModelFinalFallback(w http.ResponseWriter, r *http.Request, key store.APIKey, chain store.Chain, portal bool) {
	enabled, ok := decodeEnabled(w, r)
	if !ok {
		return
	}
	if err := s.identity.Keys().SetModelFinalFallback(r.Context(), key.ID, chain.Name, enabled); err != nil {
		s.log.Error("model settings: save final fallback failed", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to save final fallback")
		return
	}
	s.identity.InvalidateAuthCacheForKey(key.ID)
	s.writeModelSettingsRow(w, r, key, chain, portal)
}

// writeModelSettingsRow answers a successful write with the model's updated
// row, redacted for the portal.
func (s *Server) writeModelSettingsRow(w http.ResponseWriter, r *http.Request, key store.APIKey, chain store.Chain, portal bool) {
	resp, err := s.buildModelSettings(r.Context(), key, []store.Chain{chain})
	if err != nil || len(resp.Models) != 1 {
		s.log.Error("model settings: build row failed", "model", chain.Name, "err", err)
		writeError(w, http.StatusInternalServerError, "failed to load model settings")
		return
	}
	row := resp.Models[0]
	if portal {
		redactModelSettingsRow(&row)
	}
	writeJSON(w, http.StatusOK, row)
}

// portalKey loads the portal user's key.
func (s *Server) portalKey(w http.ResponseWriter, r *http.Request, u store.PortalUser) (store.APIKey, bool) {
	key, err := s.identity.Get(r.Context(), u.KeyID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "key no longer exists")
			return store.APIKey{}, false
		}
		s.log.Error("portal model settings: load key failed", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to load key")
		return store.APIKey{}, false
	}
	return key, true
}

// portalModelTarget resolves the signed-in user, their key and the {model}
// path parameter, which must be a chain the key may call.
func (s *Server) portalModelTarget(w http.ResponseWriter, r *http.Request) (store.APIKey, store.Chain, bool) {
	u, ok := s.portalUserOrError(w, r)
	if !ok {
		return store.APIKey{}, store.Chain{}, false
	}
	key, ok := s.portalKey(w, r, u)
	if !ok {
		return store.APIKey{}, store.Chain{}, false
	}
	chains, err := s.portalChains(r, key, u.PlanID)
	if err != nil {
		s.log.Error("portal model settings: list models failed", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to load models")
		return store.APIKey{}, store.Chain{}, false
	}
	chain, ok := findKeyChain(w, r, chains)
	return key, chain, ok
}

// findKeyChain picks the chain named by the {model} path parameter out of the
// chains a key may call, answering 404 when there is none.
func findKeyChain(w http.ResponseWriter, r *http.Request, chains []store.Chain) (store.Chain, bool) {
	model := store.NormalizeDiscountModel(pathParam(r, "model"))
	for _, c := range chains {
		if store.NormalizeDiscountModel(c.Name) == model {
			return c, true
		}
	}
	writeError(w, http.StatusNotFound, "model not found for this key")
	return store.Chain{}, false
}

// pathParam reads a URL parameter exactly once-decoded. chi matches on the
// raw path when the request has one (a model name with a slash arrives as
// "Ali%2FGLM-5"), so the value is decoded here only in that case; without a
// RawPath chi's value is already decoded and a literal '%' must survive.
func pathParam(r *http.Request, name string) string {
	v := chi.URLParam(r, name)
	if r.URL.RawPath != "" {
		if dec, err := url.PathUnescape(v); err == nil {
			v = dec
		}
	}
	return strings.TrimSpace(v)
}

func chainHasStepProvider(c store.Chain, provider string) bool {
	for _, st := range c.Steps {
		if st.Provider == provider {
			return true
		}
	}
	return false
}

// decodeEnabled reads {"enabled": bool}; the field is required.
func decodeEnabled(w http.ResponseWriter, r *http.Request) (bool, bool) {
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<10)).Decode(&body); err != nil || body.Enabled == nil {
		writeError(w, http.StatusBadRequest, "body must be {\"enabled\": true|false}")
		return false, false
	}
	return *body.Enabled, true
}
