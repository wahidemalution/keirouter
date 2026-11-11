package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"
)

// adminRoutingMux wires the four admin key-routing routes the way mountAdmin
// does, next to the fixture's portal routes for the same key.
func adminRoutingMux(f *modelSettingsFixture) chi.Router {
	mux := chi.NewRouter()
	mux.Get("/api/keys/{id}/model-settings", f.srv.adminKeyModelSettings)
	mux.Put("/api/keys/{id}/allowed-providers", f.srv.adminSetAllowedProviders)
	mux.Put("/api/keys/{id}/model-settings/{model}/providers/{provider}", f.srv.adminSetKeyModelProvider)
	mux.Put("/api/keys/{id}/model-settings/{model}/final-fallback", f.srv.adminSetKeyModelFinalFallback)
	return mux
}

func adminDo(mux chi.Router, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

type adminSettingsJSON struct {
	Models           []settingsRowJSON `json:"models"`
	AllowedProviders []string          `json:"allowed_providers"`
	ProviderOptions  []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"provider_options"`
}

func getAdminSettings(t *testing.T, mux chi.Router, keyID string) adminSettingsJSON {
	t.Helper()
	rec := adminDo(mux, http.MethodGet, "/api/keys/"+keyID+"/model-settings", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var out adminSettingsJSON
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	return out
}

func TestAdminKeyRoutingAllowedProviders(t *testing.T) {
	f := newModelSettingsFixture(t, twoProviderChain())
	mux := adminRoutingMux(f)

	got := getAdminSettings(t, mux, f.keyID)
	require.Equal(t, []string{}, got.AllowedProviders)
	ids := []string{}
	for _, o := range got.ProviderOptions {
		ids = append(ids, o.ID)
	}
	require.ElementsMatch(t, []string{"custom-openai-surplus-openrouter", "custom-openai-surplus-gone", "deepseek"}, ids)
	for i := 1; i < len(got.ProviderOptions); i++ {
		require.LessOrEqual(t, strings.ToLower(got.ProviderOptions[i-1].Name), strings.ToLower(got.ProviderOptions[i].Name))
	}

	path := "/api/keys/" + f.keyID + "/allowed-providers"
	rec := adminDo(mux, http.MethodPut, path, `{"providers":["deepseek"]}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, []string{"deepseek"}, getAdminSettings(t, mux, f.keyID).AllowedProviders)

	_, _, rows := f.list(t)
	require.Len(t, rows, 1)
	for _, p := range rows[0].Providers {
		require.Equal(t, p.ID != "deepseek", p.Locked, p.ID)
	}

	rec = adminDo(mux, http.MethodPut, path, `{"providers":["deepseek","no-such-provider"]}`)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "unknown provider: no-such-provider")
	require.Equal(t, []string{"deepseek"}, getAdminSettings(t, mux, f.keyID).AllowedProviders)

	rec = adminDo(mux, http.MethodPut, path, `{"providers":[]}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, []string{}, getAdminSettings(t, mux, f.keyID).AllowedProviders)

	rec = adminDo(mux, http.MethodPut, "/api/keys/missing/allowed-providers", `{"providers":[]}`)
	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Contains(t, rec.Body.String(), "key not found")
}

func TestAdminKeyRoutingModelSettings(t *testing.T) {
	f := newModelSettingsFixture(t, twoProviderChain())
	mux := adminRoutingMux(f)
	base := "/api/keys/" + f.keyID + "/model-settings/deepseek-v4.1-flash"

	rec := adminDo(mux, http.MethodPut, base+"/final-fallback", `{"enabled":true}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var row settingsRowJSON
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &row))
	require.True(t, row.FinalFallback)

	_, _, rows := f.list(t)
	require.Len(t, rows, 1)
	require.True(t, rows[0].FinalFallback)
	// The portal sees the same rows with marketplace ids and names redacted.
	adminRows := getAdminSettings(t, mux, f.keyID).Models
	for i := range adminRows {
		for j := range adminRows[i].Providers {
			p := &adminRows[i].Providers[j]
			p.Name, p.ID = portalProviderName(p.Name, p.ID), portalProviderID(p.ID)
		}
	}
	require.Equal(t, adminRows, rows)

	rec = adminDo(mux, http.MethodPut, base+"/providers/deepseek", `{"enabled":false}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	_, _, rows = f.list(t)
	for _, p := range rows[0].Providers {
		require.Equal(t, p.ID != "deepseek", p.Enabled, p.ID)
	}

	rec = adminDo(mux, http.MethodPut, base+"/providers/nope", `{"enabled":true}`)
	require.Equal(t, http.StatusNotFound, rec.Code)
	rec = adminDo(mux, http.MethodPut, "/api/keys/"+f.keyID+"/model-settings/no-such-model/final-fallback", `{"enabled":true}`)
	require.Equal(t, http.StatusNotFound, rec.Code)
	rec = adminDo(mux, http.MethodPut, base+"/final-fallback", `{}`)
	require.Equal(t, http.StatusBadRequest, rec.Code)

	// A provider outside the ceiling cannot be turned on, even by an admin.
	require.Equal(t, http.StatusOK, adminDo(mux, http.MethodPut,
		"/api/keys/"+f.keyID+"/allowed-providers", `{"providers":["custom-openai-surplus-openrouter"]}`).Code)
	rec = adminDo(mux, http.MethodPut, base+"/providers/deepseek", `{"enabled":true}`)
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Contains(t, rec.Body.String(), "locked")
}
