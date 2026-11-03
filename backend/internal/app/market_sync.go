package app

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/mydisha/keirouter/backend/internal/market"
	"github.com/mydisha/keirouter/backend/internal/store"
)

const (
	marketCacheReadMult  = market.CacheReadMult
	marketCacheWriteMult = market.CacheWriteMult
	// maxSurplusBooksPerSync bounds the distinct order books one sync fetches,
	// so a large slug set cannot fan out unboundedly.
	maxSurplusBooksPerSync = 50
)

// MarketCache exposes the live market snapshot for routing decisions.
func (a *App) MarketCache() *market.SnapshotCache { return a.marketCache }

// fetchMergedModels fetches inferhub and Surplus independently and concatenates
// whatever succeeded. A single-source outage leaves the other source usable; if
// both fail an error is returned and the caller keeps the previous snapshot.
func (a *App) fetchMergedModels(ctx context.Context) ([]market.Model, error) {
	// The sources are independent, so fetch them concurrently: a sync then
	// takes as long as the slowest source instead of their sum.
	var (
		wg                 sync.WaitGroup
		inferhub, surplus  []market.Model
		trusted, fast      []market.Model
		inferErr, surplErr error
	)
	wg.Add(3)
	go func() {
		defer wg.Done()
		inferhub, inferErr = market.Fetch(ctx, a.marketURL)
	}()
	go func() {
		defer wg.Done()
		surplus, surplErr = market.FetchSurplus(ctx, a.surplusURL)
	}()
	go func() {
		defer wg.Done()
		trustedBound := a.boundTrustedSurplusModels(ctx, market.SurplusTrustedPrefix)
		fastBound := a.boundTrustedSurplusModels(ctx, market.SurplusFastPrefix)
		names := make([]string, 0, len(trustedBound)+len(fastBound))
		for _, b := range trustedBound {
			names = append(names, b.name)
		}
		for _, b := range fastBound {
			names = append(names, b.name)
		}
		books := a.fetchSurplusBooks(ctx, names)
		trusted = a.fetchSurplusTrusted(ctx, trustedBound, books)
		fast = a.fetchSurplusFast(ctx, fastBound, books)
	}()
	wg.Wait()

	if surplErr != nil {
		a.log.Debug("surplus fetch failed", "err", surplErr)
	}
	out := make([]market.Model, 0, len(inferhub)+len(surplus)+len(trusted)+len(fast))
	out = append(out, inferhub...)
	out = append(out, surplus...)
	out = append(out, trusted...)
	out = append(out, fast...)
	if len(out) == 0 {
		if inferErr != nil {
			return nil, inferErr
		}
		return nil, surplErr
	}
	return out, nil
}

// trustedBinding is one surplus-trusted or surplus-fast model referenced by
// the chains, with the /min{N} floor of the provider base URL serving it.
// provider is the pinned Surplus provider of a surplus-trusted@ step, "" for
// the plain forms.
type trustedBinding struct {
	name     string
	provider string
	floorPct int
}

// boundTrustedSurplusModels loads the tenant's chains and custom provider
// floors and binds them with market.BindTrustedSurplus.
func (a *App) boundTrustedSurplusModels(ctx context.Context, prefix string) []trustedBinding {
	chains, err := a.db.Chains().ListByTenant(ctx, store.DefaultTenantID)
	if err != nil {
		a.log.Debug("surplus trusted: list chains failed", "err", err)
		return nil
	}
	floors := map[string]int{}
	if providers, err := a.db.CustomProviders().ListProviders(ctx, store.DefaultTenantID); err == nil {
		for _, p := range providers {
			floors[p.ID] = market.MinDiscountFloor(p.BaseURL)
		}
	} else {
		a.log.Debug("surplus trusted: list custom providers failed", "err", err)
	}
	var out []trustedBinding
	for _, b := range market.BindTrustedSurplus(chainRoutes(chains), prefix, floors) {
		out = append(out, trustedBinding{name: b.Name, provider: b.Provider, floorPct: b.FloorPct})
	}
	return out
}

// chainRoutes keeps only the chain fields Surplus binding reads. Its twin is
// gateway.detailChainRoute; keep the two in step.
func chainRoutes(chains []store.Chain) []market.ChainRoute {
	routes := make([]market.ChainRoute, len(chains))
	for i, c := range chains {
		steps := make([]market.StepRoute, len(c.Steps))
		for j, st := range c.Steps {
			steps[j] = market.StepRoute{MarketSlug: st.MarketSlug, Model: st.Model, Provider: st.Provider}
		}
		routes[i] = market.ChainRoute{MarketSlugs: c.MarketSlugs, Steps: steps}
	}
	return routes
}

// fetchSurplusBooks downloads each distinct model's order book once, at most
// maxSurplusBooksPerSync of them, 8 at a time. A failed fetch leaves the model
// out of the result. The result replaces a.surplusBooks so the failover
// enrichment of the same run can reuse it.
func (a *App) fetchSurplusBooks(ctx context.Context, models []string) map[string][]byte {
	seen := make(map[string]bool, len(models))
	var distinct []string
	for _, m := range models {
		if !seen[m] {
			seen[m] = true
			distinct = append(distinct, m)
		}
	}
	if len(distinct) > maxSurplusBooksPerSync {
		a.log.Warn("surplus: order-book fetch capped", "cap", maxSurplusBooksPerSync, "bound", len(distinct))
		distinct = distinct[:maxSurplusBooksPerSync]
	}
	bodies := make([][]byte, len(distinct))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for i, name := range distinct {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			body, err := market.FetchSurplusBookRaw(ctx, a.surplusURL, name)
			if err != nil {
				a.log.Debug("surplus book fetch failed", "model", name, "err", err)
				return
			}
			bodies[i] = body
		}()
	}
	wg.Wait()
	books := make(map[string][]byte, len(distinct))
	for i, name := range distinct {
		if bodies[i] != nil {
			books[name] = bodies[i]
		}
	}
	a.surplusBooksMu.Lock()
	a.surplusBooks = books
	a.surplusBooksMu.Unlock()
	return books
}

// fetchSurplusTrusted quotes every bound surplus-trusted model from its order
// book, using only trusted+available+healthy offers that also clear the
// provider's /min{N} floor. A provider binding quotes only that Surplus
// provider's offers. A missing or empty book drops that slug from the
// snapshot so routing never prices against stale trusted offers.
func (a *App) fetchSurplusTrusted(ctx context.Context, bound []trustedBinding, books map[string][]byte) []market.Model {
	if len(bound) == 0 {
		return nil
	}
	settings := market.LoadSettings(ctx, a.db.Settings().Get)
	depth, blocked := settings.SurplusFallbackDepth, settings.SurplusBlockedProviders
	ceiling := depth > 0
	if !ceiling {
		depth = 1
	}
	var models []market.Model
	for _, b := range bound {
		book, ok := books[b.name]
		if !ok {
			continue
		}
		q, err := market.ParseSurplusTrustedBookFor(bytes.NewReader(book), depth, b.floorPct, blocked, b.provider)
		if err != nil || !q.Found {
			if err != nil {
				a.log.Debug("surplus trusted book parse failed", "model", b.name, "provider", b.provider, "err", err)
			}
			continue
		}
		if !ceiling {
			q.MinDiscountPct = 0
			if q.FloorCapIn > 0 && q.FloorCapOut > 0 {
				// Without a ceiling Surplus may fail over to any offer
				// clearing the floor; bill the unreported case at that bound.
				q.CapIn, q.CapOut = q.FloorCapIn, q.FloorCapOut
			}
		}
		if b.provider != "" {
			models = append(models, q.ProviderModel(b.provider, b.name))
		} else {
			models = append(models, q.Model(b.name))
		}
	}
	return models
}

// fetchSurplusFast quotes every bound surplus-fast model: available and
// healthy offers of any trust within the configured price tolerance, with
// providers ranked by published TTFT. Performance is fetched per model (a
// failed fetch ranks by price); the book comes from books. As with trusted
// quotes, a missing or empty book drops the slug so routing never uses stale
// offers.
func (a *App) fetchSurplusFast(ctx context.Context, bound []trustedBinding, books map[string][]byte) []market.Model {
	if len(bound) == 0 {
		return nil
	}
	settings := market.LoadSettings(ctx, a.db.Settings().Get)
	tolerance, depth, blocked := settings.SurplusFastTolerancePct, settings.SurplusFallbackDepth, settings.SurplusBlockedProviders
	out := make([]market.Model, len(bound))
	ok := make([]bool, len(bound))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for i, b := range bound {
		book, found := books[b.name]
		if !found {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			perf, err := market.FetchSurplusPerformance(ctx, a.surplusURL, b.name)
			if err != nil {
				perf = nil
			}
			q, err := market.ParseSurplusFastBook(bytes.NewReader(book), perf, tolerance, depth, b.floorPct, blocked)
			if err != nil || !q.Found {
				if err != nil {
					a.log.Debug("surplus fast book parse failed", "model", b.name, "err", err)
				}
				return
			}
			out[i], ok[i] = q.FastModel(b.name), true
		}()
	}
	wg.Wait()
	models := out[:0]
	for i := range out {
		if ok[i] {
			models = append(models, out[i])
		}
	}
	return models
}

// enrichSurplusFailover stamps the next-best ask of each Surplus model actually
// bound by a chain onto the snapshot model, so the safety margin can floor
// against the fallback cost. It reads the books this sync already fetched and
// downloads only the missing ones, at most maxSurplusBooksPerSync. It is only
// called when the safety margin is enabled, and best-effort: a failed book
// leaves the model's failover unknown (the floor then stays inert for it).
func (a *App) enrichSurplusFailover(ctx context.Context, models []market.Model, slugs map[string]bool) []market.Model {
	if len(slugs) == 0 {
		return models
	}
	bySlug := make(map[string]int, len(models))
	for i, m := range models {
		bySlug[m.Slug] = i
	}
	a.surplusBooksMu.Lock()
	shared := a.surplusBooks
	a.surplusBooksMu.Unlock()
	fetched, capped := 0, false
	for slug := range slugs {
		idx, ok := bySlug[slug]
		if !ok || !strings.HasPrefix(slug, market.SurplusPrefix) {
			continue
		}
		name := strings.TrimPrefix(slug, market.SurplusPrefix)
		book, ok := shared[name]
		if !ok {
			if fetched >= maxSurplusBooksPerSync {
				if !capped {
					a.log.Warn("safety margin: order-book fetch capped", "cap", maxSurplusBooksPerSync)
					capped = true
				}
				continue
			}
			fetched++
			var err error
			if book, err = market.FetchSurplusBookRaw(ctx, a.surplusURL, name); err != nil {
				a.log.Debug("surplus book fetch failed", "model", name, "err", err)
				continue
			}
		}
		_, _, nextIn, nextOut, found, err := market.ParseSurplusBook(bytes.NewReader(book))
		if err != nil || !found {
			if err != nil {
				a.log.Debug("surplus book parse failed", "model", name, "err", err)
			}
			continue
		}
		models[idx].FailoverIn = nextIn
		models[idx].FailoverOut = nextOut
	}
	return models
}

// syncChainMarketPrices recomputes every chain that has market slugs and
// writes the derived rates onto the chain row. Chains whose slugs are all
// absent from the snapshot keep their existing price. Returns the number of
// chains whose price changed and a non-nil error when any write failed.
func (a *App) syncChainMarketPrices(ctx context.Context) (int, error) {
	models, err := a.fetchMergedModels(ctx)
	if err != nil {
		return 0, err
	}
	a.marketCache.Replace(models)
	settings := market.LoadSettings(ctx, a.db.Settings().Get)
	return a.syncChainMarketPricesWithModels(ctx, models, settings)
}

// syncChainMarketPricesWithModels applies an already-fetched snapshot to the
// stored chains, writing derived rates for any chain whose price changed.
func (a *App) syncChainMarketPricesWithModels(ctx context.Context, models []market.Model, settings market.Settings) (int, error) {
	chains, err := a.db.Chains().ListByTenant(ctx, store.DefaultTenantID)
	if err != nil {
		return 0, err
	}

	if settings.SafetyMarginEnabled {
		bound := make(map[string]bool)
		for _, c := range chains {
			for _, slug := range c.MarketSlugs {
				bound[slug] = true
			}
		}
		models = a.enrichSurplusFailover(ctx, models, bound)
	}

	changed := 0
	failed := 0
	for _, c := range chains {
		if len(c.MarketSlugs) == 0 {
			continue
		}
		rate, ok := market.ComputeChainRateWithSafety(c.MarketSlugs, models, market.EffectiveMarkup(c.MarkupPercent, settings.MarkupPercent), marketCacheReadMult, marketCacheWriteMult, settings.SafetyMarginEnabled, settings.SafetyMarginPercent)
		if !ok {
			continue
		}
		if c.InputPerM == rate.InputPerM && c.OutputPerM == rate.OutputPerM &&
			c.CacheWritePerM == rate.CacheWritePerM && c.CacheReadPerM == rate.CachedInputPerM {
			continue
		}
		if err := a.db.Chains().UpdateRates(ctx, c.ID, rate.InputPerM, rate.OutputPerM, rate.CacheWritePerM, rate.CachedInputPerM); err != nil {
			a.log.Warn("market price write failed", "chain", c.Name, "err", err)
			failed++
			continue
		}
		changed++
	}
	if failed > 0 {
		return changed, fmt.Errorf("%d chain price writes failed", failed)
	}
	return changed, nil
}

// runMarketSync polls the market feed on the configured interval. The live
// snapshot cache is warmed on every successful fetch regardless of the
// AutoRefresh setting; AutoRefresh only gates whether derived rates are
// written back to the chain rows.
func (a *App) runMarketSync(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var lastRun time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			settings := market.LoadSettings(ctx, a.db.Settings().Get)
			interval := time.Duration(settings.RefreshIntervalSeconds) * time.Second
			if time.Since(lastRun) < interval {
				continue
			}
			lastRun = time.Now()
			models, err := a.fetchMergedModels(ctx)
			if err != nil {
				a.log.Debug("market fetch failed", "err", err)
				continue
			}
			a.marketCache.Replace(models)
			if !settings.AutoRefresh {
				continue
			}
			if n, err := a.syncChainMarketPricesWithModels(ctx, models, settings); err != nil {
				settings.LastFetchError = err.Error()
				_ = market.SaveSettings(ctx, a.db.Settings().Set, settings)
				a.log.Debug("market sync failed", "err", err)
			} else {
				settings.LastFetchedAt = time.Now().UTC().Format(time.RFC3339)
				settings.LastFetchError = ""
				settings.LastSyncedCount = n
				_ = market.SaveSettings(ctx, a.db.Settings().Set, settings)
			}
		}
	}
}
