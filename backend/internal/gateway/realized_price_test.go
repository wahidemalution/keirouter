package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mydisha/keirouter/backend/internal/market"
	"github.com/mydisha/keirouter/backend/internal/store"
	"github.com/stretchr/testify/require"
)

func TestRealizedFromWeighsByTokens(t *testing.T) {
	official := &officialPrice{InputPerM: 1, OutputPerM: 10}
	// 1M input billed $0.50 (50% off) and 100k output billed $0.80 (20% off):
	// $1.30 paid of a $2.00 list, so 35% off.
	b := store.ChainBilledUsage{InputTokens: 1_000_000, OutputTokens: 100_000, InputNanos: 500_000_000, OutputNanos: 800_000_000}
	rp, ok := realizedFrom(b, official)
	require.True(t, ok)
	require.InDelta(t, 0.5, rp.InputPerM, 1e-9)
	require.InDelta(t, 8.0, rp.OutputPerM, 1e-9)
	require.Equal(t, 35, rp.DiscountPct)
	require.Equal(t, int64(1_100_000), rp.Tokens)
}

func TestRealizedFromNeedsEnoughTraffic(t *testing.T) {
	_, ok := realizedFrom(store.ChainBilledUsage{InputTokens: 500_000, OutputTokens: 400_000, InputNanos: 1, OutputNanos: 1}, nil)
	require.False(t, ok, "under 1M tokens")
	_, ok = realizedFrom(store.ChainBilledUsage{InputTokens: 2_000_000, InputNanos: 1}, nil)
	require.False(t, ok, "no output billed")
}

func TestRealizedFromNoDiscountAboveList(t *testing.T) {
	rp, ok := realizedFrom(store.ChainBilledUsage{InputTokens: 1_000_000, OutputTokens: 1_000_000, InputNanos: 2e9, OutputNanos: 2e9},
		&officialPrice{InputPerM: 1, OutputPerM: 1})
	require.True(t, ok)
	require.Zero(t, rp.DiscountPct)
}

// A market chain with enough billed traffic publishes the billed average and
// its discount; one without falls back to the live quote with no discount.
func TestPublicModelsUsesBilledAverage(t *testing.T) {
	db, gw := newPublicTestGatewayWithDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	in, out := 1.0, 10.0
	for _, id := range []string{"busy", "quiet"} {
		require.NoError(t, db.Chains().Create(ctx, store.Chain{
			ID: id, TenantID: store.DefaultTenantID, Name: id, Strategy: "priority",
			InputPerM: 0.01, OutputPerM: 0.1, CacheReadPerM: 0.001, OfficialInputPerM: &in, OfficialOutputPerM: &out,
			Steps:     []store.ChainStep{{ID: id + "-s1", Provider: "custom-openai-x", Model: "m", MarketSlug: "surplus-trusted:" + id, Position: 0}},
			CreatedAt: now, UpdatedAt: now,
		}))
	}
	require.NoError(t, db.Usage().RecordBatch(ctx, []store.UsageRecord{
		{ID: "r1", TenantID: store.DefaultTenantID, ChainID: "busy", Status: "success", PromptTokens: 1_000_000, CompletionTokens: 100_000,
			InputCostNanos: 500_000_000, OutputCostNanos: 800_000_000, CostNanos: 1_300_000_000, CreatedAt: now},
		{ID: "r2", TenantID: store.DefaultTenantID, ChainID: "quiet", Status: "success", PromptTokens: 1000, CompletionTokens: 100,
			InputCostNanos: 10, OutputCostNanos: 10, CostNanos: 20, CreatedAt: now},
	}))

	cache := market.NewSnapshotCache()
	cache.Replace([]market.Model{
		market.TrustedQuote{Found: true, BestIn: 0.01, BestOut: 0.1, CapIn: 0.4, CapOut: 4, MinDiscountPct: 60, Providers: []string{"a"}}.Model("busy"),
		market.TrustedQuote{Found: true, BestIn: 0.01, BestOut: 0.1, CapIn: 0.4, CapOut: 4, MinDiscountPct: 60, Providers: []string{"a"}}.Model("quiet"),
	})
	gw.marketCache = cache

	rows, err := gw.publicModelRows(ctx)
	require.NoError(t, err)
	byName := map[string]publicModelRow{}
	for _, r := range rows {
		byName[r.Name] = r
	}
	busy, quiet := byName["busy"], byName["quiet"]
	require.False(t, busy.SoldOut)
	require.Equal(t, "live", busy.PriceBasis)
	require.InDelta(t, 0.01, busy.InputPerM, 1e-9, "the live quote is shown")
	require.InDelta(t, 0.44, busy.MaxInputPerM, 1e-9, "failover cap with the 10% default markup")
	require.InDelta(t, 0.5, busy.AvgInputPerM, 1e-9)
	require.Equal(t, 35, busy.DiscountPct, "discount is the billed average")
	require.Equal(t, "live", quiet.PriceBasis)
	require.Zero(t, quiet.DiscountPct, "too little traffic for an average")
	require.Zero(t, quiet.AvgTokens)
	require.InDelta(t, 0.44, quiet.MaxInputPerM, 1e-9)
}

func TestPublicModelsFixedChainKeepsListDiscount(t *testing.T) {
	db, gw := newPublicTestGatewayWithDB(t)
	in, out := 1.0, 10.0
	require.NoError(t, db.Chains().Create(context.Background(), store.Chain{
		ID: "c-fixed", TenantID: store.DefaultTenantID, Name: "fixed", Strategy: "priority",
		InputPerM: 0.2, OutputPerM: 2, OfficialInputPerM: &in, OfficialOutputPerM: &out,
		Steps:     []store.ChainStep{{Provider: "custom-openai-x", Model: "unknown-model-xyz", Position: 0}},
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}))
	rec := httptest.NewRecorder()
	gw.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/public/models", nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var payload struct {
		Models []map[string]any `json:"models"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.Len(t, payload.Models, 1)
	require.Equal(t, "fixed", payload.Models[0]["price_basis"])
	require.Equal(t, 80.0, payload.Models[0]["discount_pct"])
	require.Nil(t, payload.Models[0]["avg_window_days"])
}
