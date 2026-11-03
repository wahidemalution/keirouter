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
	FallbackRuns       bool                   `json:"fallback_runs"`
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
	require.Equal(t, "openrouter", or.ID, "the portal sees the seller id, not the provider alias")
	require.Equal(t, "OpenRouter", or.Name, "the portal never names the marketplace")
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

// fallback_runs says whether routing really tries the fallback: a kept
// fallback (2) is dropped by a minimum discount, a chosen one (1) is not.
// Each case also resolves a request, so the row cannot drift from routing.
func TestPortalModelSettingsFallbackRuns(t *testing.T) {
	for _, tc := range []struct {
		name    string
		setting int
		minPct  int
		runs    bool
	}{
		{"kept with minimum discount", store.FinalFallbackKept, 30, false},
		{"chosen with minimum discount", store.FinalFallbackChosen, 30, true},
		{"kept without minimum discount", store.FinalFallbackKept, 0, true},
		{"off", store.FinalFallbackOff, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newModelSettingsFixture(t, twoProviderChain())
			ctx := context.Background()
			if tc.setting != store.FinalFallbackOff {
				_, err := f.srv.db.SQL().ExecContext(ctx, `INSERT INTO api_key_model_options (api_key_id, model, final_fallback, updated_at) VALUES (?, ?, ?, ?)`,
					f.keyID, "deepseek-v4.1-flash", tc.setting, time.Now().UTC().Format(time.RFC3339Nano))
				require.NoError(t, err)
			}
			require.NoError(t, f.srv.db.APIKeys().SetMinDiscountPct(ctx, f.keyID, tc.minPct))

			_, _, rows := f.list(t)
			require.Len(t, rows, 1)
			require.Equal(t, tc.setting != store.FinalFallbackOff, rows[0].FinalFallback)
			require.Equal(t, tc.runs, rows[0].FallbackRuns)

			key := store.APIKey{ID: f.keyID, MinDiscountPct: tc.minPct,
				ModelFinalFallback: map[string]int{"deepseek-v4.1-flash": tc.setting}}
			res, err := f.srv.resolveTargets(context.WithValue(ctx, apiKeyCtxKey, key), store.DefaultTenantID, "deepseek-v4.1-flash")
			routed := err == nil && len(res.Targets) > 0 && res.Targets[len(res.Targets)-1].ChainFallback
			require.Equal(t, tc.runs, routed, "routing agrees with the row (err %v)", err)
		})
	}
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
		case portalProviderID(pA):
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

// A literal '%' in a model name is decoded exactly once.
func TestPortalModelSettingsPercentInModelName(t *testing.T) {
	f := newModelSettingsFixture(t,
		store.Chain{ID: "ms4", Name: "Ali/GLM%41", Strategy: "priority",
			Steps: []store.ChainStep{{ID: "ms4a", Provider: "deepseek", Model: "deepseek-chat"}}},
		store.Chain{ID: "ms5", Name: "pct-100%", Strategy: "priority",
			Steps: []store.ChainStep{{ID: "ms5a", Provider: "deepseek", Model: "deepseek-chat"}}},
	)
	rec := f.do(http.MethodPut, "/portal/api/model-settings/Ali%2FGLM%2541/final-fallback", `{"enabled":true}`, true)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rec = f.do(http.MethodPut, "/portal/api/model-settings/pct-100%25/final-fallback", `{"enabled":true}`, true)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	got, err := f.srv.db.APIKeys().KeyRouting(context.Background(), f.keyID)
	require.NoError(t, err)
	require.Equal(t, store.FinalFallbackChosen, got.FinalFallback["ali/glm%41"])
	require.Equal(t, store.FinalFallbackChosen, got.FinalFallback["pct-100%"])
}

// Building the page must not advance surplus-fast recovery trials: the next
// real routing call still gets the trial slot.
func TestBuildModelSettingsLeavesFastHealthUntouched(t *testing.T) {
	f := newModelSettingsFixture(t, store.Chain{
		ID: "ms6", Name: "fast-model", Strategy: "priority",
		Steps: []store.ChainStep{
			{ID: "ms6a", Position: 0, Provider: "custom-openai-surplus", Model: "fm", MarketSlug: "surplus-fast:fm"},
		},
	})
	cache := market.NewSnapshotCache()
	cache.Replace([]market.Model{{
		Slug: "surplus-fast:fm", MinAskIn: 0.2, MinAskOut: 0.8,
		Providers: []string{"slowco", "fastco"}, ProviderOrder: []string{"fastco", "slowco"},
	}})
	f.srv.marketCache = cache
	now := time.Now()
	health := cache.FastHealth()
	health.SetClock(func() time.Time { return now })

	ctx := context.Background()
	chains, err := f.srv.chains.ListByTenant(ctx, store.DefaultTenantID)
	require.NoError(t, err)
	key, err := f.srv.identity.Get(ctx, f.keyID)
	require.NoError(t, err)

	// slowco answers slowly, then its cooldown ends: it is now due a trial.
	health.Observe("fm", "slowco", 20*time.Second, true)
	now = now.Add(market.FastCooldownMax + time.Second)

	for i := 0; i < 5; i++ {
		resp, err := f.srv.buildModelSettings(ctx, key, chains)
		require.NoError(t, err)
		require.True(t, resp.Models[0].Providers[0].Available)
	}
	// Five page builds did not count as requests: this first routing call
	// still gets the trial slot.
	require.Equal(t, []string{"slowco", "fastco"}, health.Filter("fm", []string{"slowco", "fastco"}))
}

func TestBuildModelSettingsUsesSurplusProviderLabels(t *testing.T) {
	f := newModelSettingsFixture(t, store.Chain{
		ID: "ms7", Name: "labelled", Strategy: "priority",
		Steps: []store.ChainStep{
			{ID: "ms7a", Position: 0, Provider: "custom-openai-surplus-dashscope-cn", Model: "lm", MarketSlug: "surplus-trusted@dashscope-cn:lm"},
			{ID: "ms7b", Position: 1, Provider: "custom-openai-surplus", Model: "lm", MarketSlug: "surplus-trusted:lm"},
		},
	})
	cache := market.NewSnapshotCache()
	// No quote for the dashscope slug (no offer right now): the label must
	// still come through.
	cache.MergeProviderLabels(map[string]string{"dashscope-cn": "Alibaba DashScope - China"})
	f.srv.marketCache = cache

	ctx := context.Background()
	chains, err := f.srv.chains.ListByTenant(ctx, store.DefaultTenantID)
	require.NoError(t, err)
	key, err := f.srv.identity.Get(ctx, f.keyID)
	require.NoError(t, err)
	resp, err := f.srv.buildModelSettings(ctx, key, chains)
	require.NoError(t, err)
	providers := resp.Models[0].Providers
	require.Equal(t, "Alibaba DashScope - China", providers[0].Name)
	require.False(t, providers[0].Available)
	require.NotEqual(t, "", providers[1].Name, "a plain step keeps its keirouter name")
	require.NotEqual(t, "Alibaba DashScope - China", providers[1].Name)
}

func TestBuildModelSettingsBestPriceOffersDiscountLatency(t *testing.T) {
	official := 1.0
	officialOut := 4.0
	markup := 10.0
	f := newModelSettingsFixture(t, store.Chain{
		ID: "ms8", Name: "offers-model", Strategy: "priority", MarkupPercent: &markup,
		OfficialInputPerM: &official, OfficialOutputPerM: &officialOut,
		Steps: []store.ChainStep{
			{ID: "ms8a", Position: 0, Provider: "custom-openai-surplus-openrouter", Model: "om", MarketSlug: "surplus-trusted@openrouter:om"},
		},
	})
	perf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/analytics/performance/om" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"computed_at":1,"providers":[{"provider":"OpenRouter","requests":5,"p50_latency_ms":2300}]}`))
	}))
	defer perf.Close()
	f.srv.surplusURL = perf.URL + "/api/markets"

	q := market.TrustedQuote{Found: true, BestIn: 0.2, BestOut: 0.8, CapIn: 0.5, CapOut: 2, Providers: []string{"openrouter"},
		Offers: []market.OfferPrice{{In: 0.2, Out: 0.8}, {In: 0.5, Out: 2}}}
	cache := market.NewSnapshotCache()
	cache.Replace([]market.Model{q.ProviderModel("openrouter", "om")})
	f.srv.marketCache = cache

	ctx := context.Background()
	chains, err := f.srv.chains.ListByTenant(ctx, store.DefaultTenantID)
	require.NoError(t, err)
	key, err := f.srv.identity.Get(ctx, f.keyID)
	require.NoError(t, err)
	resp, err := f.srv.buildModelSettings(ctx, key, chains)
	require.NoError(t, err)
	p := resp.Models[0].Providers[0]
	require.True(t, p.Available)
	require.InDelta(t, 0.22, *p.InputPerM, 1e-9, "best price is the cheapest offer with markup")
	require.InDelta(t, 0.88, *p.OutputPerM, 1e-9)
	require.Equal(t, 78, *p.DiscountPct, "1.10 against 5.00 official")
	require.Len(t, p.Offers, 2)
	require.InDelta(t, 0.55, p.Offers[1].InputPerM, 1e-9)
	require.Equal(t, 45, *p.Offers[1].DiscountPct, "2.75 against 5.00 official")
	require.NotNil(t, p.LatencyP50MS)
	require.Equal(t, 2300.0, *p.LatencyP50MS)
}
