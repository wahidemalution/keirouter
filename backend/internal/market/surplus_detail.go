package market

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"time"
)

const (
	detailFetchTimeout = 8 * time.Second
	detailMaxBody      = 2 << 20
)

// RoutePerf is one provider's published 24h performance. Nil means Surplus
// has too few samples to publish the figure.
type RoutePerf struct {
	Provider     string
	Requests     int64
	P50LatencyMS *float64
	P95LatencyMS *float64
	TokensPerSec *float64
	UptimePct    *float64
}

// RouteShare is a RoutePerf for an eligible route with its share of traffic
// among the eligible routes.
type RouteShare struct {
	Name string
	RoutePerf
	SharePct int
}

// ParsePerformanceRoutes reads GET /api/analytics/performance/:model.
func ParsePerformanceRoutes(r io.Reader) ([]RoutePerf, time.Time, error) {
	var body struct {
		ComputedAt int64 `json:"computed_at"`
		Providers  []struct {
			Provider     string   `json:"provider"`
			Requests     int64    `json:"requests"`
			P50LatencyMS *float64 `json:"p50_latency_ms"`
			P95LatencyMS *float64 `json:"p95_latency_ms"`
			TPSP50       *float64 `json:"tps_p50"`
			UptimePct    *float64 `json:"uptime_pct"`
		} `json:"providers"`
	}
	if err := json.NewDecoder(r).Decode(&body); err != nil {
		return nil, time.Time{}, err
	}
	var at time.Time
	if body.ComputedAt > 0 {
		at = time.UnixMilli(body.ComputedAt)
	}
	routes := make([]RoutePerf, 0, len(body.Providers))
	for _, p := range body.Providers {
		if p.Provider == "" {
			continue
		}
		routes = append(routes, RoutePerf{
			Provider:     p.Provider,
			Requests:     p.Requests,
			P50LatencyMS: p.P50LatencyMS,
			P95LatencyMS: p.P95LatencyMS,
			TokensPerSec: p.TPSP50,
			UptimePct:    p.UptimePct,
		})
	}
	return routes, at, nil
}

// getCapped GETs url and returns the body. It fails on a non-200 status (a 404
// is tolerated when allow404 is set) or a body over detailMaxBody.
func getCapped(ctx context.Context, url string, allow404 bool) (body []byte, found bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, false, err
	}
	resp, err := (&http.Client{Timeout: detailFetchTimeout}).Do(req)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound && allow404 {
		return nil, false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("surplus status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, detailMaxBody+1))
	if err != nil {
		return nil, false, err
	}
	if len(data) > detailMaxBody {
		return nil, false, fmt.Errorf("surplus response over %d bytes", detailMaxBody)
	}
	return data, true, nil
}

// FetchSurplusDetail downloads one model's order book and returns its
// eligible trusted offers. Any non-200 status, 404 included, is an error.
func FetchSurplusDetail(ctx context.Context, marketsURL, model string, floorPct int, blocked []string) ([]TrustedOffer, error) {
	data, _, err := getCapped(ctx, SurplusBookURL(marketsURL, model), false)
	if err != nil {
		return nil, err
	}
	return EligibleTrustedOffers(bytes.NewReader(data), floorPct, blocked)
}

// FetchSurplusBookRaw downloads one model's order book as raw JSON, so a caller
// can quote several providers from a single fetch. Any non-200 status, 404
// included, is an error.
func FetchSurplusBookRaw(ctx context.Context, marketsURL, model string) ([]byte, error) {
	data, _, err := getCapped(ctx, SurplusBookURL(marketsURL, model), false)
	return data, err
}

// FetchSurplusPerformanceRoutes downloads one model's per-route performance.
// A 404 (no recent traffic) returns no routes and no error.
func FetchSurplusPerformanceRoutes(ctx context.Context, marketsURL, model string) ([]RoutePerf, time.Time, error) {
	data, found, err := getCapped(ctx, SurplusPerformanceURL(marketsURL, model), true)
	if err != nil {
		return nil, time.Time{}, err
	}
	if !found {
		return []RoutePerf{}, time.Time{}, nil
	}
	return ParsePerformanceRoutes(bytes.NewReader(data))
}

// MedianPercent returns the median of vals, rounding down for an even count.
// It does not modify vals.
func MedianPercent(vals []int) (int, bool) {
	if len(vals) == 0 {
		return 0, false
	}
	s := append([]int(nil), vals...)
	sort.Ints(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2], true
	}
	return int(math.Floor(float64(s[n/2-1]+s[n/2]) / 2)), true
}

// ShareRoutes keeps the providers in keep (id to label) and computes each
// one's share of the kept requests, busiest first.
func ShareRoutes(perf []RoutePerf, keep map[string]string) []RouteShare {
	var sum int64
	out := make([]RouteShare, 0, len(keep))
	for _, p := range perf {
		name, ok := keep[p.Provider]
		if !ok {
			continue
		}
		out = append(out, RouteShare{Name: name, RoutePerf: p})
		sum += p.Requests
	}
	if sum > 0 {
		for i := range out {
			out[i].SharePct = int(math.Round(100 * float64(out[i].Requests) / float64(sum)))
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Requests != out[j].Requests {
			return out[i].Requests > out[j].Requests
		}
		return out[i].Name < out[j].Name
	})
	return out
}
