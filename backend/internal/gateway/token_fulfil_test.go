package gateway

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/budget"
	"github.com/mydisha/keirouter/backend/internal/crypto"
	"github.com/mydisha/keirouter/backend/internal/store"
	"github.com/mydisha/keirouter/backend/internal/tokenpkg"
	"github.com/mydisha/keirouter/backend/internal/vault"
)

const tokenTestSub = "sub-token"

// newTokenTestServer returns a payment test server with a vault, a budget
// engine and a portal user owning a PAYG key with 5 USD of credit.
func newTokenTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	s := newPaymentTestServer(t)
	mk, err := crypto.GenerateMasterKey()
	require.NoError(t, err)
	sealer, err := crypto.NewSealer(mk)
	require.NoError(t, err)
	s.vault = vault.New(sealer)
	s.budgetEngine = budget.New(s.db.Budgets(), s.db.Usage())

	ctx := t.Context()
	payg, err := s.identity.Create(ctx, store.DefaultTenantID, "", "portal:u@example.com")
	require.NoError(t, err)
	now := time.Now()
	require.NoError(t, s.budgets.Create(ctx, store.Budget{
		ID: "b-payg", TenantID: store.DefaultTenantID, ScopeKind: store.ScopeAPIKey, ScopeID: payg.Record.ID,
		LimitMicros: 5_000_000, Period: "total", AlertPct: 80, HardCutoff: true, CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, s.db.PortalUsers().Upsert(ctx, store.PortalUser{
		GoogleSub: tokenTestSub, Email: "u@example.com", KeyID: payg.Record.ID, CreatedAt: now, UpdatedAt: now,
	}))
	return s, payg.Record.ID
}

func makeTokenOrder(t *testing.T, s *Server, paygKeyID, id, pkgID string) store.PaymentOrder {
	t.Helper()
	o := store.PaymentOrder{
		ID: id, TenantID: store.DefaultTenantID, KeyID: paygKeyID, GoogleSub: tokenTestSub,
		AmountIDR: 259000, Status: store.PaymentPending, Provider: "sumopod", ProviderPaymentID: "pay-" + id,
		PaymentLinkURL: "https://pay/" + id, CreatedAt: time.Now(), UpdatedAt: time.Now(),
		Product: store.ProductToken, TokenPackageID: pkgID,
	}
	require.NoError(t, s.db.PaymentOrders().Create(t.Context(), o))
	return o
}

func payTokenOrder(t *testing.T, s *Server, paygKeyID, id, pkgID string) bool {
	t.Helper()
	o := makeTokenOrder(t, s, paygKeyID, id, pkgID)
	applied, err := s.markTokenOrderPaid(t.Context(), o, store.PaymentCompleted, "webhook", "")
	require.NoError(t, err)
	return applied
}

func tokenBudget(t *testing.T, s *Server, keyID string) store.Budget {
	t.Helper()
	bs, err := s.budgets.ListByScope(t.Context(), store.ScopeAPIKey, keyID)
	require.NoError(t, err)
	require.Len(t, bs, 1)
	return bs[0]
}

func setTokenExpiry(t *testing.T, s *Server, at time.Time) {
	t.Helper()
	tx, err := s.db.SQL().BeginTx(t.Context(), nil)
	require.NoError(t, err)
	require.NoError(t, s.db.TokenSubscriptions().SetExpiresOnTx(t.Context(), tx, tokenTestSub, at))
	require.NoError(t, tx.Commit())
}

func recordTokens(t *testing.T, s *Server, id, keyID string, costMicros int64, tokens int) {
	t.Helper()
	require.NoError(t, s.usage.Record(t.Context(), store.UsageRecord{
		ID: id, TenantID: store.DefaultTenantID, APIKeyID: keyID, Provider: "p", Model: "gpt-6-luna",
		Status: "success", PromptTokens: tokens, CostMicros: costMicros, CreatedAt: time.Now().UTC(),
	}))
}

func TestTokenOrderFirstPurchaseCreatesKey(t *testing.T) {
	s, paygKey := newTokenTestServer(t)
	ctx := t.Context()
	require.True(t, payTokenOrder(t, s, paygKey, "t1", "tp-100m"))

	sub, err := s.db.TokenSubscriptions().GetBySub(ctx, tokenTestSub)
	require.NoError(t, err)
	require.NotEqual(t, paygKey, sub.KeyID)
	require.NotEmpty(t, sub.SealedKey.Ciphertext)
	require.WithinDuration(t, time.Now().Add(15*24*time.Hour), sub.ExpiresAt, time.Minute)

	key, err := s.identity.Get(ctx, sub.KeyID)
	require.NoError(t, err)
	require.Equal(t, "token:u@example.com", key.Name)

	b := tokenBudget(t, s, sub.KeyID)
	require.EqualValues(t, 100_000_000, b.LimitTokens)
	require.Zero(t, b.LimitMicros)
	require.Equal(t, "total", b.Period)
	require.True(t, b.HardCutoff)

	models, _, err := s.identity.Keys().EffectiveAllowedModels(ctx, sub.KeyID, "")
	require.NoError(t, err)
	require.ElementsMatch(t, tokenpkg.DefaultModels, models)

	order, err := s.db.PaymentOrders().Get(ctx, "t1")
	require.NoError(t, err)
	require.Equal(t, store.PaymentCompleted, order.Status)
	require.EqualValues(t, 5_000_000, tokenBudget(t, s, paygKey).LimitMicros, "PAYG credit untouched")
}

func TestTokenOrderStacksWhileActive(t *testing.T) {
	s, paygKey := newTokenTestServer(t)
	require.True(t, payTokenOrder(t, s, paygKey, "t1", "tp-100m"))
	sub1, err := s.db.TokenSubscriptions().GetBySub(t.Context(), tokenTestSub)
	require.NoError(t, err)

	require.True(t, payTokenOrder(t, s, paygKey, "t2", "tp-50m"))
	sub2, err := s.db.TokenSubscriptions().GetBySub(t.Context(), tokenTestSub)
	require.NoError(t, err)
	require.Equal(t, sub1.KeyID, sub2.KeyID)
	require.True(t, sub1.ExpiresAt.Equal(sub2.ExpiresAt))
	require.EqualValues(t, 150_000_000, tokenBudget(t, s, sub2.KeyID).LimitTokens)
}

func TestTokenOrderAfterExpiryDropsRemainder(t *testing.T) {
	s, paygKey := newTokenTestServer(t)
	require.True(t, payTokenOrder(t, s, paygKey, "t1", "tp-100m"))
	sub, err := s.db.TokenSubscriptions().GetBySub(t.Context(), tokenTestSub)
	require.NoError(t, err)
	setTokenExpiry(t, s, time.Now().Add(-time.Hour))
	recordTokens(t, s, "u1", sub.KeyID, 0, 10_000_000)

	require.True(t, payTokenOrder(t, s, paygKey, "t2", "tp-20m"))
	got, err := s.db.TokenSubscriptions().GetBySub(t.Context(), tokenTestSub)
	require.NoError(t, err)
	require.WithinDuration(t, time.Now().Add(15*24*time.Hour), got.ExpiresAt, time.Minute)
	require.EqualValues(t, 30_000_000, tokenBudget(t, s, sub.KeyID).LimitTokens)
}

func TestTokenDaysOrderAfterExpiryIsManual(t *testing.T) {
	s, paygKey := newTokenTestServer(t)
	require.True(t, payTokenOrder(t, s, paygKey, "t1", "tp-100m"))
	past := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	setTokenExpiry(t, s, past)

	require.False(t, payTokenOrder(t, s, paygKey, "t2", "tp-days-15"))
	order, err := s.db.PaymentOrders().Get(t.Context(), "t2")
	require.NoError(t, err)
	require.Equal(t, store.PaymentManual, order.Status)
	require.Contains(t, order.Reason, "no active package")
	require.True(t, strings.HasPrefix(order.Reason, "refund_required: "))

	sub, err := s.db.TokenSubscriptions().GetBySub(t.Context(), tokenTestSub)
	require.NoError(t, err)
	require.True(t, past.Equal(sub.ExpiresAt), "expiry unchanged")
	require.EqualValues(t, 100_000_000, tokenBudget(t, s, sub.KeyID).LimitTokens)
}

func TestTokenOrderDeletedPackageIsManual(t *testing.T) {
	s, paygKey := newTokenTestServer(t)
	o := makeTokenOrder(t, s, paygKey, "t1", "tp-50m")
	require.NoError(t, s.db.TokenPackages().Delete(t.Context(), "tp-50m"))

	applied, err := s.markTokenOrderPaid(t.Context(), o, store.PaymentCompleted, "webhook", "")
	require.NoError(t, err)
	require.False(t, applied)
	order, err := s.db.PaymentOrders().Get(t.Context(), "t1")
	require.NoError(t, err)
	require.Equal(t, store.PaymentManual, order.Status)
	require.Contains(t, order.Reason, "refund_required: package deleted")

	_, err = s.db.TokenSubscriptions().GetBySub(t.Context(), tokenTestSub)
	require.ErrorIs(t, err, store.ErrNotFound, "no key is created for a deleted package")
}

func TestTokenWebhookAppliesOnce(t *testing.T) {
	s, paygKey := newTokenTestServer(t)
	s.cfg.Payment.WebhookSecret = "whsec_" + base64.StdEncoding.EncodeToString([]byte("topsecret"))
	_ = makeTokenOrder(t, s, paygKey, "tw1", "tp-100m")

	payload := `{"event_type":"payment.completed","data":{"payment_id":"pay-tw1","order_id":"tw1","amount":259000,"status":"completed"}}`
	id, ts := "msg_t1", "1700000000"
	mac := hmac.New(sha256.New, []byte("topsecret"))
	mac.Write([]byte(id + "." + ts + "." + payload))
	sig := "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil))
	deliver := func() int {
		req := httptest.NewRequest(http.MethodPost, "/webhooks/sumopod", strings.NewReader(payload))
		req.Header.Set("Svix-Id", id)
		req.Header.Set("Svix-Timestamp", ts)
		req.Header.Set("Svix-Signature", sig)
		rec := httptest.NewRecorder()
		s.handleSumopodWebhook(rec, req)
		return rec.Code
	}
	require.Equal(t, http.StatusOK, deliver())
	require.Equal(t, http.StatusOK, deliver())

	sub, err := s.db.TokenSubscriptions().GetBySub(t.Context(), tokenTestSub)
	require.NoError(t, err)
	require.EqualValues(t, 100_000_000, tokenBudget(t, s, sub.KeyID).LimitTokens)
	require.EqualValues(t, 5_000_000, tokenBudget(t, s, paygKey).LimitMicros, "a token order never adds credit")
	topups, err := s.db.Topups().ListByKey(t.Context(), paygKey)
	require.NoError(t, err)
	require.Empty(t, topups)
}

func TestTokenAndCreditIsolation(t *testing.T) {
	s, paygKey := newTokenTestServer(t)
	require.True(t, payTokenOrder(t, s, paygKey, "t1", "tp-100m"))
	sub, err := s.db.TokenSubscriptions().GetBySub(t.Context(), tokenTestSub)
	require.NoError(t, err)

	recordTokens(t, s, "u-payg", paygKey, 1_000_000, 5_000_000)
	recordTokens(t, s, "u-token", sub.KeyID, 2_000_000, 7_000_000)

	dec, err := s.budgetEngine.Check(t.Context(), budget.Scope{APIKeyID: sub.KeyID})
	require.NoError(t, err)
	require.True(t, dec.Allowed)
	require.Len(t, dec.BudgetUsage, 1)
	require.EqualValues(t, 7_000_000, dec.BudgetUsage[0].TokenCount)

	dec, err = s.budgetEngine.Check(t.Context(), budget.Scope{APIKeyID: paygKey})
	require.NoError(t, err)
	require.True(t, dec.Allowed)
	require.Len(t, dec.BudgetUsage, 1)
	require.EqualValues(t, 1_000_000, dec.BudgetUsage[0].CostMicros)
}

func TestAdminApproveTokenOrder(t *testing.T) {
	s, paygKey := newTokenTestServer(t)
	_ = makeTokenOrder(t, s, paygKey, "ta1", "tp-100m")

	approve := func() int {
		req := httptest.NewRequest(http.MethodPost, "/payments/orders/ta1/approve", strings.NewReader(`{"reason":"bank transfer"}`))
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", "ta1")
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
		rec := httptest.NewRecorder()
		s.adminApprovePaymentOrder(rec, req)
		return rec.Code
	}
	require.Equal(t, http.StatusOK, approve())
	require.Equal(t, http.StatusConflict, approve())

	sub, err := s.db.TokenSubscriptions().GetBySub(t.Context(), tokenTestSub)
	require.NoError(t, err)
	require.EqualValues(t, 100_000_000, tokenBudget(t, s, sub.KeyID).LimitTokens)
	require.EqualValues(t, 5_000_000, tokenBudget(t, s, paygKey).LimitMicros)
	order, err := s.db.PaymentOrders().Get(t.Context(), "ta1")
	require.NoError(t, err)
	require.Equal(t, store.PaymentManual, order.Status)
}

func TestCreditPaymentOrderRejectsTokenOrder(t *testing.T) {
	s, paygKey := newTokenTestServer(t)
	o := makeTokenOrder(t, s, paygKey, "tc1", "tp-100m")
	o.CreditMicros = 1_000_000
	_, _, applied, err := s.creditPaymentOrder(t.Context(), o, store.PaymentCompleted, "webhook", "")
	require.Error(t, err)
	require.False(t, applied)
	require.EqualValues(t, 5_000_000, tokenBudget(t, s, paygKey).LimitMicros)
}

func TestAdminCancelAndSummaryRecogniseTokenOrders(t *testing.T) {
	s, paygKey := newTokenTestServer(t)
	_ = makeTokenOrder(t, s, paygKey, "tx1", "tp-100m")
	require.True(t, payTokenOrder(t, s, paygKey, "tx2", "tp-50m"))

	req := httptest.NewRequest(http.MethodPost, "/payments/orders/tx1/cancel", strings.NewReader(`{"reason":"new link"}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "tx1")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	s.adminCancelPaymentOrder(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	rec = httptest.NewRecorder()
	s.adminPaymentSummary(rec, httptest.NewRequest(http.MethodGet, "/payments/summary", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	var sum struct {
		TotalIDR    int64          `json:"total_idr"`
		TokenIDR    int64          `json:"token_total_idr"`
		TokenCounts map[string]int `json:"token_count_by_status"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &sum))
	require.Zero(t, sum.TotalIDR, "token sales are not credit revenue")
	require.EqualValues(t, 259000, sum.TokenIDR)
	require.Equal(t, map[string]int{"cancelled": 1, "completed": 1}, sum.TokenCounts)
}
