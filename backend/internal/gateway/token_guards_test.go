package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/store"
	"github.com/mydisha/keirouter/backend/internal/tokenpkg"
)

// buyPortalTokenKey pays a tp-100m order for the portal user "gsub" and
// returns the resulting subscription and the token key's plaintext.
func buyPortalTokenKey(t *testing.T, s *Server, tok string) (store.TokenSubscription, string) {
	t.Helper()
	ctx := t.Context()
	rec := createTokenOrder(t, s, tok, `{"terms_version":"`+tokenpkg.TermsVersion+`","package_id":"tp-100m","idempotency_key":"buy-1"}`)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var created map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created))
	o, err := s.db.PaymentOrders().Get(ctx, created["id"].(string))
	require.NoError(t, err)
	applied, err := s.markTokenOrderPaid(ctx, o, store.PaymentCompleted, "webhook", "")
	require.NoError(t, err)
	require.True(t, applied)
	sub, err := s.db.TokenSubscriptions().GetBySub(ctx, "gsub")
	require.NoError(t, err)
	plain, err := s.vault.Sealer().OpenString(sub.SealedKey)
	require.NoError(t, err)
	return sub, plain
}

func TestAdminTopupRefusesTokenKey(t *testing.T) {
	s, paygKey := newTokenTestServer(t)
	s.settings = s.db.Settings()
	require.True(t, payTokenOrder(t, s, paygKey, "t1", "tp-100m"))
	sub, err := s.db.TokenSubscriptions().GetBySub(t.Context(), tokenTestSub)
	require.NoError(t, err)

	rec := adminTokenCall(s, http.MethodPost, "/keys/"+sub.KeyID+"/topup",
		`{"amount_usd":5,"reason":"x","idempotency_key":"i1"}`, map[string]string{"id": sub.KeyID}, s.adminTopupKey)
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "credit cannot be added to a token package key")
	require.Zero(t, tokenBudget(t, s, sub.KeyID).LimitMicros)
	topups, err := s.db.Topups().ListByKey(t.Context(), sub.KeyID)
	require.NoError(t, err)
	require.Empty(t, topups)
}

func TestAdminLimitAdjustmentRefusesTokenKey(t *testing.T) {
	s, paygKey := newTokenTestServer(t)
	s.settings = s.db.Settings()
	require.True(t, payTokenOrder(t, s, paygKey, "t1", "tp-100m"))
	sub, err := s.db.TokenSubscriptions().GetBySub(t.Context(), tokenTestSub)
	require.NoError(t, err)

	w := httptest.NewRecorder()
	s.adminAdjustKeyLimit(w, adjustReq(sub.KeyID, `{"limit_usd":10,"reason":"x","idempotency_key":"a1"}`))
	require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), "credit cannot be added to a token package key")
	require.Zero(t, tokenBudget(t, s, sub.KeyID).LimitMicros)
	adjs, err := s.db.LimitAdjustments().ListByKey(t.Context(), sub.KeyID)
	require.NoError(t, err)
	require.Empty(t, adjs)
}

func TestPortalClaimRejectsTokenKey(t *testing.T) {
	s, tok, _ := tokenPortalServer(t)
	ctx := t.Context()
	sub, plain := buyPortalTokenKey(t, s, tok)

	other, err := s.auth.IssuePortalSession("portal:sub-other", "o@example.com")
	require.NoError(t, err)
	rec := httptest.NewRecorder()
	s.handlePortalClaim(rec, portalReq(http.MethodPost, "/portal/auth/claim", `{"api_key":"`+plain+`"}`, other))
	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	_, err = s.db.PortalUsers().GetBySub(ctx, "sub-other")
	require.ErrorIs(t, err, store.ErrNotFound)

	key, err := s.identity.Get(ctx, sub.KeyID)
	require.NoError(t, err)
	require.Equal(t, "token:u@example.com", key.Name, "claim must not rename the token key")
}

func TestAdminDeletePortalUserDeletesTokenKey(t *testing.T) {
	s, tok, _ := tokenPortalServer(t)
	ctx := t.Context()
	sub, plain := buyPortalTokenKey(t, s, tok)
	rec, _ := callAuth(s, plain)
	require.Equal(t, http.StatusOK, rec.Code, "warm the auth and token caches")

	del := httptest.NewRecorder()
	s.adminDeletePortalUser(del, withChiParam(httptest.NewRequest(http.MethodDelete, "/api/portal-users/gsub", nil), "gsub"))
	require.Equal(t, http.StatusNoContent, del.Code, del.Body.String())

	_, err := s.identity.Get(ctx, sub.KeyID)
	require.ErrorIs(t, err, store.ErrNotFound)
	_, err = s.db.TokenSubscriptions().GetBySub(ctx, "gsub")
	require.ErrorIs(t, err, store.ErrNotFound)
	rec, called := callAuth(s, plain)
	require.Equal(t, http.StatusUnauthorized, rec.Code, rec.Body.String())
	require.False(t, called)
}

func TestTokenPlanKeyWithoutSubscriptionRejected(t *testing.T) {
	s, _ := newTokenTestServer(t)
	ctx := t.Context()
	issued, err := s.identity.Generate(store.DefaultTenantID, "", "orphan")
	require.NoError(t, err)
	issued.Record.PlanID = tokenpkg.DefaultPlanID
	require.NoError(t, s.identity.Keys().Create(ctx, issued.Record))

	rec, called := callAuth(s, issued.Plaintext)
	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "token_package_expired")
	require.False(t, called)
}

func TestPortalTokenOrderRefusesDisabledKey(t *testing.T) {
	s, tok, calls := tokenPortalServer(t)
	sub, _ := buyPortalTokenKey(t, s, tok)
	require.NoError(t, s.identity.SetDisabled(t.Context(), sub.KeyID, true))
	before := calls.Load()

	for _, pkg := range []string{"tp-20m", "tp-days-15"} {
		rec := createTokenOrder(t, s, tok, `{"terms_version":"`+tokenpkg.TermsVersion+`","package_id":"`+pkg+`","idempotency_key":"d-`+pkg+`"}`)
		require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
		require.Contains(t, rec.Body.String(), "token_key_disabled")
	}
	require.Equal(t, before, calls.Load(), "no payment is created")

	st := tokenStatus(t, s, tok)
	require.Equal(t, true, st["disabled"])
	require.NoError(t, s.identity.SetDisabled(t.Context(), sub.KeyID, false))
	require.Equal(t, false, tokenStatus(t, s, tok)["disabled"])
}

func TestPortalTokenStatusNotDisabledWithoutKey(t *testing.T) {
	s, tok, _ := tokenPortalServer(t)
	require.Equal(t, false, tokenStatus(t, s, tok)["disabled"])
}

// The token plan and the token key share one source for allowed models.
func TestTokenKeyModelsFollowPlan(t *testing.T) {
	s, paygKey := newTokenTestServer(t)
	ctx := t.Context()
	require.True(t, payTokenOrder(t, s, paygKey, "t1", "tp-100m"))
	sub, err := s.db.TokenSubscriptions().GetBySub(ctx, tokenTestSub)
	require.NoError(t, err)
	key, err := s.identity.Get(ctx, sub.KeyID)
	require.NoError(t, err)
	own, err := s.identity.Keys().GetAllowedModels(ctx, key.ID)
	require.NoError(t, err)
	require.Empty(t, own, "token keys carry no per-key model override")

	rec := adminTokenCall(s, http.MethodPut, "/token-packages/settings",
		`{"allowed_models":["gpt-6-luna"]}`, nil, s.adminPutTokenSettings)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	models, source, err := s.identity.Keys().EffectiveAllowedModels(ctx, key.ID, key.PlanID)
	require.NoError(t, err)
	require.Equal(t, "plan", source)
	require.Equal(t, []string{"gpt-6-luna"}, models)

	rec = adminTokenCall(s, http.MethodGet, "/token-packages/settings", "", nil, s.adminGetTokenSettings)
	require.True(t, strings.Contains(rec.Body.String(), `"allowed_models":["gpt-6-luna"]`), rec.Body.String())
}
