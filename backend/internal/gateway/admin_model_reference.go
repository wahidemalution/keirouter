package gateway

import (
	"net/http"
	"strings"
)

// adminModelReference returns the models.dev list price of one model, used by
// the chain editor to fill a discount step. Cache prices are null when
// models.dev lists none, so the editor leaves them to derive from input.
func (s *Server) adminModelReference(w http.ResponseWriter, r *http.Request) {
	model := strings.TrimSpace(r.URL.Query().Get("model"))
	if model == "" {
		writeError(w, http.StatusBadRequest, "model is required")
		return
	}
	ref, ok := s.references.Lookup(model)
	if !ok || !ref.Valid() {
		writeError(w, http.StatusNotFound, "models.dev has no price for "+model)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"model":             model,
		"provider":          ref.Provider,
		"input_per_m":       ref.InputPerM,
		"output_per_m":      ref.OutputPerM,
		"cache_read_per_m":  listedRate(ref.CacheReadPerM),
		"cache_write_per_m": listedRate(ref.CacheWritePerM),
	})
}

// listedRate is a listed price as-is, or nil when unlisted (0). It does not
// round: the editor stores what models.dev lists.
func listedRate(v float64) *float64 {
	if v <= 0 {
		return nil
	}
	return &v
}
