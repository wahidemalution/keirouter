package gateway

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/limits"
	"github.com/mydisha/keirouter/backend/internal/store"
)

func i64(n int64) *int64 { return &n }

func setPackageLimits(t *testing.T, s *Server, id string, rpm, conc *int64) {
	t.Helper()
	p, err := s.db.TokenPackages().Get(t.Context(), id)
	require.NoError(t, err)
	p.RPMLimit, p.ConcurrencyLimit = rpm, conc
	require.NoError(t, s.db.TokenPackages().Update(t.Context(), p))
}

func tokenKeyLimits(t *testing.T, s *Server, sub string) limits.EffectiveLimits {
	t.Helper()
	ts, err := s.db.TokenSubscriptions().GetBySub(t.Context(), sub)
	require.NoError(t, err)
	key, err := s.identity.Get(t.Context(), ts.KeyID)
	require.NoError(t, err)
	got, err := s.effectiveLimits(t.Context(), key)
	require.NoError(t, err)
	return got
}

func requireLimits(t *testing.T, got limits.EffectiveLimits, rpm, conc int64) {
	t.Helper()
	require.EqualValues(t, rpm, got.RPM, "rpm")
	require.EqualValues(t, conc, got.Concurrency, "concurrency")
	require.Zero(t, got.TPM, "tpm stays the plan's")
	require.True(t, got.AlwaysEnforce)
}

func requireSubLimits(t *testing.T, s *Server, sub string, rpm, conc *int64) {
	t.Helper()
	ts, err := s.db.TokenSubscriptions().GetBySub(t.Context(), sub)
	require.NoError(t, err)
	require.Equal(t, rpm, ts.RPMLimit, "stored rpm")
	require.Equal(t, conc, ts.ConcurrencyLimit, "stored concurrency")
}

func TestTokenPurchaseRaisesLimitsImmediately(t *testing.T) {
	s, payg := newTokenTestServer(t)
	require.True(t, payTokenOrder(t, s, payg, "o1", "tp-100m"))
	requireLimits(t, tokenKeyLimits(t, s, tokenTestSub), 60, 3) // now cached

	setPackageLimits(t, s, "tp-50m", i64(120), i64(10))
	require.True(t, payTokenOrder(t, s, payg, "o2", "tp-50m"))
	requireLimits(t, tokenKeyLimits(t, s, tokenTestSub), 120, 10)
}

func TestTokenStackKeepsHighestLimits(t *testing.T) {
	s, payg := newTokenTestServer(t)
	setPackageLimits(t, s, "tp-100m", i64(120), i64(10))
	setPackageLimits(t, s, "tp-50m", i64(30), i64(2))

	require.True(t, payTokenOrder(t, s, payg, "o1", "tp-100m"))
	requireLimits(t, tokenKeyLimits(t, s, tokenTestSub), 120, 10)
	require.True(t, payTokenOrder(t, s, payg, "o2", "tp-50m"))
	requireLimits(t, tokenKeyLimits(t, s, tokenTestSub), 120, 10)
	require.True(t, payTokenOrder(t, s, payg, "o3", "tp-days-15"))
	requireLimits(t, tokenKeyLimits(t, s, tokenTestSub), 120, 10)
	requireSubLimits(t, s, tokenTestSub, i64(120), i64(10))
}

func TestTokenNewPeriodResetsLimits(t *testing.T) {
	s, payg := newTokenTestServer(t)
	setPackageLimits(t, s, "tp-100m", i64(120), i64(10))
	setPackageLimits(t, s, "tp-50m", i64(30), i64(2))

	require.True(t, payTokenOrder(t, s, payg, "o1", "tp-100m"))
	requireLimits(t, tokenKeyLimits(t, s, tokenTestSub), 120, 10)
	setTokenExpiry(t, s, time.Now().Add(-time.Hour))
	require.True(t, payTokenOrder(t, s, payg, "o2", "tp-50m"))
	requireLimits(t, tokenKeyLimits(t, s, tokenTestSub), 30, 2)
	requireSubLimits(t, s, tokenTestSub, i64(30), i64(2))
}

func TestTokenPlanFollowingPurchaseRaisesLowLimits(t *testing.T) {
	s, payg := newTokenTestServer(t)
	setPackageLimits(t, s, "tp-50m", i64(30), i64(2))
	require.True(t, payTokenOrder(t, s, payg, "o1", "tp-50m"))
	requireLimits(t, tokenKeyLimits(t, s, tokenTestSub), 30, 2)

	require.True(t, payTokenOrder(t, s, payg, "o2", "tp-100m"))
	requireLimits(t, tokenKeyLimits(t, s, tokenTestSub), 60, 3)
	requireSubLimits(t, s, tokenTestSub, nil, nil)
}

func TestTokenKeyLimitsFallBackToPlan(t *testing.T) {
	s, payg := newTokenTestServer(t)
	require.True(t, payTokenOrder(t, s, payg, "o1", "tp-100m"))
	requireSubLimits(t, s, tokenTestSub, nil, nil)

	plan, err := s.db.Plans().Get(t.Context(), "plan-token-packages")
	require.NoError(t, err)
	plan.RPMLimit, plan.ConcurrencyLimit = 200, 8
	require.NoError(t, s.db.Plans().Update(t.Context(), plan))
	requireLimits(t, tokenKeyLimits(t, s, tokenTestSub), 200, 8)
}

func TestTokenOrderSnapshotsLimits(t *testing.T) {
	s, tok, _ := tokenPortalServer(t)
	ctx := t.Context()
	setPackageLimits(t, s, "tp-100m", i64(120), i64(10))
	o := checkoutTokenOrder(t, s, tok)
	require.Equal(t, i64(120), o.TokenRPMLimit)
	require.Equal(t, i64(10), o.TokenConcurrencyLimit)

	setPackageLimits(t, s, "tp-100m", i64(30), nil)
	applied, err := s.markTokenOrderPaid(ctx, o, store.PaymentCompleted, "webhook", "")
	require.NoError(t, err)
	require.True(t, applied)
	requireSubLimits(t, s, "gsub", i64(120), i64(10))
}
