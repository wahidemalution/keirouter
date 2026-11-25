package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestChainStepImagePriceRoundTrip(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	now := time.Now().UTC()
	hi := 0.0225
	c := Chain{
		ID: "c-img", TenantID: DefaultTenantID, Name: "img", Strategy: "priority", CreatedAt: now, UpdatedAt: now,
		Steps: []ChainStep{
			{ID: "s-img", ChainID: "c-img", Position: 0, Provider: "openai", Model: "gpt-image-1",
				StaticPerImage: 0.0042, StaticPerImage2K: &hi, CreatedAt: now},
			{ID: "s-txt", ChainID: "c-img", Position: 1, Provider: "openai", Model: "gpt-4o", CreatedAt: now},
		},
	}
	require.NoError(t, db.Chains().Create(ctx, c))

	got, err := db.Chains().Get(ctx, "c-img")
	require.NoError(t, err)
	require.Len(t, got.Steps, 2)
	require.Equal(t, 0.0042, got.Steps[0].StaticPerImage)
	require.NotNil(t, got.Steps[0].StaticPerImage2K)
	require.Equal(t, 0.0225, *got.Steps[0].StaticPerImage2K)
	require.True(t, got.Steps[0].HasImagePrice())
	require.Equal(t, 0.0, got.Steps[1].StaticPerImage)
	require.Nil(t, got.Steps[1].StaticPerImage2K)
	require.False(t, got.Steps[1].HasImagePrice())
}

func TestUsageRecordImageCount(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	require.NoError(t, db.Usage().Record(ctx, UsageRecord{
		ID: "u-img", TenantID: DefaultTenantID, Provider: "openai", Model: "gpt-image-1",
		Status: "success", ImageCount: 3, CreatedAt: time.Now().UTC(),
	}))
	var n int
	require.NoError(t, db.sql.QueryRowContext(ctx, db.rebind(`SELECT image_count FROM usage_records WHERE id = ?`), "u-img").Scan(&n))
	require.Equal(t, 3, n)
}
