package gateway

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/store"
)

func TestBuildModelSettingsMeasuredLatency(t *testing.T) {
	f := newModelSettingsFixture(t, store.Chain{
		ID: "ml1", Name: "measured", Strategy: "priority",
		Steps: []store.ChainStep{
			{ID: "ml1a", Position: 0, Provider: "custom-openai-sharellm", Model: "m1", StaticInputPerM: 0.04, StaticOutputPerM: 0.2},
			{ID: "ml1b", Position: 1, Provider: "custom-openai-sparse", Model: "m1", StaticInputPerM: 0.04, StaticOutputPerM: 0.2},
		},
	})
	ctx := context.Background()
	now := time.Now().UTC()
	record := func(provider, status string, ms int, at time.Time) {
		require.NoError(t, f.srv.usage.Record(ctx, store.UsageRecord{
			ID: uuid.NewString(), TenantID: store.DefaultTenantID, Provider: provider, Model: "m1",
			Status: status, UpstreamLatencyMS: ms, LatencyMS: ms, EndToEndLatencyMS: ms, CreatedAt: at,
		}))
	}
	for _, ms := range []int{1000, 2000, 3000, 4000, 9000} {
		record("custom-openai-sharellm", "success", ms, now.Add(-time.Hour))
	}
	// Failures and requests older than 24h stay out of the median.
	record("custom-openai-sharellm", "failed", 50000, now.Add(-time.Hour))
	record("custom-openai-sharellm", "success", 60000, now.Add(-25*time.Hour))
	// Four requests are too few to show.
	for range 4 {
		record("custom-openai-sparse", "success", 1500, now.Add(-time.Hour))
	}

	chains, err := f.srv.chains.ListByTenant(ctx, store.DefaultTenantID)
	require.NoError(t, err)
	key, err := f.srv.identity.Get(ctx, f.keyID)
	require.NoError(t, err)
	resp, err := f.srv.buildModelSettings(ctx, key, chains)
	require.NoError(t, err)
	providers := resp.Models[0].Providers
	require.Len(t, providers, 2)
	require.NotNil(t, providers[0].LatencyP50MS)
	require.Equal(t, 3000.0, *providers[0].LatencyP50MS)
	require.Nil(t, providers[1].LatencyP50MS)
}
