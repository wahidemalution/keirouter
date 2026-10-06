package app

import (
	"context"
	"fmt"
	"time"

	"github.com/mydisha/keirouter/backend/internal/market"
	"github.com/mydisha/keirouter/backend/internal/store"
)

const (
	marketCacheReadMult  = market.CacheReadMult
	marketCacheWriteMult = market.CacheWriteMult
)

// MarketCache exposes the live market snapshot for routing decisions.
func (a *App) MarketCache() *market.SnapshotCache { return a.marketCache }

// syncChainMarketPrices recomputes every chain that has market slugs and
// writes the derived rates onto the chain row. Chains whose slugs are all
// absent from the snapshot keep their existing price. Returns the number of
// chains whose price changed and a non-nil error when any write failed.
func (a *App) syncChainMarketPrices(ctx context.Context) (int, error) {
	models, err := market.Fetch(ctx, a.marketURL)
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

	changed := 0
	failed := 0
	for _, c := range chains {
		if len(c.MarketSlugs) == 0 {
			continue
		}
		rate, ok := market.ComputeChainRate(c.MarketSlugs, models, settings.MarkupPercent, marketCacheReadMult, marketCacheWriteMult)
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
			models, err := market.Fetch(ctx, a.marketURL)
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
