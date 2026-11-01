package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestClaimLease(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	s := db.Settings()
	const key, ttl = "lease.test", 5 * time.Minute
	t0 := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

	ok, err := s.ClaimLease(ctx, key, "A", t0, ttl)
	require.NoError(t, err)
	require.True(t, ok, "A claims a free lease")

	ok, err = s.ClaimLease(ctx, key, "B", t0.Add(time.Minute), ttl)
	require.NoError(t, err)
	require.False(t, ok, "B cannot take a live lease")

	ok, err = s.ClaimLease(ctx, key, "A", t0.Add(time.Minute), ttl)
	require.NoError(t, err)
	require.True(t, ok, "A renews its own lease")

	ok, err = s.ClaimLease(ctx, key, "B", t0.Add(4*time.Minute), ttl)
	require.NoError(t, err)
	require.False(t, ok, "the renewal moved the expiry forward")

	ok, err = s.ClaimLease(ctx, key, "B", t0.Add(7*time.Minute), ttl)
	require.NoError(t, err)
	require.True(t, ok, "B takes an expired lease")

	require.NoError(t, s.ReleaseLease(ctx, key, "A"), "releasing a lease you do not hold is a no-op")
	ok, err = s.ClaimLease(ctx, key, "A", t0.Add(8*time.Minute), ttl)
	require.NoError(t, err)
	require.False(t, ok, "B still holds it")

	require.NoError(t, s.ReleaseLease(ctx, key, "B"))
	ok, err = s.ClaimLease(ctx, key, "A", t0.Add(8*time.Minute), ttl)
	require.NoError(t, err)
	require.True(t, ok, "A claims after B released")
}
