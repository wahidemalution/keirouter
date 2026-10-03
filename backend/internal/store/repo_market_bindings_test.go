package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMarketBindingUpsertListDelete(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	b := MarketBinding{TenantID: DefaultTenantID, ProviderID: "ag", ModelID: "claude-opus-4-6-thinking", MarketSlug: "ag/claude-opus-4-6-thinking"}
	require.NoError(t, db.MarketBindings().Upsert(ctx, b))
	b.MarketSlug = "ag/other"
	require.NoError(t, db.MarketBindings().Upsert(ctx, b))
	list, err := db.MarketBindings().List(ctx, DefaultTenantID)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, "ag/other", list[0].MarketSlug)
	require.NoError(t, db.MarketBindings().Delete(ctx, DefaultTenantID, "ag", "claude-opus-4-6-thinking"))
	list, err = db.MarketBindings().List(ctx, DefaultTenantID)
	require.NoError(t, err)
	require.Len(t, list, 0)
}
