package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/limits"
	"github.com/mydisha/keirouter/backend/internal/store"
	"github.com/mydisha/keirouter/backend/internal/tokenpkg"
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

func TestAdminTokenPackageLimits(t *testing.T) {
	s, _ := newTokenTestServer(t)
	rec := adminTokenCall(s, http.MethodPost, "/token-packages",
		`{"name":"Big","kind":"tokens","tokens":1000000000,"days":15,"price_idr":570000,"rpm_limit":120,"concurrency_limit":null}`,
		nil, s.adminCreateTokenPackage)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var created map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created))
	require.EqualValues(t, 120, created["rpm_limit"])
	require.Nil(t, created["concurrency_limit"])
	id := created["id"].(string)
	got, err := s.db.TokenPackages().Get(t.Context(), id)
	require.NoError(t, err)
	require.Equal(t, i64(120), got.RPMLimit)
	require.Nil(t, got.ConcurrencyLimit)

	put := func(body string) *httptest.ResponseRecorder {
		return adminTokenCall(s, http.MethodPut, "/token-packages/"+id, body, map[string]string{"id": id}, s.adminUpdateTokenPackage)
	}
	rec = put(`{"name":"Big","kind":"tokens","tokens":1000000000,"days":15,"price_idr":570000,"concurrency_limit":7}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	got, err = s.db.TokenPackages().Get(t.Context(), id)
	require.NoError(t, err)
	require.Equal(t, i64(120), got.RPMLimit, "omitted rpm_limit keeps its value")
	require.Equal(t, i64(7), got.ConcurrencyLimit)

	rec = put(`{"name":"Big","kind":"tokens","tokens":1000000000,"days":15,"price_idr":570000,"rpm_limit":null,"concurrency_limit":null}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	got, err = s.db.TokenPackages().Get(t.Context(), id)
	require.NoError(t, err)
	require.Nil(t, got.RPMLimit, "null clears rpm_limit")
	require.Nil(t, got.ConcurrencyLimit, "null clears concurrency_limit")

	for name, tc := range map[string]struct{ body, msg string }{
		"rpm zero":      {`"rpm_limit":0`, "rpm_limit must be at least 1"},
		"rpm negative":  {`"rpm_limit":-5`, "rpm_limit must be at least 1"},
		"conc zero":     {`"concurrency_limit":0`, "concurrency_limit must be at least 1"},
		"rpm fraction":  {`"rpm_limit":1.5`, ""},
		"rpm as string": {`"rpm_limit":"60"`, ""},
	} {
		rec := adminTokenCall(s, http.MethodPost, "/token-packages",
			`{"name":"x","kind":"tokens","tokens":5,"days":15,"price_idr":1000,`+tc.body+`}`, nil, s.adminCreateTokenPackage)
		require.Equal(t, http.StatusBadRequest, rec.Code, name+": "+rec.Body.String())
		if tc.msg != "" {
			require.Contains(t, rec.Body.String(), tc.msg, name)
		}
	}

	rec = adminTokenCall(s, http.MethodPost, "/token-packages",
		`{"name":"Extra","kind":"days","days":7,"price_idr":9000,"rpm_limit":500,"concurrency_limit":50}`, nil, s.adminCreateTokenPackage)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created))
	days, err := s.db.TokenPackages().Get(t.Context(), created["id"].(string))
	require.NoError(t, err)
	require.Nil(t, days.RPMLimit, "a days package carries no limits")
	require.Nil(t, days.ConcurrencyLimit, "a days package carries no limits")
}

func TestPortalTokenPackagesPerPackageLimits(t *testing.T) {
	s, _, _ := tokenPortalServer(t)
	setPackageLimits(t, s, "tp-20m", i64(120), nil)

	byID := map[string]map[string]any{}
	for _, p := range getTokenPackages(t, s)["packages"].([]any) {
		m := p.(map[string]any)
		byID[m["id"].(string)] = m
	}
	require.EqualValues(t, 120, byID["tp-20m"]["rpm_limit"])
	require.EqualValues(t, 3, byID["tp-20m"]["concurrency_limit"], "unset falls back to the plan")
	require.EqualValues(t, 60, byID["tp-100m"]["rpm_limit"])
	require.EqualValues(t, 3, byID["tp-100m"]["concurrency_limit"])
	require.Nil(t, byID["tp-days-15"]["rpm_limit"], "days packages show no limits")
	require.Nil(t, byID["tp-days-15"]["concurrency_limit"])

	plan, err := s.db.Plans().Get(t.Context(), "plan-token-packages")
	require.NoError(t, err)
	plan.ConcurrencyLimit = 0
	require.NoError(t, s.db.Plans().Update(t.Context(), plan))
	for _, p := range getTokenPackages(t, s)["packages"].([]any) {
		m := p.(map[string]any)
		if m["id"] == "tp-100m" {
			require.Nil(t, m["concurrency_limit"], "an unlimited plan shows no number")
		}
	}
}

func TestPortalTokenOrderReuseNeedsMatchingLimits(t *testing.T) {
	s, tok, calls := tokenPortalServer(t)
	buy := func(idem string) string {
		t.Helper()
		rec := createTokenOrder(t, s, tok, `{"terms_version":"`+tokenpkg.TermsVersion+`","package_id":"tp-100m","idempotency_key":"`+idem+`"}`)
		require.Less(t, rec.Code, 300, rec.Body.String())
		var out map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
		return out["id"].(string)
	}
	first := buy("l1")
	setPackageLimits(t, s, "tp-100m", i64(120), nil)
	second := buy("l2")
	require.NotEqual(t, first, second, "a new rpm limit needs a new order")
	o, err := s.db.PaymentOrders().Get(t.Context(), second)
	require.NoError(t, err)
	require.Equal(t, i64(120), o.TokenRPMLimit)
	require.Equal(t, second, buy("l3"), "unchanged limits reuse the open order")

	setPackageLimits(t, s, "tp-100m", i64(120), i64(4))
	third := buy("l4")
	require.NotEqual(t, second, third, "a new concurrency limit needs a new order")
	setPackageLimits(t, s, "tp-100m", nil, i64(4))
	require.NotEqual(t, third, buy("l5"), "clearing a limit needs a new order")
	require.EqualValues(t, 4, calls.Load())
}

func TestTokenStoredZeroLimitFollowsPlan(t *testing.T) {
	s, payg := newTokenTestServer(t)
	require.True(t, payTokenOrder(t, s, payg, "o1", "tp-100m"))
	tx, err := s.db.SQL().BeginTx(t.Context(), nil)
	require.NoError(t, err)
	require.NoError(t, s.db.TokenSubscriptions().SetLimitsOnTx(t.Context(), tx, tokenTestSub, i64(0), i64(-1)))
	require.NoError(t, tx.Commit())
	ts, err := s.db.TokenSubscriptions().GetBySub(t.Context(), tokenTestSub)
	require.NoError(t, err)
	s.invalidateTokenSub(ts.KeyID)
	requireLimits(t, tokenKeyLimits(t, s, tokenTestSub), 60, 3)
}
