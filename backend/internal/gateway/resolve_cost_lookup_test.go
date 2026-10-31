package gateway

import (
	"testing"

	"github.com/mydisha/keirouter/backend/internal/connectors"
	"github.com/mydisha/keirouter/backend/internal/core"
	"github.com/mydisha/keirouter/backend/internal/store"
)

func registerCostLookupProvider(t *testing.T, id string, on bool) {
	t.Helper()
	connectors.RegisterDynamicProvider(connectors.DynamicProvider{ID: id, DisplayName: id, Alias: id,
		Dialect: core.DialectOpenAI, BaseURL: "https://example.test/v1", CostLookup: on})
	t.Cleanup(func() { connectors.UnregisterDynamicProvider(id) })
}

func subrouterStep(provider string, cacheRead *float64) store.ChainStep {
	return store.ChainStep{Provider: provider, Model: "deepseek-v4.1-flash",
		StaticInputPerM: 0.02, StaticOutputPerM: 0.08, StaticCacheReadPerM: cacheRead, MarkupPercent: fptr(50)}
}

func TestStaticStepCacheReadFullInputForCostLookup(t *testing.T) {
	registerCostLookupProvider(t, "custom-openai-subtest", true)
	registerCostLookupProvider(t, "custom-openai-plain", false)

	got := resolveStatic(t, store.Chain{Steps: []store.ChainStep{subrouterStep("custom-openai-subtest", nil)}}, nil)
	near(t, 0.02, got[0].UpstreamCacheReadRate, "cost-lookup cache read cost = full input")
	near(t, 0.03, got[0].MarketCacheReadRate, "cost-lookup cache read billed = full input × 1.5")

	got = resolveStatic(t, store.Chain{Steps: []store.ChainStep{subrouterStep("custom-openai-subtest", fptr(0.005))}}, nil)
	near(t, 0.005, got[0].UpstreamCacheReadRate, "explicit override wins")

	got = resolveStatic(t, store.Chain{Steps: []store.ChainStep{subrouterStep("custom-openai-plain", nil)}}, nil)
	near(t, 0.002, got[0].UpstreamCacheReadRate, "other providers keep the derived ratio")
}

func TestResolveStampsCostLookup(t *testing.T) {
	registerCostLookupProvider(t, "custom-openai-subtest", true)
	registerCostLookupProvider(t, "custom-openai-plain", false)
	got := resolveStatic(t, store.Chain{Steps: []store.ChainStep{
		subrouterStep("custom-openai-subtest", nil), subrouterStep("custom-openai-plain", nil),
	}}, nil)
	if !got[0].CostLookup || got[1].CostLookup {
		t.Fatalf("CostLookup = %v, %v; want true, false", got[0].CostLookup, got[1].CostLookup)
	}
}
