package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/mydisha/keirouter/backend/internal/store"
)

// spendRollupCheckInterval is how often usage_spend_daily is compared with
// usage_records. Each check is one scan of the usage table.
const spendRollupCheckInterval = time.Hour

// ensureSpendRollup installs or removes the budget spend rollup. A failure is
// logged and leaves spend queries summing usage_records, which is correct,
// only slower.
func ensureSpendRollup(ctx context.Context, db *store.DB, enabled bool, log *slog.Logger) {
	start := time.Now()
	fixed, err := db.EnsureSpendRollup(ctx, enabled)
	switch {
	case err != nil:
		log.Error("budget spend rollup unavailable; summing usage rows instead", "err", err)
	case !enabled:
		log.Info("budget spend rollup disabled; summing usage rows")
	case fixed > 0:
		log.Info("budget spend rollup rebuilt", "groups_fixed", fixed, "took", time.Since(start).Round(time.Millisecond))
	default:
		log.Info("budget spend rollup verified", "took", time.Since(start).Round(time.Millisecond))
	}
}

// runSpendRollupCheck compares the rollup with the usage rows every interval
// and rebuilds it if they differ. A difference means a write path bypassed
// the triggers, so it is logged as an error.
func (a *App) runSpendRollupCheck(ctx context.Context) {
	t := time.NewTicker(spendRollupCheckInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			bad, err := a.db.CheckSpendRollup(ctx)
			if err != nil {
				if ctx.Err() == nil {
					a.log.Error("budget spend rollup check failed", "err", err)
				}
				continue
			}
			if bad > 0 {
				a.log.Error("budget spend rollup differed from usage rows and was rebuilt", "groups_fixed", bad)
			}
		}
	}
}
