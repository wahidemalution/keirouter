package app

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/mydisha/keirouter/backend/internal/market"
	"github.com/mydisha/keirouter/backend/internal/store"
)

const (
	marketCacheReadMult  = market.CacheReadMult
	marketCacheWriteMult = market.CacheWriteMult
	// maxSurplusBooksPerSync bounds order-book fetches per sync when the safety
	// margin is on, so a large slug set cannot fan out unboundedly.
	maxSurplusBooksPerSync = 50
)

// MarketCache exposes the live market snapshot for routing decisions.
func (a *App) MarketCache() *market.SnapshotCache { return a.marketCache }

// fetchMergedModels fetches inferhub and Surplus independently and concatenates
// whatever succeeded. A single-source outage leaves the other source usable; if
// both fail an error is returned and the caller keeps the previous snapshot.
func (a *App) fetchMergedModels(ctx context.Context) ([]market.Model, error) {
	var out []market.Model
	var firstErr error

	if models, err := market.Fetch(ctx, a.marketURL); err != nil {
		firstErr = err
	} else {
		out = append(out, models...)
	}
	if models, err := market.FetchSurplus(ctx, a.surplusURL); err != nil {
		a.log.Debug("surplus fetch failed", "err", err)
		if firstErr == nil {
			firstErr = err
		}
	} else {
		out = append(out, models...)
	}

	if len(out) == 0 {
		return nil, firstErr
	}
	return out, nil
}

// enrichSurplusFailover fetches the order book for each Surplus model actually
// bound by a chain and stamps the next-best ask onto the snapshot model, so the
// safety margin can floor against the fallback cost. It is only called when the
// safety margin is enabled, and best-effort: a failed book leaves the model's
// failover unknown (the floor then stays inert for it).
func (a *App) enrichSurplusFailover(ctx context.Context, models []market.Model, slugs map[string]bool) []market.Model {
	if len(slugs) == 0 {
		return models
	}
	bySlug := make(map[string]int, len(models))
	for i, m := range models {
		bySlug[m.Slug] = i
	}
	checked := 0
	for slug := range slugs {
		idx, ok := bySlug[slug]
		if !ok || !strings.HasPrefix(slug, market.SurplusPrefix) {
			continue
		}
		if checked >= maxSurplusBooksPerSync {
			a.log.Warn("safety margin: order-book fetch capped", "cap", maxSurplusBooksPerSync)
			break
		}
		checked++
		name := strings.TrimPrefix(slug, market.SurplusPrefix)
		url := strings.TrimSuffix(a.surplusURL, "/") + "/" + url.PathEscape(name)
		_, _, nextIn, nextOut, found, err := market.FetchSurplusBook(ctx, url)
		if err != nil || !found {
			if err != nil {
				a.log.Debug("surplus book fetch failed", "model", name, "err", err)
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
		rate, ok := market.ComputeChainRateWithSafety(c.MarketSlugs, models, settings.MarkupPercent, marketCacheReadMult, marketCacheWriteMult, settings.SafetyMarginEnabled, settings.SafetyMarginPercent)
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
