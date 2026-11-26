package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/config"
	"github.com/mydisha/keirouter/backend/internal/identity"
	"github.com/mydisha/keirouter/backend/internal/market"
	"github.com/mydisha/keirouter/backend/internal/store"
)

// publicPriceFixture holds one chain per route kind the public price list
// must describe: chain price, Surplus trusted, cost lookup, a mix, a route
// with no price, a chain fallback and named providers.
func publicPriceFixture(t *testing.T) *Server {
	t.Helper()
	registerCostLookupProvider(t, "custom-openai-subtest", true)
	registerCostLookupProvider(t, "custom-openai-plainx", false)

	db, err := store.Open(context.Background(), config.DatabaseConfig{Driver: "sqlite", DSN: ":memory:"}, t.TempDir())
	require.NoError(t, err)
	require.NoError(t, db.Migrate(context.Background()))
	require.NoError(t, db.Tenants().EnsureDefault(context.Background()))
	t.Cleanup(func() { _ = db.Close() })
	gw := New(Deps{Config: config.Default(), DB: db, Usage: db.Usage(), Settings: db.Settings(), Chains: db.Chains(), Identity: identity.New(db.APIKeys())})

	ctx := context.Background()
	now := time.Now().UTC()
	m25, m100, m0 := 25.0, 100.0, 0.0
	offIn, offOut := 1.0, 4.0
	trusted := store.ChainStep{Provider: "custom-openai-surplus", Model: "glm-5.3", MarketSlug: "surplus-trusted:glm-5.3"}
	for _, c := range []store.Chain{
		{ID: "c-fixed", Name: "fixed", InputPerM: 2, OutputPerM: 8,
			Steps: []store.ChainStep{{Provider: "openai", Model: "gpt-4o"}}},
		{ID: "c-glm", Name: "glm-trusted", MarkupPercent: &m25, OfficialInputPerM: &offIn, OfficialOutputPerM: &offOut,
			MarketSlugs: []string{"surplus-trusted:glm-5.3"}, Steps: []store.ChainStep{trusted}},
		{ID: "c-sub", Name: "sub", MarkupPercent: &m100,
			Steps: []store.ChainStep{{Provider: "custom-openai-subtest", Model: "ds", StaticInputPerM: 1, StaticOutputPerM: 4}}},
		{ID: "c-mixed", Name: "mixed", MarkupPercent: &m0, MarketSlugs: []string{"surplus-trusted:glm-5.3"},
			Steps: []store.ChainStep{{Provider: "custom-openai-plainx", Model: "glm", StaticInputPerM: 3, StaticOutputPerM: 3}, trusted}},
		{ID: "c-bare", Name: "bare", Steps: []store.ChainStep{{Provider: "openai", Model: "gpt-4o"}}},
		{ID: "c-fb", Name: "fbk", InputPerM: 2, OutputPerM: 8,
			FallbackProvider: "openai", FallbackModel: "gpt-4o-mini", FallbackInputPerM: 10, FallbackOutputPerM: 30,
			Steps: []store.ChainStep{{Provider: "openai", Model: "gpt-4o"}}},
		{ID: "c-clchain", Name: "clchain", InputPerM: 1, OutputPerM: 1,
			Steps: []store.ChainStep{{Provider: "custom-openai-subtest", Model: "ds"}}},
		{ID: "c-plain", Name: "plain", MarkupPercent: &m0,
			Steps: []store.ChainStep{{Provider: "custom-openai-plainx", Model: "r1", MarketSlug: "surplus:r1"}}},
		{ID: "c-pinned", Name: "pinned", MarkupPercent: &m0,
			Steps: []store.ChainStep{{Provider: "custom-openai-surplus", Model: "glm-5.3", MarketSlug: "surplus-trusted@zai:glm-5.3"}}},
		{ID: "c-named", Name: "named", InputPerM: 1, OutputPerM: 1,
			Steps: []store.ChainStep{{Provider: "openai", Model: "a", DisplayName: "Anthropic"}, {Provider: "deepseek", Model: "b"}}},
	} {
		c.TenantID, c.Strategy, c.CreatedAt, c.UpdatedAt = store.DefaultTenantID, "priority", now, now
		for j := range c.Steps {
			c.Steps[j].ID, c.Steps[j].Position = fmt.Sprintf("%s-s%d", c.ID, j), j
		}
		require.NoError(t, db.Chains().Create(ctx, c))
	}
	cache := market.NewSnapshotCache()
	cache.Replace([]market.Model{
		market.TrustedQuote{Found: true, BestIn: 0.2, BestOut: 0.8, CapIn: 0.4, CapOut: 1.6, MinDiscountPct: 50, Providers: []string{"zai"},
			Offers: []market.OfferPrice{{In: 0.2, Out: 0.8}, {In: 0.3, Out: 1.2}, {In: 0.3, Out: 1.2}}}.Model("glm-5.3"),
		market.TrustedQuote{Found: true, BestIn: 0.2, BestOut: 0.8, CapIn: 0.4, CapOut: 1.6, Providers: []string{"zai"}}.ProviderModel("zai", "glm-5.3"),
		{Slug: "surplus:r1", MinAskIn: 1, MinAskOut: 2, Reasoning: 5},
	})
	gw.marketCache = cache
	return gw
}

// publicPriceOf prices one fixture chain and returns it as the JSON a client
// reads.
func publicPriceOf(t *testing.T, gw *Server, name string) map[string]any {
	t.Helper()
	chains, err := gw.chains.ListByTenant(context.Background(), adminTenant)
	require.NoError(t, err)
	for _, c := range chains {
		if c.Name != name {
			continue
		}
		markup := market.EffectiveMarkup(c.MarkupPercent, market.DefaultSettings().MarkupPercent)
		raw, err := json.Marshal(gw.publicChainPrice(c, markup, nil))
		require.NoError(t, err)
		var out map[string]any
		require.NoError(t, json.Unmarshal(raw, &out))
		return out
	}
	t.Fatalf("no chain %q", name)
	return nil
}

func pubProviders(t *testing.T, m map[string]any) []map[string]any {
	t.Helper()
	raw, ok := m["providers"].([]any)
	require.True(t, ok, m)
	out := make([]map[string]any, len(raw))
	for i, p := range raw {
		out[i] = p.(map[string]any)
	}
	return out
}

func TestPublicChainPrice_ChainPriceIsFixed(t *testing.T) {
	m := publicPriceOf(t, publicPriceFixture(t), "fixed")
	require.Equal(t, true, m["available"])
	require.Equal(t, "fixed", m["price_type"])
	require.Equal(t, 2.0, m["input_per_m"])
	require.Equal(t, 8.0, m["output_per_m"])
	require.Equal(t, 2.0, m["max_input_per_m"])
	require.Equal(t, 8.0, m["max_output_per_m"])
	require.Equal(t, true, m["max_guaranteed"])
	require.Nil(t, m["min_request_cost"])
	ps := pubProviders(t, m)
	require.Len(t, ps, 1)
	require.Len(t, ps[0]["prices"], 1)
}

func TestPublicChainPrice_SurplusTrusted(t *testing.T) {
	m := publicPriceOf(t, publicPriceFixture(t), "glm-trusted")
	require.Equal(t, "variable", m["price_type"])
	require.Equal(t, 0.25, m["input_per_m"])
	require.Equal(t, 1.0, m["output_per_m"])
	require.Equal(t, 0.5, m["max_input_per_m"], "the failover ceiling × markup")
	require.Equal(t, 2.0, m["max_output_per_m"])
	require.Equal(t, false, m["max_guaranteed"], "billed from Surplus' reported cost")
	require.Equal(t, 75.0, m["discount_pct"], "(1 - 1.25/5) × 100")
}

func TestPublicChainPrice_SurplusLevels(t *testing.T) {
	ps := pubProviders(t, publicPriceOf(t, publicPriceFixture(t), "glm-trusted"))
	require.Len(t, ps, 1)
	levels := ps[0]["prices"].([]any)
	require.Len(t, levels, 2, "identical offers merge into one level")
	first, last := levels[0].(map[string]any), levels[1].(map[string]any)
	require.Equal(t, 0.25, first["input_per_m"])
	require.Equal(t, 1.0, first["offers"])
	require.Equal(t, "cheapest", first["tag"])
	require.Equal(t, 0.375, last["input_per_m"])
	require.Equal(t, 1.5, last["output_per_m"])
	require.Equal(t, 2.0, last["offers"])
	require.Equal(t, "highest", last["tag"])
}

func TestPublicChainPrice_CostLookupIsVariable(t *testing.T) {
	m := publicPriceOf(t, publicPriceFixture(t), "sub")
	require.Equal(t, "variable", m["price_type"], "the sweeper re-bills from the upstream log")
	require.Equal(t, 2.0, m["max_input_per_m"])
	require.Equal(t, 8.0, m["max_output_per_m"])
	require.Equal(t, false, m["max_guaranteed"])
	require.Equal(t, 0.000004, m["min_request_cost"], "2 micro minimum × 2 markup")
}

func TestPublicChainPrice_MixedTakesPerFieldMax(t *testing.T) {
	m := publicPriceOf(t, publicPriceFixture(t), "mixed")
	require.Equal(t, 3.0, m["input_per_m"], "headline is the first declared route")
	require.Equal(t, 3.0, m["max_input_per_m"])
	require.Equal(t, 3.0, m["max_output_per_m"])
	require.Equal(t, false, m["max_guaranteed"])
	require.Len(t, pubProviders(t, m), 2)
}

func TestPublicChainPrice_UnpricedRouteHasNoMax(t *testing.T) {
	m := publicPriceOf(t, publicPriceFixture(t), "bare")
	require.Nil(t, m["input_per_m"])
	require.Nil(t, m["max_input_per_m"])
	require.Nil(t, m["max_output_per_m"])
	require.Equal(t, false, m["max_guaranteed"])
	raw, _ := json.Marshal(m)
	require.NotContains(t, string(raw), `_per_m":0`)
}

func TestPublicChainPrice_FallbackCountsTowardMax(t *testing.T) {
	m := publicPriceOf(t, publicPriceFixture(t), "fbk")
	require.Equal(t, 2.0, m["input_per_m"], "the fallback never sets the headline")
	require.Equal(t, 10.0, m["max_input_per_m"])
	require.Equal(t, 30.0, m["max_output_per_m"])
	ps := pubProviders(t, m)
	require.Len(t, ps, 2)
	require.Equal(t, true, ps[1]["final_fallback"])
	_, has := ps[0]["final_fallback"]
	require.False(t, has)
}

func TestPublicChainPrice_ProviderNames(t *testing.T) {
	gw := publicPriceFixture(t)
	ps := pubProviders(t, publicPriceOf(t, gw, "named"))
	require.Equal(t, "Anthropic", ps[0]["name"])
	require.Equal(t, "Provider 2", ps[1]["name"])
	for _, name := range []string{"sub", "mixed", "glm-trusted"} {
		raw, _ := json.Marshal(publicPriceOf(t, gw, name))
		require.False(t, strings.Contains(string(raw), "custom-openai"), name+": "+string(raw))
		require.NotContains(t, string(raw), "surplus-trusted")
	}
}

// A cost-lookup step billed at the chain price is still re-billed from the
// upstream log by the sweeper, so its max is no promise.
func TestPublicChainPrice_CostLookupOnChainPrice(t *testing.T) {
	m := publicPriceOf(t, publicPriceFixture(t), "clchain")
	require.Equal(t, false, m["max_guaranteed"])
	require.Equal(t, "variable", m["price_type"])
}

// Reasoning tokens bill at their own rate, which can exceed the output rate;
// the guaranteed output max must cover it.
func TestPublicChainPrice_MaxCoversReasoningRate(t *testing.T) {
	m := publicPriceOf(t, publicPriceFixture(t), "plain")
	require.Equal(t, 2.0, m["output_per_m"])
	require.Equal(t, 5.0, m["max_output_per_m"])
	require.Equal(t, true, m["max_guaranteed"], "a plain surplus stamp is billed as stamped")
	require.Equal(t, "variable", m["price_type"])
}

// The public build fetches Surplus latency only for published chains, and
// not at all while public pricing is off.
func TestAllChainPrices_LatencyOnlyForPublished(t *testing.T) {
	gw := publicPriceFixture(t)
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	gw.surplusURL = srv.URL

	_, err := gw.allChainPrices(context.Background())
	require.NoError(t, err)
	require.Zero(t, hits.Load(), "nothing is published")

	require.NoError(t, gw.settings.Set(context.Background(), publicPricingSettingsKey, `{"enabled":true,"chain_ids":["c-pinned"]}`))
	gw.publicPrices = publicPriceCache{}
	_, err = gw.allChainPrices(context.Background())
	require.NoError(t, err)
	require.Positive(t, hits.Load(), "the published pinned chain's latency is fetched")
}
