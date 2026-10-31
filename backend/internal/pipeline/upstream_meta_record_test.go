package pipeline

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/core"
	"github.com/mydisha/keirouter/backend/internal/dispatch"
	"github.com/mydisha/keirouter/backend/internal/meter"
	"github.com/mydisha/keirouter/backend/internal/store"
)

// TestPipelineForwardsUpstreamRequestID proves the attempt's upstream holder
// and the step's cost-lookup flag reach the persisted usage row.
func TestPipelineForwardsUpstreamRequestID(t *testing.T) {
	ctx := context.Background()
	db := newPipelineTestDB(t)
	p := New(Deps{Meter: meter.New(db.Usage(), nil, nil)})

	up := core.NewUpstreamMeta()
	up.SetRequestID("20261031145028472218319QeMWIGBK")
	up.SetStreamed()
	att := dispatch.Attempt{
		Target: dispatch.Target{
			Provider: "custom-openai-subrouter", Model: "deepseek-v4.1-flash", CostLookup: true,
			StaticPrice: true, MarketRateIn: 0.03, MarketRateOut: 0.12, UpstreamRateIn: 0.02, UpstreamRateOut: 0.08,
		},
		Account:  store.Account{ID: "acc-1"},
		Upstream: up,
	}
	p.recordOutcomeWithTTFT(ctx, core.RequestMetadata{TenantID: store.DefaultTenantID}, att,
		core.Usage{PromptTokens: 872, CompletionTokens: 515}, "success", "", false,
		time.Millisecond, time.Millisecond, 0, nil)

	var id, status string
	require.NoError(t, db.SQL().QueryRowContext(ctx,
		`SELECT upstream_request_id, reconcile_status FROM usage_records WHERE tenant_id = ?`,
		store.DefaultTenantID).Scan(&id, &status))
	require.Equal(t, "20261031145028472218319QeMWIGBK", id)
	require.Equal(t, "pending", status)
}
