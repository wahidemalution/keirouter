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
	slug3, slug4 := market.SurplusTrustedPrefix+"m3", market.SurplusTrustedPrefix+"m4"
	for _, c := range []store.Chain{
		{ID: "A", Name: "A", Steps: []store.ChainStep{{ID: "A-s", ChainID: "A", Provider: "p30", Model: "m1", MarketSlug: slug1}}},
		{ID: "B", Name: "B", Steps: []store.ChainStep{{ID: "B-s", ChainID: "B", Provider: "p10", Model: "m1", MarketSlug: slug1}}},
		// m3 meets the lower floor first, m4 last, so only min() passes both.
		{ID: "D", Name: "D", Steps: []store.ChainStep{
			{ID: "D-1", ChainID: "D", Position: 0, Provider: "p10", Model: "m3", MarketSlug: slug3},
			{ID: "D-2", ChainID: "D", Position: 1, Provider: "p30", Model: "m3", MarketSlug: slug3},
			{ID: "D-3", ChainID: "D", Position: 2, Provider: "p30", Model: "m4", MarketSlug: slug4},
			{ID: "D-4", ChainID: "D", Position: 3, Provider: "p10", Model: "m4", MarketSlug: slug4},
		}},
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
	// Chains list newest first and may tie on created_at, so compare by name.
	want := map[string]int{"m1": 10, "m2": 0, "m3": 10, "m4": 10}
	floors := map[string]int{}
	for _, b := range got {
		floors[b.name] = b.floorPct
	}
	if len(got) != len(want) || !reflect.DeepEqual(floors, want) {
		t.Fatalf("bindings = %+v, want %+v", got, want)
	}
}
