package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"
)

func newCategoryTestServer(t *testing.T) *Server {
	t.Helper()
	s, db := newCustomProviderTestServer(t)
	s.chains = db.Chains()
	return s
}

func createCategory(t *testing.T, s *Server, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/provider-categories", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.adminCreateProviderCategory(rec, req)
	var out map[string]any
	if rec.Body.Len() > 0 {
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	}
	return rec.Code, out
}

func TestProviderCategoryCreateAndList(t *testing.T) {
	s := newCategoryTestServer(t)

	code, out := createCategory(t, s, `{"label":"DeepSeek"}`)
	require.Equal(t, http.StatusCreated, code)
	require.Equal(t, "deepseek", out["id"])
	require.Equal(t, "DeepSeek", out["label"])

	req := httptest.NewRequest(http.MethodGet, "/provider-categories", nil)
	rec := httptest.NewRecorder()
	s.adminListProviderCategories(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	var list struct {
		Categories []map[string]any `json:"categories"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	require.Len(t, list.Categories, 1)
}

func TestProviderCategoryExplicitIDAndCollision(t *testing.T) {
	s := newCategoryTestServer(t)

	code, out := createCategory(t, s, `{"id":"xai","label":"xAI"}`)
	require.Equal(t, http.StatusCreated, code)
	require.Equal(t, "xai", out["id"])

	code, _ = createCategory(t, s, `{"id":"xai","label":"Duplicate"}`)
	require.Equal(t, http.StatusConflict, code)
}

func TestProviderCategoryDelete(t *testing.T) {
	s := newCategoryTestServer(t)
	require.Equal(t, http.StatusCreated, func() int { c, _ := createCategory(t, s, `{"label":"OpenAI"}`); return c }())

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "openai")
	req := httptest.NewRequest(http.MethodDelete, "/provider-categories/openai", nil).
		WithContext(context.WithValue(context.Background(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	s.adminDeleteProviderCategory(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
}

func TestChainPersistsDisplayProvider(t *testing.T) {
	s := newCategoryTestServer(t)

	code, _, _ := postChain(t, s, `{"name":"cat-chain","strategy":"priority","display_provider":"deepseek","steps":[{"provider":"custom-openai","model":"m1"}]}`)
	require.Equal(t, http.StatusCreated, code)

	chains := listChains(t, s)
	require.Len(t, chains, 1)
	require.Equal(t, "deepseek", chains[0]["display_provider"])
}
