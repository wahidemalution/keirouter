package meter

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/core"
	"github.com/mydisha/keirouter/backend/internal/store"
)

func imageEvent(count int, bill, cost float64) Event {
	return Event{
		RequestID: "img-1", TenantID: "t1", ProjectID: "p1", APIKeyID: "k1",
		Provider: "openai", Model: "gpt-image-1", ChainID: "c1",
		ImageCount: count, ImageBillMicro: bill, ImageCostMicro: cost,
	}
}

func TestImageCostBillsPerImage(t *testing.T) {
	ev := imageEvent(3, 8400, 4200)
	got, ok := imageCost(ev)
	require.True(t, ok)
	require.Equal(t, int64(25_200), got.CostMicros)
	require.Equal(t, int64(25_200_000), got.CostNanos)
	require.Equal(t, "static_image", got.Pricing.MatchKind)
	require.Equal(t, "priced", got.Pricing.Status)

	require.Equal(t, got, (&Meter{}).costForEvent(ev), "costForEvent bills images per image")
	up := upstreamCostForEvent(ev)
	require.Equal(t, int64(12_600_000), up.CostNanos)
	require.Equal(t, int64(12_600), up.CostMicros)
}

func TestImageCostNeedsCountAndBill(t *testing.T) {
	_, ok := imageCost(imageEvent(0, 8400, 4200))
	require.False(t, ok)
	_, ok = imageCost(imageEvent(2, 0, 4200))
	require.False(t, ok)
}

// Image events never pass through the token paths: no min-charge floor, no
// upstream-reported cost, no cost-lookup reconcile.
func TestImageCostIgnoresTokenBillingPaths(t *testing.T) {
	ev := imageEvent(2, 1000, 600)
	ev.StaticPrice, ev.MarketRateIn, ev.MarketRateOut, ev.UpstreamRateIn, ev.UpstreamRateOut = true, 1, 1, 1, 1
	ev.MinChargeMicro, ev.UpstreamCostMult = 99_999, 3
	ev.BillUpstreamCost = true
	ev.Usage = core.Usage{UpstreamCostReported: true, UpstreamCostMicro: 77_777}
	require.Equal(t, int64(2_000), (&Meter{}).costForEvent(ev).CostMicros)
	require.Equal(t, int64(1_200), upstreamCostForEvent(ev).CostMicros)

	var rec store.UsageRecord
	m := New(&memUsage{rows: map[string]bool{}}, nil, nil)
	m.SetRecordObserver(func(r store.UsageRecord) { rec = r })
	ev.CostLookup, ev.Streamed, ev.UpstreamRequestID = true, true, "up-1"
	got, err := m.Record(context.Background(), ev)
	require.NoError(t, err)
	require.Equal(t, int64(2_000), got)
	require.Empty(t, rec.ReconcileStatus)
	require.Empty(t, rec.UpstreamRequestID)
}

func TestRecordImageEventWithoutTokens(t *testing.T) {
	var rec store.UsageRecord
	m := New(&memUsage{rows: map[string]bool{}}, nil, nil)
	m.SetRecordObserver(func(r store.UsageRecord) { rec = r })

	got, err := m.Record(context.Background(), imageEvent(2, 8400, 4200))
	require.NoError(t, err)
	require.Equal(t, int64(16_800), got)

	require.Equal(t, 2, rec.ImageCount)
	require.Equal(t, int64(16_800), rec.CostMicros)
	require.Equal(t, int64(16_800_000), rec.CostNanos)
	require.Equal(t, int64(8_400_000), rec.UpstreamCostNanos)
	require.Equal(t, int64(8_400), rec.UpstreamCostMicros)
	require.Equal(t, "success", rec.Status)
	require.Equal(t, "priced", rec.PricingStatus)
	require.Equal(t, "static_image", rec.PricingMatchKind)
	require.Equal(t, "static_image", rec.PricingSource)
	require.NotNil(t, rec.PricingAsOf)
	require.Zero(t, rec.PromptTokens+rec.CompletionTokens)
	require.Equal(t, "k1", rec.APIKeyID)
	require.Equal(t, "c1", rec.ChainID)
}

// A chat event prices exactly as before image billing existed.
func TestTokenEventUnchanged(t *testing.T) {
	ev := staticStepEvent(core.Usage{PromptTokens: 1_000_000, CompletionTokens: 1_000_000})
	ev.ImageCostMicro = 4200 // no ImageCount: not an image event

	var rec store.UsageRecord
	m := New(&memUsage{rows: map[string]bool{}}, nil, nil)
	m.SetRecordObserver(func(r store.UsageRecord) { rec = r })
	got, err := m.Record(context.Background(), ev)
	require.NoError(t, err)
	require.Equal(t, int64(3_000_000), got)
	require.Equal(t, int64(2_500_000), rec.UpstreamCostMicros)
	require.Equal(t, "static_step", rec.PricingSource)
	require.Zero(t, rec.ImageCount)

	// Without usage, a chat event still records no price.
	got, err = m.Record(context.Background(), staticStepEvent(core.Usage{}))
	require.NoError(t, err)
	require.Zero(t, got)
	require.Equal(t, "none", rec.PricingStatus)
}
