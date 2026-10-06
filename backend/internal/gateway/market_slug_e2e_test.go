package gateway

import (
	"context"
	"testing"

	"github.com/mydisha/keirouter/backend/internal/core"
	"github.com/mydisha/keirouter/backend/internal/dispatch"
	"github.com/mydisha/keirouter/backend/internal/market"
	"github.com/mydisha/keirouter/backend/internal/meter"
	"github.com/mydisha/keirouter/backend/internal/store"
)

// slugCaptureStore records the last usage row the meter persisted.
type slugCaptureStore struct{ last store.UsageRecord }

func (c *slugCaptureStore) Record(_ context.Context, u store.UsageRecord) error {
	c.last = u
	return nil
}
func (c *slugCaptureStore) RecordBatch(_ context.Context, rs []store.UsageRecord) error {
	if len(rs) > 0 {
		c.last = rs[len(rs)-1]
	}
	return nil
}

// TestMarketSlugChainResolvesCheapestThenMeterBillsIt exercises the full
// resolve -> meter seam for ReorderByMarket chains: the resolved targets must
// come back cheapest-first and carry the winning slug's post-markup rates, and
// the meter must bill that winning slug's rate.
//
// The fixture declares three slugged targets OUT OF ORDER (dear, middle, cheap)
// with a slugless step interleaved, so the resolve layer must run a sort that
// needs >=2 moves. This guards the resolve -> meter seam, not the pure sort:
// the ordering regression guard for the Task 5 sort-desync bug is owned by
// TestOrderStepsByMarketCheapestFirstAndStampsRates in resolve_market_test.go.
func TestMarketSlugChainResolvesCheapestThenMeterBillsIt(t *testing.T) {
	cache := market.NewSnapshotCache()
	cache.Replace([]market.Model{
		{Slug: "cheap/slug", MinAskIn: 0.10, MinAskOut: 0.20}, // sum 0.30 cheapest
		{Slug: "mid/slug", MinAskIn: 1.00, MinAskOut: 2.00},   // sum 3.00
		{Slug: "dear/slug", MinAskIn: 5.00, MinAskOut: 5.00},  // sum 10.00 dearest
	})

	// Declared order is deliberately non-ascending: dear(10.00), mid(3.00),
	// slugless, cheap(0.30). Requires >=2 moves and must sink the slugless step
	// behind every priced step.
	chains := &fakeChains{chains: []store.Chain{{
		ID:              "c-mkt",
		Name:            "market",
		ReorderByMarket: true,
		Steps: []store.ChainStep{
			{Position: 0, Provider: "openai", Model: "gpt-4o", MarketSlug: "dear/slug"},
			{Position: 1, Provider: "openai", Model: "gpt-4o", MarketSlug: "mid/slug"},
			{Position: 2, Provider: "openai", Model: "gpt-4o"}, // no slug, interleaved
			{Position: 3, Provider: "openai", Model: "gpt-4o", MarketSlug: "cheap/slug"},
		},
	}}}

	res, err := resolveTargetsWith(context.Background(), chains, &fakeAliases{}, nil, cache, 10 /*markupPercent*/, "t1", "chain:market")
	if err != nil {
		t.Fatalf("resolveTargetsWith: %v", err)
	}
	if len(res.Targets) != 4 {
		t.Fatalf("got %d targets, want 4", len(res.Targets))
	}
	wantSlugs := []string{"cheap/slug", "mid/slug", "dear/slug", ""}
	for i, want := range wantSlugs {
		if got := res.Targets[i].MarketSlug; got != want {
			t.Fatalf("targets[%d].MarketSlug = %q, want %q (cheapest-first, slugless last); slugs=%v",
				i, got, want, targetSlugs(res.Targets))
		}
	}
	win := res.Targets[0]
	if win.MarketSlug != "cheap/slug" {
		t.Fatalf("first target slug = %q, want cheap/slug (cheapest first)", win.MarketSlug)
	}
	// Markup 10%: 0.10*1.1 = 0.11, 0.20*1.1 = 0.22.
	if win.MarketRateIn < 0.1099 || win.MarketRateIn > 0.1101 {
		t.Fatalf("winning MarketRateIn = %v, want ~0.11", win.MarketRateIn)
	}
	if win.MarketRateOut < 0.2199 || win.MarketRateOut > 0.2201 {
		t.Fatalf("winning MarketRateOut = %v, want ~0.22", win.MarketRateOut)
	}

	// Feed the winning target through the same mapping the pipeline uses and
	// confirm the meter bills the slug's post-markup rate.
	cap := &slugCaptureStore{}
	mtr := meter.New(cap, nil, nil)
	_, err = mtr.Record(context.Background(), meter.Event{
		TenantID: store.DefaultTenantID, Provider: win.Provider, Model: win.Model,
		MarketSlug: win.MarketSlug, MarketRateIn: win.MarketRateIn, MarketRateOut: win.MarketRateOut,
		Status: "success",
		Usage:  core.Usage{PromptTokens: 1_000_000, CompletionTokens: 1_000_000, Source: core.UsageSourceProvider},
	})
	if err != nil {
		t.Fatalf("meter.Record: %v", err)
	}
	if cap.last.PricingSource != "market_slug" {
		t.Fatalf("PricingSource = %q, want market_slug", cap.last.PricingSource)
	}
	if cap.last.PricingKey != "cheap/slug" {
		t.Fatalf("PricingKey = %q, want cheap/slug", cap.last.PricingKey)
	}
	if cap.last.PricingMatchKind != "market_slug" {
		t.Fatalf("PricingMatchKind = %q, want market_slug", cap.last.PricingMatchKind)
	}
	// 0.11 + 0.22 = 0.33 USD = 330,000,000 nanos.
	if cap.last.CostNanos != 330_000_000 {
		t.Fatalf("CostNanos = %d, want 330000000", cap.last.CostNanos)
	}
}

func targetSlugs(ts []dispatch.Target) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.MarketSlug
	}
	return out
}
