package gateway

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/market"
	"github.com/mydisha/keirouter/backend/internal/store"
)

const detailBook = `{"offers":[
 {"provider_id":"alpha","provider":"Alpha","trusted":true,"available":true,"healthy":true,"price_input_per_1m":200000,"price_output_per_1m":800000,"direct_input_per_1m":1000000,"direct_output_per_1m":4000000},
 {"provider_id":"beta","provider":"Beta","trusted":true,"available":true,"healthy":true,"price_input_per_1m":300000,"price_output_per_1m":1200000,"direct_input_per_1m":1000000,"direct_output_per_1m":4000000},
 {"provider_id":"gamma","provider":"Gamma","trusted":true,"available":true,"healthy":true,"price_input_per_1m":500000,"price_output_per_1m":2000000,"direct_input_per_1m":1000000,"direct_output_per_1m":4000000},
 {"provider_id":"shady","provider":"Shady","trusted":false,"available":true,"healthy":true,"price_input_per_1m":1000,"price_output_per_1m":1000}
]}`

func detailChain(markup float64, offIn, offOut float64) store.Chain {
	return store.Chain{
		ID: "chain-id-qz7", TenantID: "tenant-id-xk9", Name: "m",
		MarkupPercent: &markup, OfficialInputPerM: &offIn, OfficialOutputPerM: &offOut,
		Steps: []store.ChainStep{{ID: "s1", Provider: "custom-openai-surplus", Model: "m", MarketSlug: "surplus-trusted:m"}},
	}
}

// Safety on in both cases. With markup 100 alpha's marked-up price sits above
// the safety floor; with markup 10 the floor wins.
func TestDetailBestMatchesCardDiscount(t *testing.T) {
	offers, err := market.EligibleTrustedOffers(strings.NewReader(detailBook), 0, nil)
	require.NoError(t, err)
	require.Len(t, offers, 3)
	quote, err := market.ParseSurplusTrustedBook(strings.NewReader(detailBook), 2, 0, nil)
	require.NoError(t, err)
	snap := quote.Model("m")
	floorIn, floorOut := snap.FailoverIn*1.1, snap.FailoverOut*1.1

	for _, tc := range []struct {
		name      string
		markup    float64
		wantFloor bool
	}{
		{"markup above floor", 100, false},
		{"floor wins", 10, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gw := newPublicTestGateway(t)
			cache := market.NewSnapshotCache()
			cache.Replace([]market.Model{snap})
			gw.marketCache = cache

			c := detailChain(tc.markup, 2, 8)
			settings := market.Settings{MarkupPercent: 0, SafetyMarginEnabled: true, SafetyMarginPercent: 10}
			card := gw.surplusChainPrice(c, []string{"surplus-trusted:m"}, settings)
			require.True(t, card.Available)
			official, ok := gw.chainOfficialPrice(c)
			require.True(t, ok)

			d := buildModelDetail(detailInputs{
				Chain: c, Offers: offers, OffersOK: true, BookAt: time.Now(),
				Snapshot: snap, Settings: settings, Official: &official,
			})
			require.NotNil(t, d.Best)
			require.NotNil(t, d.Best.DiscountPct)
			require.Equal(t, discountPercent(*card.InputPerM, *card.OutputPerM, official), *d.Best.DiscountPct)
			require.InDelta(t, *card.InputPerM, d.Best.InputPerM, 1e-12)
			require.InDelta(t, *card.OutputPerM, d.Best.OutputPerM, 1e-12)
			require.Equal(t, 3, *d.OfferCount)

			if tc.wantFloor {
				require.InDelta(t, floorIn, d.Best.InputPerM, 1e-9)
				require.InDelta(t, floorOut, d.Best.OutputPerM, 1e-9)
				return
			}
			mult := 1 + tc.markup/100
			require.InDelta(t, offers[0].In*mult, d.Best.InputPerM, 1e-9, "alpha marked up")
			require.InDelta(t, offers[0].Out*mult, d.Best.OutputPerM, 1e-9, "alpha marked up")
			require.Greater(t, d.Best.InputPerM, floorIn+1e-6)
			require.Greater(t, d.Best.OutputPerM, floorOut+1e-6)
			require.Equal(t, 80, *d.Best.DiscountPct)
		})
	}
}

func TestDetailAppliesMarkupToEveryOffer(t *testing.T) {
	offers := []market.TrustedOffer{
		{ProviderID: "c", Provider: "C", In: 1.5, Out: 1.5},
		{ProviderID: "a", Provider: "A", In: 0.5, Out: 0.5},
		{ProviderID: "b", Provider: "B", In: 1, Out: 1},
	}
	official := officialPrice{InputPerM: 10, OutputPerM: 10}
	d := buildModelDetail(detailInputs{
		Chain: detailChain(50, 10, 10), Offers: offers, OffersOK: true, BookAt: time.Now(),
		Snapshot: market.Model{Slug: "surplus-trusted:m", MinAskIn: 0.5, MinAskOut: 0.5},
		Settings: market.Settings{MarkupPercent: 10}, Official: &official,
	})
	require.NotNil(t, d.Best)
	require.InDelta(t, 0.75, d.Best.InputPerM, 1e-9)
	require.InDelta(t, 0.75, d.Best.OutputPerM, 1e-9)
	require.Equal(t, 92, *d.Best.DiscountPct)
	require.NotNil(t, d.MedianDiscountPct)
	require.Equal(t, 85, *d.MedianDiscountPct)
	require.Equal(t, 3, *d.OfferCount)
}

// The builder never reads the chain's slugs: a plain surplus: slug on the
// chain does not change Best, which comes only from the given offers.
func TestDetailIgnoresChainSlugsUsesGivenOffers(t *testing.T) {
	c := detailChain(0, 10, 10)
	c.MarketSlugs = []string{"surplus:m", "surplus-trusted:m"}
	offers := []market.TrustedOffer{{ProviderID: "a", Provider: "A", In: 2, Out: 3}}
	official := officialPrice{InputPerM: 10, OutputPerM: 10}
	d := buildModelDetail(detailInputs{
		Chain: c, Offers: offers, OffersOK: true, BookAt: time.Now(),
		Snapshot: market.Model{Slug: "surplus-trusted:m", MinAskIn: 0.1, MinAskOut: 0.1},
		Settings: market.Settings{}, Official: &official,
	})
	// The snapshot ask is cheaper, but Best still comes from the given offer.
	require.NotNil(t, d.Best)
	require.InDelta(t, 2, d.Best.InputPerM, 1e-9)
	require.InDelta(t, 3, d.Best.OutputPerM, 1e-9)
	require.Equal(t, 75, *d.Best.DiscountPct)
}

func TestDetailNoOfficialPrice(t *testing.T) {
	offers := []market.TrustedOffer{{ProviderID: "a", Provider: "A", In: 1, Out: 1}}
	d := buildModelDetail(detailInputs{
		Chain: detailChain(0, 0, 0), Offers: offers, OffersOK: true, BookAt: time.Now(),
		Snapshot: market.Model{Slug: "surplus-trusted:m"},
	})
	require.NotNil(t, d.Best)
	require.Nil(t, d.Best.DiscountPct)
	require.Nil(t, d.MedianDiscountPct)
	require.Nil(t, d.OfficialInputPerM)
	require.Nil(t, d.OfficialOutputPerM)
	require.Equal(t, 1, *d.OfferCount)
}

func TestDetailOffersUnavailableAndEmpty(t *testing.T) {
	official := officialPrice{InputPerM: 10, OutputPerM: 10}
	down := buildModelDetail(detailInputs{Chain: detailChain(0, 10, 10), Official: &official, Requests: 7})
	require.Nil(t, down.Best)
	require.Nil(t, down.MedianDiscountPct)
	require.Nil(t, down.OfferCount)
	require.Nil(t, down.AsOf)
	require.Nil(t, down.Performance)
	require.Equal(t, int64(7), down.Requests24h)

	empty := buildModelDetail(detailInputs{Chain: detailChain(0, 10, 10), OffersOK: true, BookAt: time.Now(), Official: &official})
	require.Nil(t, empty.Best)
	require.Nil(t, empty.MedianDiscountPct)
	require.NotNil(t, empty.OfferCount)
	require.Equal(t, 0, *empty.OfferCount)
	require.NotNil(t, empty.AsOf)
}

func TestDetailRoutesKeepEligibleProviders(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	perfAt := time.Date(2026, 11, 2, 7, 55, 0, 0, time.FixedZone("x", 3600))
	d := buildModelDetail(detailInputs{
		Chain:    detailChain(0, 10, 10),
		Offers:   []market.TrustedOffer{{ProviderID: "a", Provider: "Alpha", In: 1, Out: 1}, {ProviderID: "b", Provider: "", In: 2, Out: 2}},
		OffersOK: true, BookAt: time.Now(),
		Routes: []market.RoutePerf{
			{Provider: "a", Requests: 30, P50LatencyMS: f(100), UptimePct: f(99.5), TokensPerSec: f(50)},
			{Provider: "b", Requests: 10},
			{Provider: "z", Requests: 1000},
		},
		RoutesOK: true, PerfAt: perfAt,
	})
	require.NotNil(t, d.Performance)
	require.Equal(t, "2026-11-02T06:55:00Z", d.Performance.AsOf)
	require.Len(t, d.Performance.Routes, 2)
	r := d.Performance.Routes[0]
	require.Equal(t, "Alpha", r.Name)
	require.Equal(t, 75, r.SharePct)
	require.Equal(t, 99.5, *r.SuccessPct)
	require.Equal(t, 50.0, *r.TokensPerSec)
	require.Equal(t, "b", d.Performance.Routes[1].Name)

	dashed := buildModelDetail(detailInputs{
		Chain: detailChain(0, 10, 10),
		Offers: []market.TrustedOffer{
			{ProviderID: "ds", Provider: "Alibaba DashScope \u2014 China", In: 1, Out: 1},
			{ProviderID: "x", Provider: "A\u2014B", In: 1, Out: 1},
		},
		OffersOK: true, BookAt: time.Now(),
		Routes:   []market.RoutePerf{{Provider: "ds", Requests: 2}, {Provider: "x", Requests: 1}},
		RoutesOK: true, PerfAt: perfAt,
	})
	require.Equal(t, "Alibaba DashScope \u00b7 China", dashed.Performance.Routes[0].Name)
	require.Equal(t, "A\u00b7B", dashed.Performance.Routes[1].Name)
}

func TestDetailJSONHasNoLeaks(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	official := officialPrice{InputPerM: 10, OutputPerM: 10, Source: "models.dev", Provider: "surplusintelligence"}
	d := buildModelDetail(detailInputs{
		Chain:    detailChain(25, 10, 10),
		Offers:   []market.TrustedOffer{{ProviderID: "a", Provider: "Alpha", In: 0.123456, Out: 0.654321, DirectIn: 1, DirectOut: 2}},
		OffersOK: true, BookAt: time.Now(),
		Snapshot: market.Model{Slug: "surplus-trusted:m", MinAskIn: 0.123456, MinAskOut: 0.654321, FailoverIn: 0.2, FailoverOut: 0.7},
		Settings: market.Settings{MarkupPercent: 25, SafetyMarginPercent: 10},
		Official: &official, Requests: 12,
		Routes:   []market.RoutePerf{{Provider: "a", Requests: 5, P50LatencyMS: f(1), P95LatencyMS: f(2), TokensPerSec: f(3), UptimePct: f(4)}},
		RoutesOK: true, PerfAt: time.Now(),
	})
	b, err := json.Marshal(d)
	require.NoError(t, err)
	body := string(b)
	require.Contains(t, body, `"best"`)
	require.Contains(t, body, `"routes"`)
	for _, bad := range []string{
		"seller_base_url", "reference_", "surplusintelligence", "markup", "safety", "cost",
		"chain-id-qz7", "tenant-id-xk9", "0.123456", "0.654321", "provider_id", "models.dev",
	} {
		require.NotContains(t, body, bad)
	}
}
