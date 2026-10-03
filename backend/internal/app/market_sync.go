package app

import (
	"context"
	"time"

	"github.com/mydisha/keirouter/backend/internal/market"
	"github.com/mydisha/keirouter/backend/internal/store"
)

// syncChainMarketPrices recomputes every chain that has market slugs and
// writes the derived rates onto the chain row. Chains whose slugs are all
// absent from the snapshot keep their existing price. Returns the number of
// chains whose price changed.
func (a *App) syncChainMarketPrices(ctx context.Context) (int, error) {
	models, err := market.Fetch(ctx, a.marketURL)
	if err != nil {
		return 0, err
	}
	settings := market.LoadSettings(ctx, a.db.Settings().Get)
	chains, err := a.db.Chains().ListByTenant(ctx, store.DefaultTenantID)
	if err != nil {
		return 0, err
	}

	changed := 0
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
		c.InputPerM = rate.InputPerM
		c.OutputPerM = rate.OutputPerM
		c.CacheWritePerM = rate.CacheWritePerM
		c.CacheReadPerM = rate.CachedInputPerM
		if err := a.db.Chains().Update(ctx, c); err != nil {
			a.log.Warn("market price write failed", "chain", c.Name, "err", err)
			continue
		}
		changed++
	}
	return changed, nil
}

// runMarketSync polls the market feed and refreshes chain prices on the
// configured interval while auto-refresh is enabled.
func (a *App) runMarketSync(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	var lastRun time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			settings := market.LoadSettings(ctx, a.db.Settings().Get)
			if !settings.AutoRefresh {
				continue
			}
			interval := time.Duration(settings.RefreshIntervalMinutes) * time.Minute
			if time.Since(lastRun) < interval {
				continue
			}
			lastRun = time.Now()
			if n, err := a.syncChainMarketPrices(ctx); err != nil {
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
