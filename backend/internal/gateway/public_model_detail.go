package gateway

import (
	"context"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/mydisha/keirouter/backend/internal/market"
	"github.com/mydisha/keirouter/backend/internal/store"
)

const (
	detailBookTTL     = 30 * time.Second
	detailPerfTTL     = 5 * time.Minute
	detailRequestsTTL = 30 * time.Second
	// detailRetryAfter holds off a refetch after an upstream failure so an
	// outage cannot turn popups into a retry storm against Surplus.
	detailRetryAfter        = 15 * time.Second
	detailRequestsTimeout   = 5 * time.Second
	detailMaxModelLen       = 200
	detailRequestsPerMinute = 60
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
			// Surplus labels separate region with an em dash; the UI uses a middle dot.
			name = strings.ReplaceAll(strings.ReplaceAll(name, " \u2014 ", " \u00b7 "), "\u2014", "\u00b7")
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

type cachedBook struct {
	key    string // model|floor|blocked the offers were filtered under
	offers []market.TrustedOffer
	at     time.Time
}

type cachedPerf struct {
	routes []market.RoutePerf
	asOf   time.Time
	at     time.Time
}

// detailCache holds the popup's Surplus reads and request counts. It is
// separate from marketCache and s.pricing and never writes to them. Entries
// are keyed by Surplus model names (and floors) taken from chains, never from
// the query, so the maps cannot grow past the number of market chains.
type detailCache struct {
	mu    sync.Mutex
	books map[string]cachedBook // by Surplus model name|floor
	perf  map[string]cachedPerf // by Surplus model name
	reqAt time.Time
	req   map[string]int64 // chain id to requests in the last 24h
	sf    singleflight.Group
	// failed is when each fetch (singleflight key) last failed.
	failed map[string]time.Time
	now    func() time.Time // tests override it
}

// coolingDown reports whether fetch sfKey failed less than detailRetryAfter
// ago. Callers hold c.mu.
func (c *detailCache) coolingDown(sfKey string) bool {
	at, ok := c.failed[sfKey]
	return ok && c.clock().Sub(at) < detailRetryAfter
}

// noteResult records a failed fetch or clears an earlier failure. Callers
// hold c.mu.
func (c *detailCache) noteResult(sfKey string, err error) {
	if err == nil {
		delete(c.failed, sfKey)
		return
	}
	if c.failed == nil {
		c.failed = map[string]time.Time{}
	}
	c.failed[sfKey] = c.clock()
}

func (c *detailCache) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// book returns the cached offers for slot filtered under key, fetching when
// missing or older than detailBookTTL. On a fetch error the old entry (same
// key) is kept and returned with its old timestamp, along with the error, and
// no refetch happens for detailRetryAfter.
func (c *detailCache) book(slot, key string, fetch func() ([]market.TrustedOffer, error)) (cachedBook, bool, error) {
	sfKey := "book|" + key
	c.mu.Lock()
	e, ok := c.books[slot]
	ok = ok && e.key == key
	cooling := c.coolingDown(sfKey)
	c.mu.Unlock()
	if ok && (cooling || c.clock().Sub(e.at) < detailBookTTL) {
		return e, true, nil
	}
	if cooling {
		return cachedBook{}, false, nil
	}
	v, err, _ := c.sf.Do(sfKey, func() (any, error) {
		offers, err := fetch()
		c.mu.Lock()
		defer c.mu.Unlock()
		c.noteResult(sfKey, err)
		if err == nil {
			if c.books == nil {
				c.books = map[string]cachedBook{}
			}
			c.books[slot] = cachedBook{key: key, offers: offers, at: c.clock()}
		}
		if e, ok := c.books[slot]; ok && e.key == key {
			return e, err
		}
		return nil, err
	})
	e, ok = v.(cachedBook)
	return e, ok, err
}

// performance is book for the route performance feed, cached detailPerfTTL.
func (c *detailCache) performance(name string, fetch func() ([]market.RoutePerf, time.Time, error)) (cachedPerf, bool, error) {
	sfKey := "perf|" + name
	c.mu.Lock()
	e, ok := c.perf[name]
	cooling := c.coolingDown(sfKey)
	c.mu.Unlock()
	if ok && (cooling || c.clock().Sub(e.at) < detailPerfTTL) {
		return e, true, nil
	}
	if cooling {
		return cachedPerf{}, false, nil
	}
	v, err, _ := c.sf.Do(sfKey, func() (any, error) {
		routes, asOf, err := fetch()
		c.mu.Lock()
		defer c.mu.Unlock()
		c.noteResult(sfKey, err)
		if err == nil {
			if c.perf == nil {
				c.perf = map[string]cachedPerf{}
			}
			at := c.clock()
			if asOf.IsZero() {
				asOf = at
			}
			c.perf[name] = cachedPerf{routes: routes, asOf: asOf, at: at}
		}
		if e, ok := c.perf[name]; ok {
			return e, err
		}
		return nil, err
	})
	e, ok = v.(cachedPerf)
	return e, ok, err
}

// requests returns every chain's request count over the last 24h, refreshed
// at most every detailRequestsTTL. On a query error the old map is kept; with
// no old map the error is returned.
func (c *detailCache) requests(fetch func(since time.Time) (map[string]int64, error)) (map[string]int64, error) {
	c.mu.Lock()
	req, at := c.req, c.reqAt
	c.mu.Unlock()
	if req != nil && c.clock().Sub(at) < detailRequestsTTL {
		return req, nil
	}
	v, err, _ := c.sf.Do("req", func() (any, error) {
		now := c.clock()
		fresh, err := fetch(now.Add(-24 * time.Hour))
		c.mu.Lock()
		defer c.mu.Unlock()
		if err == nil {
			c.req, c.reqAt = fresh, now
		}
		return c.req, err
	})
	req, _ = v.(map[string]int64)
	if req != nil {
		return req, nil
	}
	return nil, err
}

// publicModelDetail serves GET /v1/public/model-detail?model=<chain name>:
// the popup data for one market-priced chain, margin included. It only reads
// chains, settings, usage and the routing snapshot; the Surplus model name in
// upstream URLs comes from the chain's own slug, never from the query.
func (s *Server) publicModelDetail(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	model := strings.TrimSpace(r.URL.Query().Get("model"))
	if model == "" || len(model) > detailMaxModelLen {
		writeError(w, http.StatusNotFound, "model not found")
		return
	}
	ctx := r.Context()
	chains, err := s.chains.ListByTenant(ctx, adminTenant)
	if err != nil {
		s.log.Warn("model detail: list chains failed", "err", err)
		writeError(w, http.StatusInternalServerError, "detail unavailable")
		return
	}
	idx := slices.IndexFunc(chains, func(c store.Chain) bool { return strings.EqualFold(c.Name, model) })
	if idx < 0 || chains[idx].Disabled || len(chains[idx].Steps) == 0 {
		writeError(w, http.StatusNotFound, "model not found")
		return
	}
	c := chains[idx]
	name, prefix, ok := detailSurplusModel(c)
	if !ok {
		writeError(w, http.StatusNotFound, "no market detail")
		return
	}
	if _, static := s.leadStaticTarget(ctx, c); static {
		writeError(w, http.StatusNotFound, "no market detail")
		return
	}

	settings := market.DefaultSettings()
	if s.settings != nil {
		settings = market.LoadSettings(ctx, s.settings.Get)
	}
	floors, err := s.detailProviderFloors(ctx)
	if err != nil {
		s.log.Warn("model detail: list custom providers failed", "err", err)
		writeError(w, http.StatusInternalServerError, "detail unavailable")
		return
	}
	floor := 0
	for _, b := range market.BindTrustedSurplus([]market.ChainRoute{detailChainRoute(c)}, prefix, floors) {
		if b.Name == name {
			floor = b.FloorPct
			break
		}
	}
	// Shared fetches outlive one caller's disconnect, with their own timeout.
	fetchCtx := context.WithoutCancel(ctx)
	reqs, err := s.detail.requests(func(since time.Time) (map[string]int64, error) {
		qctx, cancel := context.WithTimeout(fetchCtx, detailRequestsTimeout)
		defer cancel()
		return s.detailRequestCounts(qctx, since)
	})
	if err != nil {
		s.log.Warn("model detail: usage query failed", "err", err)
		writeError(w, http.StatusInternalServerError, "detail unavailable")
		return
	}

	in := detailInputs{Chain: c, Settings: settings, Requests: reqs[c.ID]}
	if ref, ok := s.chainOfficialPrice(c); ok {
		in.Official = &ref
	}
	// Only trusted offers are shown, so a chain without a surplus-trusted
	// slug does not route through them and gets no price.
	trusted := prefix == market.SurplusTrustedPrefix
	if snap, ok := s.detailSnapshot(c, prefix+name); trusted && ok {
		blocked := slices.Clone(settings.SurplusBlockedProviders)
		slices.Sort(blocked)
		// Chains sharing a Surplus model with different floors get their own
		// slot; the blocked list only invalidates it.
		slot := name + "|" + strconv.Itoa(floor)
		key := slot + "|" + strings.Join(blocked, ",")
		book, ok, err := s.detail.book(slot, key, func() ([]market.TrustedOffer, error) {
			return market.FetchSurplusDetail(fetchCtx, s.surplusURL, name, floor, blocked)
		})
		if err != nil {
			s.log.Warn("model detail: order book fetch failed", "model", name, "err", err)
		}
		if ok {
			in.Offers, in.OffersOK, in.BookAt, in.Snapshot = book.offers, true, book.at, snap
		}
	}
	perf, ok, err := s.detail.performance(name, func() ([]market.RoutePerf, time.Time, error) {
		return market.FetchSurplusPerformanceRoutes(fetchCtx, s.surplusURL, name)
	})
	if err != nil {
		s.log.Warn("model detail: performance fetch failed", "model", name, "err", err)
	}
	if ok {
		in.Routes, in.RoutesOK, in.PerfAt = perf.routes, true, perf.asOf
	}

	w.Header().Set("Cache-Control", "public, max-age=30")
	writeJSON(w, http.StatusOK, buildModelDetail(in))
}

// detailSurplusModel picks the Surplus model the popup reads: the chain's
// first surplus-trusted slug, else its first surplus slug.
func detailSurplusModel(c store.Chain) (name, prefix string, ok bool) {
	slugs := surplusSlugs(c)
	for _, p := range []string{market.SurplusTrustedPrefix, market.SurplusPrefix} {
		for _, slug := range slugs {
			if n, found := strings.CutPrefix(slug, p); found && n != "" {
				return n, p, true
			}
		}
	}
	return "", "", false
}

// detailSnapshot returns the routing snapshot entry for slug with its
// failover rates set to the lowest positive ones across the chain's market
// slugs, as ComputeChainRateWithSafety does, so the popup floor is the billed
// floor. ok is false when slug has no live quote.
func (s *Server) detailSnapshot(c store.Chain, slug string) (market.Model, bool) {
	if s.marketCache == nil {
		return market.Model{}, false
	}
	snap, ok := s.marketCache.Lookup(slug)
	if !ok || snap.MinAskIn <= 0 || snap.MinAskOut <= 0 {
		return market.Model{}, false
	}
	failIn, failOut := math.Inf(1), math.Inf(1)
	for _, m := range s.marketCache.Models(surplusSlugs(c)) {
		if m.MinAskIn <= 0 || m.MinAskOut <= 0 {
			continue
		}
		if m.FailoverIn > 0 {
			failIn = math.Min(failIn, m.FailoverIn)
		}
		if m.FailoverOut > 0 {
			failOut = math.Min(failOut, m.FailoverOut)
		}
	}
	snap.FailoverIn, snap.FailoverOut = 0, 0
	if !math.IsInf(failIn, 1) {
		snap.FailoverIn = failIn
	}
	if !math.IsInf(failOut, 1) {
		snap.FailoverOut = failOut
	}
	return snap, true
}

// detailProviderFloors maps custom provider id to its /min{N} floor, the same
// source the market sync binds with.
func (s *Server) detailProviderFloors(ctx context.Context) (map[string]int, error) {
	floors := map[string]int{}
	if s.db == nil {
		return floors, nil
	}
	providers, err := s.db.CustomProviders().ListProviders(ctx, adminTenant)
	if err != nil {
		return nil, err
	}
	for _, p := range providers {
		floors[p.ID] = market.MinDiscountFloor(p.BaseURL)
	}
	return floors, nil
}

// detailChainRoute mirrors app.chainRoutes (internal/app/market_sync.go) for
// one chain; keep the two in step.
func detailChainRoute(c store.Chain) market.ChainRoute {
	steps := make([]market.StepRoute, len(c.Steps))
	for i, st := range c.Steps {
		steps[i] = market.StepRoute{MarketSlug: st.MarketSlug, Model: st.Model, Provider: st.Provider}
	}
	return market.ChainRoute{MarketSlugs: c.MarketSlugs, Steps: steps}
}

func (s *Server) detailRequestCounts(ctx context.Context, since time.Time) (map[string]int64, error) {
	out := map[string]int64{}
	if s.usage == nil {
		return out, nil
	}
	usage, err := s.usage.ChainUsageAccurate(ctx, adminTenant, since)
	if err != nil {
		return nil, err
	}
	for id, u := range usage {
		out[id] = u.TotalRequests
	}
	return out, nil
}

// publicModelDetailRateLimiter caps GET /v1/public/model-detail per client IP
// with its own budget, built like publicPricingRateLimiter.
func (s *Server) publicModelDetailRateLimiter(next http.Handler) http.Handler {
	limiter := newIPLimiter(detailRequestsPerMinute, time.Minute)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := s.clientIP(r)
		if !limiter.Allow(ip) {
			_, wait := limiter.Exceeded(ip)
			w.Header().Set("Retry-After", strconv.Itoa(max(1, int(math.Ceil(wait.Seconds())))))
			w.Header().Set("Cache-Control", "no-store")
			writeError(w, http.StatusTooManyRequests, "rate limit exceeded: at most 60 detail requests per minute")
			return
		}
		next.ServeHTTP(w, r)
	})
}
