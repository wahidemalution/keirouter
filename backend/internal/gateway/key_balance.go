package gateway

import (
	"context"
	"time"

	"github.com/mydisha/keirouter/backend/internal/budget"
	"github.com/mydisha/keirouter/backend/internal/store"
)

// keyBalance builds the balance block of GET /v1/keys/me/usage: what a key
// has left, shaped by its kind. A token package block carries no cost keys,
// so the token-key cost strip leaves it intact.
func (s *Server) keyBalance(ctx context.Context, key store.APIKey, now time.Time) (map[string]any, error) {
	tokenState, err := s.tokenKeyState(ctx, key)
	if err != nil {
		return nil, err
	}
	if tokenState.isToken {
		return s.tokenBalance(ctx, key, tokenState, now)
	}
	return s.paygBalance(ctx, key, now)
}

func (s *Server) tokenBalance(ctx context.Context, key store.APIKey, st tokenSubEntry, now time.Time) (map[string]any, error) {
	used, limit, err := s.tokenUsage(ctx, key.ID, now)
	if err != nil {
		return nil, err
	}
	lim, err := s.effectiveLimits(ctx, key)
	if err != nil {
		return nil, err
	}
	active := now.Before(st.expiresAt)
	// Unused tokens are dropped when a lapsed package is renewed, so an
	// expired package has nothing left to spend.
	var remaining int64
	if active && limit > used {
		remaining = limit - used
	}
	expires := ""
	if !st.expiresAt.IsZero() {
		expires = st.expiresAt.UTC().Format(time.RFC3339)
	}
	return map[string]any{
		"type":              "token_package",
		"active":            active,
		"expires_at":        expires,
		"days_remaining":    daysRemaining(st.expiresAt, now),
		"tokens_limit":      limit,
		"tokens_used":       used,
		"tokens_remaining":  remaining,
		"rpm_limit":         lim.RPM,
		"concurrency_limit": lim.Concurrency,
	}, nil
}

// paygBalance reads the key's API-key budget, the same one the portal
// topups balance and the budgets list use.
func (s *Server) paygBalance(ctx context.Context, key store.APIKey, now time.Time) (map[string]any, error) {
	out := map[string]any{
		"type": "payg", "usd_limit": 0.0, "usd_spent": 0.0, "usd_remaining": 0.0, "alert": false,
	}
	budgets, err := s.budgets.ListByScope(ctx, store.ScopeAPIKey, key.ID)
	if err != nil {
		return nil, err
	}
	if len(budgets) == 0 {
		return out, nil
	}
	b := budgets[0]
	spentMicros, _, err := s.usage.SpendAndTokens(ctx, b.ScopeKind, b.ScopeID, budget.PeriodStart(b.Period, now))
	if err != nil {
		return nil, err
	}
	remaining := b.LimitMicros - spentMicros
	if remaining < 0 {
		remaining = 0
	}
	out["usd_limit"] = float64(b.LimitMicros) / 1_000_000
	out["usd_spent"] = float64(spentMicros) / 1_000_000
	out["usd_remaining"] = float64(remaining) / 1_000_000
	out["alert"] = b.AlertPct > 0 && b.LimitMicros > 0 && spentMicros*100 >= b.LimitMicros*int64(b.AlertPct)
	return out, nil
}

// daysRemaining counts started days until expires, 0 once it has passed.
func daysRemaining(expires, now time.Time) int64 {
	if !expires.After(now) {
		return 0
	}
	const day = 24 * time.Hour
	return int64((expires.Sub(now) + day - 1) / day)
}
