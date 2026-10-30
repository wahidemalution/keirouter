package market

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const (
	// SurplusFastPrefix keys Surplus models routed for speed: any available and
	// healthy offer (trusted or not) within a price band above the cheapest,
	// with providers tried fastest first by observed TTFT. Like
	// surplus-trusted, requests are billed from the upstream's reported cost.
	SurplusFastPrefix = "surplus-fast:"
	// DefaultSurplusFastTolerancePct is how far above the cheapest eligible
	// offer (input + output per 1M) a surplus-fast offer may ask and still be
	// routed to.
	DefaultSurplusFastTolerancePct = 30
	// SurplusFastMinUptimePct drops providers whose published 24h uptime shows
	// they fail most requests. Short-term slowness is left to FastHealth, which
	// judges providers on KeiRouter's own recent traffic. An unpublished uptime
	// never drops a provider.
	SurplusFastMinUptimePct = 50
	// SurplusFastFirstOutputTimeout is how long a surplus-fast stream may go
	// without model output before the chain moves to its next step.
	SurplusFastFirstOutputTimeout = 20 * time.Second
)

// CutUpstreamCostSlug returns the Surplus model name of a slug billed from the
// upstream's reported cost (surplus-trusted: or surplus-fast:).
func CutUpstreamCostSlug(slug string) (string, bool) {
	for _, prefix := range []string{SurplusTrustedPrefix, SurplusFastPrefix} {
		if name, ok := strings.CutPrefix(slug, prefix); ok && name != "" {
			return name, true
		}
	}
	return "", false
}

// IsUpstreamCostSlug reports whether slug is a surplus-trusted: or
// surplus-fast: slug.
func IsUpstreamCostSlug(slug string) bool {
	_, ok := CutUpstreamCostSlug(slug)
	return ok
}

// ProviderPerf is one provider's published 24h performance for a model.
// Nil means Surplus has too few samples to publish the figure.
type ProviderPerf struct {
	UptimePct    *float64 `json:"uptime_pct"`
	TTFTP50MS    *float64 `json:"ttft_p50_ms"`
	LatencyP50MS *float64 `json:"p50_latency_ms"`
}

// ParseSurplusPerformance reads GET /api/analytics/performance/:model into a
// map keyed by provider id (the order book's provider_id).
func ParseSurplusPerformance(r io.Reader) (map[string]ProviderPerf, error) {
	var body struct {
		Providers []struct {
			Provider string `json:"provider"`
			ProviderPerf
		} `json:"providers"`
	}
	if err := json.NewDecoder(r).Decode(&body); err != nil {
		return nil, err
	}
	out := make(map[string]ProviderPerf, len(body.Providers))
	for _, p := range body.Providers {
		if p.Provider != "" {
			out[p.Provider] = p.ProviderPerf
		}
	}
	return out, nil
}

// SurplusPerformanceURL returns the performance URL for model, next to the
// markets feed (…/api/markets → …/api/analytics/performance/<model>).
func SurplusPerformanceURL(marketsURL, model string) string {
	if marketsURL == "" {
		marketsURL = SurplusAPIURL
	}
	base := strings.TrimSuffix(strings.TrimSuffix(marketsURL, "/"), "/markets")
	return base + "/analytics/performance/" + url.PathEscape(model)
}

// FetchSurplusPerformance downloads one model's provider performance. A 404
// (no recent traffic, or the rollup is stale) is not an error: it returns an
// empty map and the quote falls back to price order.
func FetchSurplusPerformance(ctx context.Context, marketsURL, model string) (map[string]ProviderPerf, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, SurplusPerformanceURL(marketsURL, model), nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return map[string]ProviderPerf{}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("surplus performance status %d", resp.StatusCode)
	}
	return ParseSurplusPerformance(resp.Body)
}

// unreliableForFast reports whether perf shows a provider below the
// surplus-fast uptime floor.
func unreliableForFast(p ProviderPerf) bool {
	return p.UptimePct != nil && *p.UptimePct < SurplusFastMinUptimePct
}

// ParseSurplusFastBook reads GET /api/markets/:model and quotes the offers that
// are available and healthy (trust is not required). Providers whose 24h
// uptime falls below SurplusFastMinUptimePct are dropped before the band is
// set. floorPct > 0 drops offers
// that cannot prove the provider's /min{N} floor. Offers from blocked
// providers are ignored, so they never set the band or the ceiling.
//
// The band is every offer asking at most tolerancePct above the cheapest one,
// comparing input + output per 1M. Its providers go first, ranked by perf:
// lowest TTFT p50, then lowest latency p50, then cheapest; a provider without
// published figures ranks after those with them. The quote's displayed rate is
// the top-ranked provider's cheapest band offer, since that is who serves first.
//
// The cheapest offers are often ones Surplus will not route to (seller
// credentials denied, thin books), which the order book does not show. So the
// offers above the band are kept as overflow up to the depth-th price level
// that brings a new provider: their providers follow the band's, cheapest
// first, and Surplus fails over to them within the request instead of
// answering no_healthy_sellers. Overflow ranks by price because it can reach
// far dearer offers; FastHealth already sets aside the ones slow right now.
// depth 0 keeps every eligible offer and sends no ceiling.
//
// MinDiscountPct is the X-Min-Discount ceiling that admits every kept offer,
// so Surplus cannot route above them. It is zero (no ceiling) when some kept
// offer has no list price, as a ceiling would then exclude it.
func ParseSurplusFastBook(r io.Reader, perf map[string]ProviderPerf, tolerancePct float64, depth, floorPct int, blocked []string) (TrustedQuote, error) {
	var body trustedBookEnvelope
	if err := json.NewDecoder(r).Decode(&body); err != nil {
		return TrustedQuote{}, err
	}
	if tolerancePct < 0 || math.IsNaN(tolerancePct) || math.IsInf(tolerancePct, 0) {
		tolerancePct = 0
	}
	type ask struct {
		provider                     string
		in, out, directIn, directOut float64
	}
	var asks []ask
	skip := providerSet(blocked)
	for _, o := range body.Offers {
		if !o.Available || !o.Healthy || o.ProviderID == "" || skip[strings.ToLower(o.ProviderID)] {
			continue
		}
		if unreliableForFast(perf[o.ProviderID]) {
			continue
		}
		if o.PriceInputPer1M == nil || o.PriceOutputPer1M == nil {
			continue
		}
		a := ask{provider: o.ProviderID, in: *o.PriceInputPer1M / surplusUnitScale, out: *o.PriceOutputPer1M / surplusUnitScale}
		if !validRate(a.in) || !validRate(a.out) || a.out <= 0 {
			continue
		}
		if o.DirectInputPer1M != nil && o.DirectOutputPer1M != nil {
			a.directIn = *o.DirectInputPer1M / surplusUnitScale
			a.directOut = *o.DirectOutputPer1M / surplusUnitScale
		}
		if floorPct > 0 && !meetsFloor(a.in, a.out, a.directIn, a.directOut, floorPct) {
			continue
		}
		asks = append(asks, a)
	}
	if len(asks) == 0 {
		return TrustedQuote{}, nil
	}
	sort.SliceStable(asks, func(i, j int) bool {
		si, sj := asks[i].in+asks[i].out, asks[j].in+asks[j].out
		if si != sj {
			return si < sj
		}
		return asks[i].out < asks[j].out
	})
	limit := (asks[0].in+asks[0].out)*(1+tolerancePct/100) + 1e-12
	bandLen := 0
	for bandLen < len(asks) && asks[bandLen].in+asks[bandLen].out <= limit {
		bandLen++
	}
	kept := len(asks)
	if depth > 0 {
		// Only a price level that brings a provider not yet kept counts toward
		// depth, so one provider's many offers cannot fill every backup slot.
		// Levels in between stay: they are other sellers of kept providers.
		have := map[string]bool{}
		for _, a := range asks[:bandLen] {
			have[a.provider] = true
		}
		levels := 0
		for i := bandLen; i < len(asks); {
			end, adds := i, false
			for end < len(asks) && asks[end].in == asks[i].in && asks[end].out == asks[i].out {
				adds = adds || !have[asks[end].provider]
				end++
			}
			if adds {
				if levels == depth {
					kept = i
					break
				}
				levels++
			}
			for _, a := range asks[i:end] {
				have[a.provider] = true
			}
			i = end
		}
	}
	band, overflow := asks[:bandLen], asks[bandLen:kept]

	// rank orders the providers of offers (each by its cheapest one there),
	// skipping providers already placed: by speed, or by price alone.
	placed := map[string]bool{}
	rank := func(offers []ask, bySpeed bool) ([]string, map[string]ask) {
		best := map[string]ask{}
		for _, a := range offers {
			if placed[a.provider] {
				continue
			}
			if b, ok := best[a.provider]; !ok || a.in+a.out < b.in+b.out {
				best[a.provider] = a
			}
		}
		order := make([]string, 0, len(best))
		for p := range best {
			order = append(order, p)
			placed[p] = true
		}
		sort.Slice(order, func(i, j int) bool {
			if bySpeed {
				pi, pj := perf[order[i]], perf[order[j]]
				if c := cmpKnownFirst(pi.TTFTP50MS, pj.TTFTP50MS); c != 0 {
					return c < 0
				}
				if c := cmpKnownFirst(pi.LatencyP50MS, pj.LatencyP50MS); c != 0 {
					return c < 0
				}
			}
			bi, bj := best[order[i]], best[order[j]]
			if si, sj := bi.in+bi.out, bj.in+bj.out; si != sj {
				return si < sj
			}
			return order[i] < order[j]
		})
		return order, best
	}
	order, bandBest := rank(band, true)
	more, _ := rank(overflow, false)
	order = append(order, more...)

	top := bandBest[order[0]]
	keptAsks := asks[:kept]
	q := TrustedQuote{Found: true, BestIn: top.in, BestOut: top.out, Eligible: len(keptAsks), ProviderOrder: order}
	q.Providers = append([]string(nil), order...)
	sort.Strings(q.Providers)

	// Without a ceiling, the fallback bill is the dearest kept offer.
	for _, a := range keptAsks {
		q.CapIn, q.CapOut = math.Max(q.CapIn, a.in), math.Max(q.CapOut, a.out)
	}
	if depth <= 0 {
		return q, nil
	}
	disc := math.Inf(1)
	for _, a := range keptAsks {
		if a.directIn <= 0 || a.directOut <= 0 {
			return q, nil
		}
		disc = math.Min(disc, math.Min(1-a.in/a.directIn, 1-a.out/a.directOut)*100)
	}
	n := max(int(math.Floor(disc+1e-9))-ceilingMarginPct, floorPct)
	if n > 0 && n <= 100 {
		q.MinDiscountPct = n
		keep := 1 - float64(n)/100
		var capIn, capOut float64
		for _, a := range keptAsks {
			capIn = math.Max(capIn, math.Max(a.in, a.directIn*keep))
			capOut = math.Max(capOut, math.Max(a.out, a.directOut*keep))
		}
		q.CapIn, q.CapOut = capIn, capOut
	}
	return q, nil
}

// cmpKnownFirst orders two optional figures ascending, unknown last.
func cmpKnownFirst(a, b *float64) int {
	switch {
	case a == nil && b == nil:
		return 0
	case a == nil:
		return 1
	case b == nil:
		return -1
	case *a < *b:
		return -1
	case *a > *b:
		return 1
	}
	return 0
}

// FetchSurplusFastBook downloads one model's order book and performance and
// quotes it for surplus-fast routing. A failed performance fetch is not fatal:
// providers then rank by price.
func FetchSurplusFastBook(ctx context.Context, marketsURL, model string, tolerancePct float64, depth, floorPct int, blocked []string) (TrustedQuote, error) {
	perf, err := FetchSurplusPerformance(ctx, marketsURL, model)
	if err != nil {
		perf = nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, SurplusBookURL(marketsURL, model), nil)
	if err != nil {
		return TrustedQuote{}, err
	}
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return TrustedQuote{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return TrustedQuote{}, fmt.Errorf("surplus book status %d", resp.StatusCode)
	}
	return ParseSurplusFastBook(resp.Body, perf, tolerancePct, depth, floorPct, blocked)
}
