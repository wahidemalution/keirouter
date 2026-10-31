package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestUsageRecordRoundTripsReconcileFields(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	require.NoError(t, db.Usage().Record(ctx, UsageRecord{
		ID: "u1", TenantID: DefaultTenantID, Provider: "custom-openai-subrouter", Model: "deepseek-v4.1-flash",
		Status: "success", UpstreamRequestID: "20261031145028472218319QeMWIGBK", ReconcileStatus: "pending",
		CreatedAt: time.Now().UTC(),
	}))

	var id, status string
	q := db.rebind(`SELECT upstream_request_id, reconcile_status FROM usage_records WHERE id = ?`)
	require.NoError(t, db.sql.QueryRowContext(ctx, q, "u1").Scan(&id, &status))
	require.Equal(t, "20261031145028472218319QeMWIGBK", id)
	require.Equal(t, "pending", status)
}
