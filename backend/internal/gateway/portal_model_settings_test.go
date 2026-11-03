package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/market"
	"github.com/mydisha/keirouter/backend/internal/store"
)

type modelSettingsFixture struct {
	srv   *Server
	tok   string
	keyID string
	plain string
	mux   chi.Router
}

// newModelSettingsFixture claims a fresh key for a portal user and wires the
// three Model Settings routes onto a chi router, so URL escaping is exercised
// the way the real server routes it.
func newModelSettingsFixture(t *testing.T, chains ...store.Chain) *modelSettingsFixture {
	t.Helper()
	srv := newPortalTestServer(t)
	ctx := context.Background()
	now := time.Now().UTC()
	for _, c := range chains {
		c.TenantID = store.DefaultTenantID
		c.CreatedAt, c.UpdatedAt = now, now
		require.NoError(t, srv.db.Chains().Create(ctx, c))
	}
	issued, err := srv.identity.Create(ctx, store.DefaultTenantID, "", "portal-key")
	require.NoError(t, err)
	tok, err := srv.auth.IssuePortalSession("portal:sub-model-settings", "m@example.com")
	require.NoError(t, err)
	claimReq := httptest.NewRequest(http.MethodPost, "/portal/auth/claim",
		strings.NewReader(`{"api_key":"`+issued.Plaintext+`"}`))
	claimReq.AddCookie(&http.Cookie{Name: portalSessionCookie, Value: tok})
	claimRec := httptest.NewRecorder()
	srv.handlePortalClaim(claimRec, claimReq)
	require.Equal(t, http.StatusOK, claimRec.Code)

	mux := chi.NewRouter()
	mux.Get("/portal/api/model-settings", srv.handlePortalModelSettings)
	mux.Put("/portal/api/model-settings/{model}/providers/{provider}", srv.handlePortalSetModelProvider)
	mux.Put("/portal/api/model-settings/{model}/final-fallback", srv.handlePortalSetModelFinalFallback)
	return &modelSettingsFixture{srv: srv, tok: tok, keyID: issued.Record.ID, plain: issued.Plaintext, mux: mux}
}

func (f *modelSettingsFixture) do(method, path, body string, session bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if session {
		req.AddCookie(&http.Cookie{Name: portalSessionCookie, Value: f.tok})
	}
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	return rec
}

type settingsProviderJSON struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Enabled    bool     `json:"enabled"`
	Locked     bool     `json:"locked"`
	Available  bool     `json:"available"`
	InputPerM  *float64 `json:"input_per_m"`
	OutputPerM *float64 `json:"output_per_m"`
}

type settingsRowJSON struct {
	Model              string                 `json:"model"`
	Supported          bool                   `json:"supported"`
	OverridePct        *int                   `json:"override_pct"`
	EffectivePct       int                    `json:"effective_pct"`
	Providers          []settingsProviderJSON `json:"providers"`
	HasFallback        bool                   `json:"has_fallback"`
	FinalFallback      bool                   `json:"final_fallback"`
	FinalFallbackKept  bool                   `json:"final_fallback_kept"`
	FallbackInputPerM  float64                `json:"fallback_input_per_m"`
	FallbackOutputPerM float64                `json:"fallback_output_per_m"`
}

func (f *modelSettingsFixture) list(t *testing.T) (int, int, []settingsRowJSON) {
	t.Helper()
	rec := f.do(http.MethodGet, "/portal/api/model-settings", "", true)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var out struct {
		DefaultPct int               `json:"default_pct"`
		MaxPct     int               `json:"max_pct"`
		Models     []settingsRowJSON `json:"models"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	return out.DefaultPct, out.MaxPct, out.Models
}

func twoProviderChain() store.Chain {
	markup := 10.0
	return store.Chain{
		ID: "ms1", Name: "deepseek-v4.1-flash", Strategy: "priority", MarkupPercent: &markup,
		FallbackProvider: "openai", FallbackModel: "gpt-x", InputPerM: 0.3, OutputPerM: 1.2,
		Steps: []store.ChainStep{
			{ID: "ms1a", Position: 0, Provider: "custom-openai-surplus-openrouter", Model: "deepseek-v4.1-flash",
				MarketSlug: "surplus-trusted@openrouter:deepseek-v4.1-flash"},
			{ID: "ms1b", Position: 1, Provider: "custom-openai-surplus-gone", Model: "deepseek-v4.1-flash",
				MarketSlug: "surplus-trusted@gone:deepseek-v4.1-flash"},
			{ID: "ms1c", Position: 2, Provider: "deepseek", Model: "deepseek-chat",
				StaticInputPerM: 1, StaticOutputPerM: 4},
		},
	}
}

func TestPortalModelSettingsList(t *testing.T) {
	f := newModelSettingsFixture(t, twoProviderChain(), store.Chain{
		ID: "ms2", Name: "plain-model", Strategy: "priority",
		Steps: []store.ChainStep{{ID: "ms2a", Provider: "deepseek", Model: "deepseek-chat"}},
	})
	ctx := context.Background()
	require.NoError(t, f.srv.db.CustomProviders().CreateProvider(ctx, store.CustomProvider{
		ID: "custom-openai-surplus-openrouter", TenantID: adminTenant, DisplayName: "Surplus OpenRouter",
		Alias: "sor", Dialect: "openai", BaseURL: "https://example.test/v1",
	}))
	cache := market.NewSnapshotCache()
	cache.Replace([]market.Model{{
		Slug: "surplus-trusted@openrouter:deepseek-v4.1-flash", MinAskIn: 0.2, MinAskOut: 0.8,
		Providers: []string{"openrouter"},
	}})
	f.srv.marketCache = cache
	require.NoError(t, f.srv.db.APIKeys().SetMinDiscountPct(ctx, f.keyID, 30))

	def, max, rows := f.list(t)
	require.Equal(t, 30, def)
	require.Equal(t, maxDiscountFloorPct, max)
	require.Len(t, rows, 2)

	ds := rows[0]
	require.Equal(t, "deepseek-v4.1-flash", ds.Model)
	require.True(t, ds.Supported)
	require.Equal(t, 30, ds.EffectivePct)
	require.True(t, ds.HasFallback)
	require.False(t, ds.FinalFallback, "a new key has the final fallback off")
	require.False(t, ds.FinalFallbackKept)
	require.Equal(t, 0.3, ds.FallbackInputPerM)
	require.Equal(t, 1.2, ds.FallbackOutputPerM)
	require.Len(t, ds.Providers, 3)

	or := ds.Providers[0]
	require.Equal(t, "custom-openai-surplus-openrouter", or.ID)
	require.Equal(t, "Surplus OpenRouter", or.Name)
	require.True(t, or.Enabled)
	require.False(t, or.Locked)
	require.True(t, or.Available)
	require.InDelta(t, 0.22, *or.InputPerM, 1e-9, "live quote with the chain markup")
	require.InDelta(t, 0.88, *or.OutputPerM, 1e-9)

	gone := ds.Providers[1]
	require.False(t, gone.Available, "no live quote")
	require.Nil(t, gone.InputPerM)
	require.True(t, gone.Enabled)

	static := ds.Providers[2]
	require.Equal(t, "DeepSeek", static.Name, "built-in providers use the catalog name")
	require.True(t, static.Available)
	require.InDelta(t, 1.1, *static.InputPerM, 1e-9)
	require.InDelta(t, 4.4, *static.OutputPerM, 1e-9)

	plain := rows[1]
	require.Equal(t, "plain-model", plain.Model)
	require.False(t, plain.Supported)
	require.False(t, plain.HasFallback)
	require.Len(t, plain.Providers, 1)
	require.True(t, plain.Providers[0].Available)
}

func TestPortalModelSettingsFinalFallbackKept(t *testing.T) {
	f := newModelSettingsFixture(t, twoProviderChain())
	repo := f.srv.db.APIKeys()
	ctx := context.Background()
	_, err := f.srv.db.SQL().ExecContext(ctx, `INSERT INTO api_key_model_options (api_key_id, model, final_fallback, updated_at) VALUES (?, ?, 2, ?)`,
		f.keyID, "deepseek-v4.1-flash", time.Now().UTC().Format(time.RFC3339Nano))
	require.NoError(t, err)

	_, _, rows := f.list(t)
	require.True(t, rows[0].FinalFallback)
	require.True(t, rows[0].FinalFallbackKept)

	// Choosing it explicitly replaces "kept" with a plain on.
	rec := f.do(http.MethodPut, "/portal/api/model-settings/deepseek-v4.1-flash/final-fallback", `{"enabled":true}`, true)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var row settingsRowJSON
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &row))
	require.True(t, row.FinalFallback)
	require.False(t, row.FinalFallbackKept)
	got, err := repo.KeyRouting(ctx, f.keyID)
	require.NoError(t, err)
	require.Equal(t, store.FinalFallbackChosen, got.FinalFallback["deepseek-v4.1-flash"])
}

func TestPortalModelSettingsToggleProvider(t *testing.T) {
	f := newModelSettingsFixture(t, twoProviderChain())
	const provider = "custom-openai-surplus-openrouter"
	path := "/portal/api/model-settings/deepseek-v4.1-flash/providers/" + provider

	rec := f.do(http.MethodPut, path, `{"enabled":false}`, true)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var row settingsRowJSON
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &row))
	require.Equal(t, "deepseek-v4.1-flash", row.Model)
	require.False(t, row.Providers[0].Enabled)

	_, _, rows := f.list(t)
	require.False(t, rows[0].Providers[0].Enabled)
	require.True(t, rows[0].Providers[1].Enabled)

	rec2, err := f.srv.identity.Authenticate(context.Background(), f.plain)
	require.NoError(t, err)
	require.False(t, rec2.ProviderEnabled("deepseek-v4.1-flash", provider), "the auth cache must not hold the old setting")

	rec = f.do(http.MethodPut, path, `{"enabled":true}`, true)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rec2, err = f.srv.identity.Authenticate(context.Background(), f.plain)
	require.NoError(t, err)
	require.True(t, rec2.ProviderEnabled("deepseek-v4.1-flash", provider))
}

func TestPortalModelSettingsToggleErrors(t *testing.T) {
	f := newModelSettingsFixture(t, twoProviderChain())
	base := "/portal/api/model-settings/"

	rec := f.do(http.MethodPut, base+"no-such-model/final-fallback", `{"enabled":true}`, true)
	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Contains(t, rec.Body.String(), "model not found for this key")

	rec = f.do(http.MethodPut, base+"deepseek-v4.1-flash/providers/not-a-step-provider", `{"enabled":false}`, true)
	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Contains(t, rec.Body.String(), "provider not in this model's chain")

	rec = f.do(http.MethodPut, base+"deepseek-v4.1-flash/final-fallback", `{}`, true)
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestPortalModelSettingsLockedProvider(t *testing.T) {
	f := newModelSettingsFixture(t, twoProviderChain())
	ctx := context.Background()
	const pA, pB = "custom-openai-surplus-openrouter", "deepseek"
	require.NoError(t, f.srv.db.APIKeys().SetAllowedProviders(ctx, f.keyID, []string{pA}))
	// pB is outside the ceiling; the customer cannot turn it on.
	rec := f.do(http.MethodPut, "/portal/api/model-settings/deepseek-v4.1-flash/providers/"+pB, `{"enabled":true}`, true)
	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "this provider is locked by your administrator")

	got, err := f.srv.db.APIKeys().KeyRouting(ctx, f.keyID)
	require.NoError(t, err)
	require.Empty(t, got.DisabledProviders, "a refused write leaves the repo untouched")
	require.Equal(t, []string{pA}, got.AllowedProviders)

	_, _, rows := f.list(t)
	for _, p := range rows[0].Providers {
		switch p.ID {
		case pA:
			require.False(t, p.Locked)
			require.True(t, p.Enabled)
		default:
			require.True(t, p.Locked, p.ID)
			require.False(t, p.Enabled, p.ID)
		}
	}
}

func TestPortalModelSettingsModelNameEscaping(t *testing.T) {
	f := newModelSettingsFixture(t, store.Chain{
		ID: "ms3", Name: "Ali/GLM-5", Strategy: "priority", FallbackProvider: "openai", FallbackModel: "gpt-x",
		Steps: []store.ChainStep{{ID: "ms3a", Provider: "deepseek", Model: "deepseek-chat"}},
	})
	rec := f.do(http.MethodPut, "/portal/api/model-settings/Ali%2FGLM-5/final-fallback", `{"enabled":true}`, true)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var row settingsRowJSON
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &row))
	require.Equal(t, "Ali/GLM-5", row.Model)
	require.True(t, row.FinalFallback)

	got, err := f.srv.db.APIKeys().KeyRouting(context.Background(), f.keyID)
	require.NoError(t, err)
	require.Equal(t, store.FinalFallbackChosen, got.FinalFallback["ali/glm-5"])

	rec = f.do(http.MethodPut, "/portal/api/model-settings/Ali%2FGLM-5/providers/deepseek", `{"enabled":false}`, true)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	got, err = f.srv.db.APIKeys().KeyRouting(context.Background(), f.keyID)
	require.NoError(t, err)
	require.Equal(t, []string{"deepseek"}, got.DisabledProviders["ali/glm-5"])
}

func TestPortalModelSettingsRequiresSession(t *testing.T) {
	f := newModelSettingsFixture(t, twoProviderChain())
	for _, c := range []struct{ method, path, body string }{
		{http.MethodGet, "/portal/api/model-settings", ""},
		{http.MethodPut, "/portal/api/model-settings/deepseek-v4.1-flash/final-fallback", `{"enabled":true}`},
		{http.MethodPut, "/portal/api/model-settings/deepseek-v4.1-flash/providers/deepseek", `{"enabled":false}`},
	} {
		rec := f.do(c.method, c.path, c.body, false)
		require.Equal(t, http.StatusUnauthorized, rec.Code, c.method+" "+c.path)
	}
	got, err := f.srv.db.APIKeys().KeyRouting(context.Background(), f.keyID)
	require.NoError(t, err)
	require.Empty(t, got.FinalFallback)
}
