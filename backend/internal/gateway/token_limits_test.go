package gateway

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/tokenpkg"
)

func TestTokenKeyGetsPlanLimits(t *testing.T) {
	s, paygKey := newTokenTestServer(t)
	ctx := t.Context()
	require.True(t, payTokenOrder(t, s, paygKey, "t1", "tp-100m"))

	sub, err := s.db.TokenSubscriptions().GetBySub(ctx, tokenTestSub)
	require.NoError(t, err)
	key, err := s.identity.Get(ctx, sub.KeyID)
	require.NoError(t, err)
	require.Equal(t, tokenpkg.DefaultPlanID, key.PlanID)

	got, err := s.effectiveLimits(ctx, key)
	require.NoError(t, err)
	require.EqualValues(t, 60, got.RPM)
	require.EqualValues(t, 3, got.Concurrency)
	require.Zero(t, got.TPM)

	plan, err := s.db.Plans().Get(ctx, tokenpkg.DefaultPlanID)
	require.NoError(t, err)
	plan.RPMLimit = 10
	require.NoError(t, s.db.Plans().Update(ctx, plan))
	got, err = s.effectiveLimits(ctx, key)
	require.NoError(t, err)
	require.EqualValues(t, 10, got.RPM)
}

func TestPortalPlansHidesTokenPlan(t *testing.T) {
	s, _ := newTokenTestServer(t)
	rec := httptest.NewRecorder()
	s.handlePortalPlans(rec, httptest.NewRequest(http.MethodGet, "/portal/plans", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotContains(t, rec.Body.String(), tokenpkg.DefaultPlanID)
	require.Contains(t, rec.Body.String(), `"default"`, "other plans stay listed")
}

// A token key whose plan id was cleared is still a token key: it keeps the
// token plan's limits, enforced regardless of the global switch.
func TestTokenKeyWithoutPlanIDStaysLimited(t *testing.T) {
	s, paygKey := newTokenTestServer(t)
	ctx := t.Context()
	require.True(t, payTokenOrder(t, s, paygKey, "t1", "tp-100m"))
	sub, err := s.db.TokenSubscriptions().GetBySub(ctx, tokenTestSub)
	require.NoError(t, err)
	key, err := s.identity.Get(ctx, sub.KeyID)
	require.NoError(t, err)
	key.PlanID = ""

	got, err := s.effectiveLimits(ctx, key)
	require.NoError(t, err)
	require.True(t, got.AlwaysEnforce)
	require.EqualValues(t, 60, got.RPM)
	require.EqualValues(t, 3, got.Concurrency)
}

func TestPAYGKeyWithoutPlanGetsDefaultLimits(t *testing.T) {
	s, paygKey := newTokenTestServer(t)
	ctx := t.Context()
	key, err := s.identity.Get(ctx, paygKey)
	require.NoError(t, err)
	key.PlanID = ""

	got, err := s.effectiveLimits(ctx, key)
	require.NoError(t, err)
	require.False(t, got.AlwaysEnforce)
	require.Equal(t, s.cfg.Limits.DefaultRPM, got.RPM)
}
