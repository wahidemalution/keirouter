package app

import (
	"context"
	"reflect"
	"testing"

	"github.com/mydisha/keirouter/backend/internal/market"
	"github.com/mydisha/keirouter/backend/internal/store"
)

// Billing guard: pins the per-model /min{N} floor chosen by
// boundTrustedSurplusModels.
func TestBoundTrustedFloorsGolden(t *testing.T) {
	db := newSyncTestDB(t)
	ctx := context.Background()
	for id, base := range map[string]string{
		"p10":   "https://api.surplusintelligence.ai/min10/v1",
		"p30":   "https://api.surplusintelligence.ai/min30/v1",
		"pnone": "https://api.surplusintelligence.ai/v1",
	} {
		if err := db.CustomProviders().CreateProvider(ctx, store.CustomProvider{
			ID: id, TenantID: store.DefaultTenantID, DisplayName: id, Alias: id, Dialect: "openai", BaseURL: base,
		}); err != nil {
			t.Fatal(err)
		}
	}
	slug1, slug2 := market.SurplusTrustedPrefix+"m1", market.SurplusTrustedPrefix+"m2"
	for _, c := range []store.Chain{
		{ID: "A", Name: "A", Steps: []store.ChainStep{{ID: "A-s", ChainID: "A", Provider: "p30", Model: "m1", MarketSlug: slug1}}},
		{ID: "B", Name: "B", Steps: []store.ChainStep{{ID: "B-s", ChainID: "B", Provider: "p10", Model: "m1", MarketSlug: slug1}}},
		// m2 is chain-level only; its one step serves another model.
		{ID: "C", Name: "C", MarketSlugs: []string{slug2}, Steps: []store.ChainStep{{ID: "C-s", ChainID: "C", Provider: "pnone", Model: "other"}}},
	} {
		c.TenantID, c.Strategy = store.DefaultTenantID, "priority"
		if err := db.Chains().Create(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	a := newSyncTestApp(t, db)
	got := a.boundTrustedSurplusModels(ctx, market.SurplusTrustedPrefix)
	want := []trustedBinding{{name: "m1", floorPct: 10}, {name: "m2", floorPct: 0}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("bindings = %+v, want %+v", got, want)
	}
}
