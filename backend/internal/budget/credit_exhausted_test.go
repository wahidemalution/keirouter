package budget

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/core"
	"github.com/mydisha/keirouter/backend/internal/store"
)

func blockedPE(t *testing.T, err error) *core.ProviderError {
	t.Helper()
	var pe *core.ProviderError
	require.ErrorAs(t, err, &pe)
	require.Equal(t, core.ErrBudgetBlocked, pe.Kind)
	return pe
}

// Only an API key's prepaid credit (a total-period key budget) running out
// tells the client to top up. Spend caps keep the generic budget message.
func TestBudgetBlockMarksCreditsExhausted(t *testing.T) {
	ctx := context.Background()

	t.Run("credit used up", func(t *testing.T) {
		eng, db := newTestEngine(t)
		scope := keyBudget(t, db, "key-empty", 1_000_000)
		recordSpend(t, db, "key-empty", 1_000_000)
		_, err := reserveMicros(eng, ctx, scope, 1)
		require.True(t, blockedPE(t, err).CreditsExhausted)
		require.True(t, blockedPE(t, eng.CheckOrError(ctx, scope)).CreditsExhausted)
	})

	t.Run("below minimum balance", func(t *testing.T) {
		eng, db := newTestEngine(t)
		scope := keyBudget(t, db, "key-low", 1_000_000)
		recordSpend(t, db, "key-low", 990_000)
		_, err := reserveMicros(eng, ctx, scope, 1)
		require.True(t, blockedPE(t, err).CreditsExhausted)
	})

	t.Run("estimate above credit", func(t *testing.T) {
		eng, db := newTestEngine(t)
		scope := keyBudget(t, db, "key-short", 1_000_000)
		_, err := reserveMicros(eng, ctx, scope, 2_000_000)
		require.True(t, blockedPE(t, err).CreditsExhausted)
	})

	t.Run("daily key cap is not credit", func(t *testing.T) {
		eng, db := newTestEngine(t)
		createBudget(t, db, store.Budget{
			ID: "b-daily", ScopeKind: store.ScopeAPIKey, ScopeID: "key-daily",
			LimitMicros: 1_000_000, Period: "daily", HardCutoff: true,
		})
		recordSpend(t, db, "key-daily", 1_000_000)
		scope := Scope{TenantID: store.DefaultTenantID, APIKeyID: "key-daily"}
		_, err := reserveMicros(eng, ctx, scope, 1)
		require.False(t, blockedPE(t, err).CreditsExhausted)
	})

	t.Run("token cap is not credit", func(t *testing.T) {
		eng, db := newTestEngine(t)
		createBudget(t, db, store.Budget{
			ID: "b-tokens", ScopeKind: store.ScopeAPIKey, ScopeID: "key-tokens",
			LimitTokens: 1, HardCutoff: true,
		})
		scope := Scope{TenantID: store.DefaultTenantID, APIKeyID: "key-tokens"}
		require.NoError(t, db.Usage().Record(ctx, store.UsageRecord{
			ID: "u-tokens", TenantID: store.DefaultTenantID, APIKeyID: "key-tokens",
			Provider: "openai", Model: "gpt-4o", Status: "success", PromptTokens: 10, CreatedAt: time.Now().UTC(),
		}))
		_, err := reserveMicros(eng, ctx, scope, 1)
		require.False(t, blockedPE(t, err).CreditsExhausted)
	})
}
