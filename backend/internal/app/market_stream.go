package app

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/mydisha/keirouter/backend/internal/market"
)

const (
	marketCacheReadMult  = 0.1
	marketCacheWriteMult = 1.25
)

var marketSnapshot atomic.Pointer[[]market.Model]

func setGlobalMarketSnapshot(m []market.Model) { marketSnapshot.Store(&m) }

func globalMarketSnapshot() []market.Model {
	if p := marketSnapshot.Load(); p != nil {
		return *p
	}
	return nil
}

func (a *App) runMarketStream(ctx context.Context) {
	backoff := time.Second
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		s := market.LoadSettings(ctx, a.db.Settings().Get)
		if !s.AutoRefresh {
			select {
			case <-ctx.Done():
				return
			case <-time.After(15 * time.Second):
			}
			continue
		}
		if err := a.consumeMarketStream(ctx); err != nil {
			a.log.Debug("market stream ended", "err", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second
	}
}

func (a *App) consumeMarketStream(ctx context.Context) error {
	url := market.StreamURL
	if a.marketURL != "" {
		url = a.marketURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	client := &http.Client{Timeout: 0}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("market stream status %d", resp.StatusCode)
	}
	return market.ReadStream(resp.Body, func(models []market.Model) error {
		setGlobalMarketSnapshot(models)
		if a.reloadPricing != nil {
			_ = a.reloadPricing(ctx)
		}
		return nil
	})
}
