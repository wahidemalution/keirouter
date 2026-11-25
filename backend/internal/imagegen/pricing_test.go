package imagegen

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/dispatch"
	"github.com/mydisha/keirouter/backend/internal/store"
)

var grokNo2K = dispatch.Target{Provider: "xai", Model: "grok-imagine-image", ImagePrice: 0.02, ImageBillMult: 1.25}

// A grok-imagine step without a 2K cost cannot bill a 2K image: its
// standard price would sell below the 2K cost.
func TestGrokHighResNeeds2KPrice(t *testing.T) {
	_, ok := priceOf(grokNo2K, "2k")
	require.False(t, ok)
	p, ok := priceOf(grokNo2K, "1k")
	require.True(t, ok)
	require.Equal(t, int64(25_000), p.sell)

	with2K := grokNo2K
	with2K.ImagePrice2K = 0.05
	p, ok = priceOf(with2K, "2k")
	require.True(t, ok)
	require.Equal(t, int64(62_500), p.sell)

	_, ok = ShownPriceMicro([]dispatch.Target{grokNo2K}, "2k")
	require.False(t, ok)
	got, ok := ShownPriceMicro([]dispatch.Target{grokNo2K, with2K}, "2k")
	require.True(t, ok)
	require.Equal(t, int64(62_500), got, "the step without a 2K cost is skipped")

	e := newEnv(t, nImages)
	key := e.key("k1", 10*priceStd, true)
	in := input("A", key, 1, grokNo2K)
	in.Resolution = "2k"
	_, err := e.svc.Submit(context.Background(), in)
	require.ErrorIs(t, err, ErrNoImageTarget)
}

// The shown price is the first priced step in declared order, whatever the
// runtime order is, and the client must have seen that price.
func TestShownPriceFromDeclaredOrder(t *testing.T) {
	cheap := dispatch.Target{Provider: "a", Model: "gpt-image-1", ImagePrice: 0.02, ImageBillMult: 1.5}
	pricey := dispatch.Target{Provider: "b", Model: "gpt-image-1", ImagePrice: 0.04, ImageBillMult: 1.5}
	e := newEnv(t, nImages)
	e.start()
	key := e.key("k1", 10*priceStd, true)

	in := input("A", key, 1, cheap, pricey) // runtime order: cheapest first
	in.PriceOrder = []dispatch.Target{pricey, cheap}
	in.ExpectedPriceMicro = 30_000 // the cheap step's price, not the declared one
	_, err := e.svc.Submit(context.Background(), in)
	require.ErrorIs(t, err, ErrPriceChanged)
	jobs, err := e.db.ImageJobs().ListByOwner(context.Background(), "A", 10)
	require.NoError(t, err)
	require.Empty(t, jobs, "no job is created")
	require.Empty(t, e.bud.reserved, "nothing is reserved")

	in.ExpectedPriceMicro = priceStd
	job := e.submitWait(in)
	require.Equal(t, store.ImageJobSucceeded, job.Status, job.Error)
	require.Equal(t, 2, len(e.gen.calls[0].Targets), "both steps are at or below the shown price")
	require.Equal(t, "a", e.gen.calls[0].Targets[0].Provider, "runtime order is kept")
	require.Equal(t, int64(30_000), e.spend("k1"), "billed at the step that answered")
}

func TestPriceRefusesNonFiniteAndHuge(t *testing.T) {
	for name, tgt := range map[string]dispatch.Target{
		"nan price":     {ImagePrice: math.NaN(), ImageBillMult: 1},
		"inf price":     {ImagePrice: math.Inf(1), ImageBillMult: 1},
		"huge price":    {ImagePrice: 1e300, ImageBillMult: 1},
		"nan mult":      {ImagePrice: 0.04, ImageBillMult: math.NaN()},
		"inf mult":      {ImagePrice: 0.04, ImageBillMult: math.Inf(1)},
		"huge mult":     {ImagePrice: 100, ImageBillMult: 1e12},
		"negative mult": {ImagePrice: 0.04, ImageBillMult: -1},
		"nan 2k":        {ImagePrice: 0.04, ImagePrice2K: math.NaN(), ImageBillMult: 1},
	} {
		res := ""
		if name == "nan 2k" {
			res = "2k"
		}
		_, ok := priceOf(tgt, res)
		require.False(t, ok, name)
	}
	_, ok := mulMicro(4, math.MaxInt64/3)
	require.False(t, ok, "overflow is refused")
	_, ok = mulMicro(4, maxImageMicro+1)
	require.False(t, ok)
	got, ok := mulMicro(4, 60_000)
	require.True(t, ok)
	require.Equal(t, int64(240_000), got)
}

// Only a key's prepaid credit (a hard-cutoff USD budget that never resets)
// pays for images; a periodic cap is not a balance.
func TestPeriodicKeyBudgetIsNotCredit(t *testing.T) {
	e := newEnv(t, nImages)
	now := time.Now()
	require.NoError(t, e.db.Budgets().Create(context.Background(), store.Budget{
		ID: "b-daily", TenantID: store.DefaultTenantID, ScopeKind: store.ScopeAPIKey, ScopeID: "k-daily",
		LimitMicros: 10 * priceStd, Period: "daily", AlertPct: 80, HardCutoff: true, CreatedAt: now, UpdatedAt: now,
	}))
	key := store.APIKey{ID: "k-daily", TenantID: store.DefaultTenantID, ProjectID: "p1"}
	_, err := e.svc.Submit(context.Background(), input("A", key, 1))
	require.ErrorIs(t, err, ErrInsufficientBalance)
	require.Zero(t, e.gen.callCount())
}
