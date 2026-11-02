package gateway

import (
	"time"

	"github.com/mydisha/keirouter/backend/internal/market"
	"github.com/mydisha/keirouter/backend/internal/store"
)

// modelDetail is the public popup payload for one market-priced chain. Every
// price in it already includes the chain's markup and safety margin.
type modelDetail struct {
	Model              string      `json:"model"`
	AsOf               *string     `json:"as_of"`
	OfficialInputPerM  *float64    `json:"official_input_per_m"`
	OfficialOutputPerM *float64    `json:"official_output_per_m"`
	Best               *detailBest `json:"best"`
	MedianDiscountPct  *int        `json:"median_discount_pct"`
	OfferCount         *int        `json:"offer_count"`
	Requests24h        int64       `json:"requests_24h"`
	Performance        *detailPerf `json:"performance"`
}

type detailBest struct {
	InputPerM   float64 `json:"input_per_m"`
	OutputPerM  float64 `json:"output_per_m"`
	DiscountPct *int    `json:"discount_pct"`
}

type detailPerf struct {
	AsOf   string        `json:"as_of"`
	Routes []detailRoute `json:"routes"`
}

type detailRoute struct {
	Name         string   `json:"name"`
	P50LatencyMS *float64 `json:"p50_latency_ms"`
	P95LatencyMS *float64 `json:"p95_latency_ms"`
	TokensPerSec *float64 `json:"tokens_per_sec"`
	SuccessPct   *float64 `json:"success_pct"`
	SharePct     int      `json:"share_pct"`
}

// detailInputs is everything buildModelDetail needs, gathered by the handler.
// OffersOK/RoutesOK are false when that source has no data at all.
type detailInputs struct {
	Chain    store.Chain
	Offers   []market.TrustedOffer
	OffersOK bool
	BookAt   time.Time
	// Snapshot is the chain's trusted model from the routing snapshot; its
	// failover rates feed the safety floor the same way billing does.
	Snapshot market.Model
	Settings market.Settings
	Official *officialPrice
	Requests int64
	Routes   []market.RoutePerf
	RoutesOK bool
	PerfAt   time.Time
}

// buildModelDetail prices each eligible offer with the same rate function the
// market sync uses, then shapes the public payload.
func buildModelDetail(in detailInputs) modelDetail {
	d := modelDetail{Model: in.Chain.Name, Requests24h: in.Requests}
	if in.Official != nil {
		d.OfficialInputPerM, d.OfficialOutputPerM = price(in.Official.InputPerM), price(in.Official.OutputPerM)
	}
	if in.OffersOK {
		asOf := in.BookAt.UTC().Format(time.RFC3339)
		d.AsOf = &asOf
		d.Best, d.MedianDiscountPct, d.OfferCount = priceOffers(in)
	}
	if in.RoutesOK {
		keep := make(map[string]string, len(in.Offers))
		for _, o := range in.Offers {
			if _, seen := keep[o.ProviderID]; seen {
				continue
			}
			name := o.Provider
			if name == "" {
				name = o.ProviderID
			}
			keep[o.ProviderID] = name
		}
		routes := []detailRoute{}
		for _, r := range market.ShareRoutes(in.Routes, keep) {
			routes = append(routes, detailRoute{
				Name: r.Name, P50LatencyMS: r.P50LatencyMS, P95LatencyMS: r.P95LatencyMS,
				TokensPerSec: r.TokensPerSec, SuccessPct: r.UptimePct, SharePct: r.SharePct,
			})
		}
		d.Performance = &detailPerf{AsOf: in.PerfAt.UTC().Format(time.RFC3339), Routes: routes}
	}
	return d
}

// priceOffers returns the cheapest marked-up offer, the median discount and
// the number of offers that priced. Offers the rate function rejects are
// skipped.
func priceOffers(in detailInputs) (*detailBest, *int, *int) {
	markup := market.EffectiveMarkup(in.Chain.MarkupPercent, in.Settings.MarkupPercent)
	var best *detailBest
	var discounts []int
	count := 0
	for _, o := range in.Offers {
		m := market.Model{Slug: "o", MinAskIn: o.In, MinAskOut: o.Out, FailoverIn: in.Snapshot.FailoverIn, FailoverOut: in.Snapshot.FailoverOut}
		rate, ok := market.ComputeChainRateWithSafety([]string{"o"}, []market.Model{m}, markup,
			market.CacheReadMult, market.CacheWriteMult, in.Settings.SafetyMarginEnabled, in.Settings.SafetyMarginPercent)
		if !ok {
			continue
		}
		count++
		// Rounded like the card's price so the discounts agree.
		pin, pout := *price(rate.InputPerM), *price(rate.OutputPerM)
		if in.Official != nil {
			discounts = append(discounts, discountPercent(pin, pout, *in.Official))
		}
		if best == nil || cheaper(pin, pout, best.InputPerM, best.OutputPerM) {
			best = &detailBest{InputPerM: pin, OutputPerM: pout}
		}
	}
	if best != nil && in.Official != nil {
		pct := discountPercent(best.InputPerM, best.OutputPerM, *in.Official)
		best.DiscountPct = &pct
	}
	var median *int
	if v, ok := market.MedianPercent(discounts); ok {
		median = &v
	}
	return best, median, &count
}

// cheaper orders by blend, then output, then input.
func cheaper(in, out, bestIn, bestOut float64) bool {
	if in+out != bestIn+bestOut {
		return in+out < bestIn+bestOut
	}
	if out != bestOut {
		return out < bestOut
	}
	return in < bestIn
}
