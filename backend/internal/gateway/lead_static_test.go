package gateway

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/market"
	"github.com/mydisha/keirouter/backend/internal/store"
)

// staticSurplusStaticChain is a chain whose first step has a fixed price,
// with a surplus-trusted backup and a second fixed-price backup behind it.
func staticSurplusStaticChain(id, strategy string) store.Chain {
	in, out := 1.0, 10.0
	now := time.Now().UTC()
	return store.Chain{
		ID: id, TenantID: store.DefaultTenantID, Name: id, Strategy: strategy,
		OfficialInputPerM: &in, OfficialOutputPerM: &out,
		Steps: []store.ChainStep{
			{ID: id + "-s1", Provider: "custom-openai-a", Model: "m", Position: 0, StaticInputPerM: 1, StaticOutputPerM: 4},
			{ID: id + "-s2", Provider: "custom-openai-b", Model: "m", Position: 1, MarketSlug: "surplus-trusted:" + id},
			{ID: id + "-s3", Provider: "custom-openai-c", Model: "m", Position: 2, StaticInputPerM: 2, StaticOutputPerM: 8},
		},
		CreatedAt: now, UpdatedAt: now,
	}
}

func publicRowsByName(t *testing.T, gw *Server) map[string]publicModelRow {
	t.Helper()
	rows, err := gw.publicModelRows(context.Background())
	require.NoError(t, err)
	out := map[string]publicModelRow{}
	for _, r := range rows {
		out[r.Name] = r
	}
	return out
}

// A fixed-price first step sets the listed price, with the markup, and the
// exact discount from it, even when a surplus backup has a cheaper quote.
func TestPublicModelsLeadStaticStepSetsPrice(t *testing.T) {
	db, gw := newPublicTestGatewayWithDB(t)
	require.NoError(t, db.Chains().Create(context.Background(), staticSurplusStaticChain("lead", "priority")))
	cache := market.NewSnapshotCache()
	cache.Replace([]market.Model{
		market.TrustedQuote{Found: true, BestIn: 0.01, BestOut: 0.1, CapIn: 0.4, CapOut: 4, MinDiscountPct: 60, Providers: []string{"a"}}.Model("lead"),
	})
	gw.marketCache = cache

	row := publicRowsByName(t, gw)["lead"]
	require.False(t, row.SoldOut)
	require.Equal(t, "fixed", row.PriceBasis)
	require.InDelta(t, 1.1, row.InputPerM, 1e-9, "static input with the 10% default markup")
	require.InDelta(t, 4.4, row.OutputPerM, 1e-9)
	require.InDelta(t, 1.1*market.CacheReadMult, row.CachedPerM, 1e-9)
	require.Equal(t, 50, row.DiscountPct, "5.5 of an 11 list")
	require.Zero(t, row.UpToDiscountPct, "no up-to badge for a fixed price")
	require.Zero(t, row.MaxInputPerM)
}

// The surplus backup having no live quote does not make the chain sold out:
// the fixed-price first step still serves it.
func TestPublicModelsLeadStaticStepNotSoldOutWithoutMarket(t *testing.T) {
	db, gw := newPublicTestGatewayWithDB(t)
	require.NoError(t, db.Chains().Create(context.Background(), staticSurplusStaticChain("lead", "priority")))
	gw.marketCache = market.NewSnapshotCache()

	row := publicRowsByName(t, gw)["lead"]
	require.False(t, row.SoldOut)
	require.Equal(t, "fixed", row.PriceBasis)
	require.InDelta(t, 1.1, row.InputPerM, 1e-9)
}

// The step's own markup wins over the chain and global markup.
func TestPublicModelsLeadStaticStepUsesStepMarkup(t *testing.T) {
	db, gw := newPublicTestGatewayWithDB(t)
	c := staticSurplusStaticChain("lead", "priority")
	stepMarkup := 25.0
	c.Steps[0].MarkupPercent = &stepMarkup
	require.NoError(t, db.Chains().Create(context.Background(), c))
	gw.marketCache = market.NewSnapshotCache()

	row := publicRowsByName(t, gw)["lead"]
	require.InDelta(t, 1.25, row.InputPerM, 1e-9)
	require.InDelta(t, 5.0, row.OutputPerM, 1e-9)
}

// Round-robin can serve the surplus step first, so the chain stays priced by
// the market.
func TestPublicModelsRoundRobinIgnoresLeadStaticStep(t *testing.T) {
	db, gw := newPublicTestGatewayWithDB(t)
	require.NoError(t, db.Chains().Create(context.Background(), staticSurplusStaticChain("rr", "round-robin")))
	cache := market.NewSnapshotCache()
	cache.Replace([]market.Model{
		market.TrustedQuote{Found: true, BestIn: 0.01, BestOut: 0.1, CapIn: 0.4, CapOut: 4, MinDiscountPct: 60, Providers: []string{"a"}}.Model("rr"),
	})
	gw.marketCache = cache

	require.Equal(t, "live", publicRowsByName(t, gw)["rr"].PriceBasis)
}
