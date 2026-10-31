package gateway

import (
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/mydisha/keirouter/backend/internal/costlookup"
	"github.com/mydisha/keirouter/backend/internal/store"
)

// adminCostLookupReport compares one UTC day (?day=YYYY-MM-DD, default
// yesterday) of the provider's logged charge with keirouter's records.
func (s *Server) adminCostLookupReport(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	p, err := s.db.CustomProviders().GetProvider(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "unknown custom provider: "+id)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, sanitizeError(s.log, err, "internal server error"))
		return
	}
	if p.CostLookup != store.CostLookupSubrouter {
		writeError(w, http.StatusBadRequest, "cost lookup is off for this provider")
		return
	}
	day := time.Now().UTC().Add(-24 * time.Hour)
	if v := r.URL.Query().Get("day"); v != "" {
		d, err := time.Parse("2006-01-02", v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "day must be YYYY-MM-DD")
			return
		}
		day = d
	}
	token, err := costlookup.OpenToken(s.vault.Sealer(), p.CostLookupToken)
	if err != nil {
		writeError(w, http.StatusInternalServerError, sanitizeError(s.log, err, "stored access token cannot be read"))
		return
	}
	client := &costlookup.Client{BaseURL: p.CostLookupBaseURL, Token: token, UserID: p.CostLookupUserID,
		TokenName: p.CostLookupTokenName, HTTP: &http.Client{Timeout: 30 * time.Second}}
	report, err := costlookup.BuildReport(r.Context(), p.ID, day, client.StatQuota, s.db.Usage())
	switch {
	case errors.Is(err, costlookup.ErrUnauthorized):
		writeError(w, http.StatusBadGateway, "Subrouter rejected the access token")
		return
	case errors.Is(err, costlookup.ErrTransient):
		writeError(w, http.StatusBadGateway, "Subrouter usage log is unavailable, try again shortly")
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, sanitizeError(s.log, err, "internal server error"))
		return
	}
	writeJSON(w, http.StatusOK, report)
}
