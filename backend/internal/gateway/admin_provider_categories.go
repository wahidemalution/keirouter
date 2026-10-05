package gateway

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/mydisha/keirouter/backend/internal/store"
)

// mountProviderCategories registers the display-provider category CRUD used by
// the chain editor to label the public model catalog.
func (s *Server) mountProviderCategories(r chi.Router) {
	r.Get("/provider-categories", s.adminListProviderCategories)
	r.Post("/provider-categories", s.adminCreateProviderCategory)
	r.Delete("/provider-categories/{id}", s.adminDeleteProviderCategory)
}

func providerCategoryJSON(c store.ProviderCategory) map[string]any {
	return map[string]any{"id": c.ID, "label": c.Label}
}

func (s *Server) adminListProviderCategories(w http.ResponseWriter, r *http.Request) {
	cats, err := s.db.ProviderCategories().List(r.Context(), adminTenant)
	if err != nil {
		writeError(w, http.StatusInternalServerError, sanitizeError(s.log, err, "internal server error"))
		return
	}
	out := make([]map[string]any, 0, len(cats))
	for _, c := range cats {
		out = append(out, providerCategoryJSON(c))
	}
	writeJSON(w, http.StatusOK, map[string]any{"categories": out})
}

func (s *Server) adminCreateProviderCategory(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID    string `json:"id"`
		Label string `json:"label"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	label := strings.TrimSpace(body.Label)
	if label == "" {
		writeError(w, http.StatusBadRequest, "label is required")
		return
	}
	id := strings.TrimSpace(body.ID)
	if id == "" {
		id = slugify(label)
	}
	if id == "" || !aliasInputRe.MatchString(id) || len(id) > 32 {
		writeError(w, http.StatusBadRequest, "id must be letters, digits, and hyphens (max 32)")
		return
	}
	id = strings.ToLower(id)

	existing, err := s.db.ProviderCategories().List(r.Context(), adminTenant)
	if err != nil {
		writeError(w, http.StatusInternalServerError, sanitizeError(s.log, err, "internal server error"))
		return
	}
	for _, c := range existing {
		if c.ID == id {
			writeError(w, http.StatusConflict, "provider category already exists: "+id)
			return
		}
	}

	cat := store.ProviderCategory{
		ID: id, TenantID: adminTenant, Label: label,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := s.db.ProviderCategories().Create(r.Context(), cat); err != nil {
		writeError(w, http.StatusInternalServerError, sanitizeError(s.log, err, "create provider category failed"))
		return
	}
	writeJSON(w, http.StatusCreated, providerCategoryJSON(cat))
}

func (s *Server) adminDeleteProviderCategory(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.db.ProviderCategories().Delete(r.Context(), id); err != nil {
		writeError(w, http.StatusNotFound, "unknown provider category: "+id)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "deleted": true})
}
