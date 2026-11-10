package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/currency"
	"github.com/mydisha/keirouter/backend/internal/payment"
	"github.com/mydisha/keirouter/backend/internal/store"
	"github.com/mydisha/keirouter/backend/internal/tokenpkg"
)

// tokenPortalServer is a payment portal server selling token packages with
// tp-20m, tp-100m and tp-days-15 enabled. The returned counter counts
// SumoPod CreatePayment calls.
func tokenPortalServer(t *testing.T) (*Server, string, *atomic.Int32) {
	t.Helper()
	s, tok := paymentPortalTestServer(t)
	ctx := t.Context()
	calls := &atomic.Int32{}
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		writeJSON(w, http.StatusOK, payment.CreateResponse{
			PaymentID: "pay-" + strconv.Itoa(int(n)), PaymentLinkURL: "https://pay.example/" + strconv.Itoa(int(n)),
			Status: "pending", ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		})
	}))
	t.Cleanup(gw.Close)
	s.paymentClient = payment.NewClient(gw.URL, "k")
	require.NoError(t, tokenpkg.Save(ctx, s.db.Settings(), tokenpkg.Settings{
		Enabled: true, PlanID: tokenpkg.DefaultPlanID,
	}))
	for _, id := range []string{"tp-20m", "tp-100m", "tp-days-15"} {
		p, err := s.db.TokenPackages().Get(ctx, id)
		require.NoError(t, err)
		p.Enabled = true
		require.NoError(t, s.db.TokenPackages().Update(ctx, p))
	}
	return s, tok, calls
}

func portalReq(method, path, body, tok string) *http.Request {
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	if tok != "" {
		req.AddCookie(&http.Cookie{Name: portalSessionCookie, Value: tok})
	}
	return req
}

func createTokenOrder(t *testing.T, s *Server, tok, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	s.handlePortalCreateTokenOrder(rec, portalReq(http.MethodPost, "/portal/api/token/orders", body, tok))
	return rec
}

func tokenStatus(t *testing.T, s *Server, tok string) map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	s.handlePortalTokenStatus(rec, portalReq(http.MethodGet, "/portal/api/token", "", tok))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var out map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	return out
}

func TestPortalTokenPackagesHidesDisabled(t *testing.T) {
	s, _, _ := tokenPortalServer(t)

	get := func() map[string]any {
		rec := httptest.NewRecorder()
		s.handlePortalTokenPackages(rec, httptest.NewRequest(http.MethodGet, "/portal/token/packages", nil))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var out map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
		return out
	}

	out := get()
	require.Equal(t, true, out["on_sale"])
	pkgs := out["packages"].([]any)
	var ids []string
	for _, p := range pkgs {
		ids = append(ids, p.(map[string]any)["id"].(string))
	}
	require.Equal(t, []string{"tp-20m", "tp-100m", "tp-days-15"}, ids)
	first := pkgs[1].(map[string]any)
	require.Equal(t, "100M Token", first["name"])
	require.Equal(t, "tokens", first["kind"])
	require.EqualValues(t, 100_000_000, first["tokens"])
	require.EqualValues(t, 15, first["days"])
	require.EqualValues(t, 259000, first["price_idr"])
	require.Equal(t, true, first["featured"])

	require.NoError(t, tokenpkg.Save(t.Context(), s.db.Settings(), tokenpkg.Settings{
		Enabled: false,
	}))
	require.Equal(t, false, get()["on_sale"])

	require.NoError(t, tokenpkg.Save(t.Context(), s.db.Settings(), tokenpkg.Settings{
		Enabled: true,
	}))
	s.paymentClient = nil
	require.Equal(t, false, get()["on_sale"])
}

func TestPortalTokenOrderRejectsDaysWithoutActive(t *testing.T) {
	s, tok, calls := tokenPortalServer(t)
	rec := createTokenOrder(t, s, tok, `{"package_id":"tp-days-15","idempotency_key":"d1"}`)
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	var out struct {
		Error struct{ Code string } `json:"error"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.Equal(t, "no_active_package", out.Error.Code)
	require.Zero(t, calls.Load())

	// Expired subscription is not active either.
	u, err := s.db.PortalUsers().GetBySub(t.Context(), "gsub")
	require.NoError(t, err)
	paid := store.PaymentOrder{
		ID: "paid-1", TenantID: adminTenant, KeyID: u.KeyID, GoogleSub: "gsub", AmountIDR: 59000,
		Status: store.PaymentPending, Provider: "sumopod", CreatedAt: time.Now(), UpdatedAt: time.Now(),
		Product: store.ProductToken, TokenPackageID: "tp-20m",
	}
	require.NoError(t, s.db.PaymentOrders().Create(t.Context(), paid))
	applied, err := s.markTokenOrderPaid(t.Context(), paid, store.PaymentCompleted, "test", "")
	require.NoError(t, err)
	require.True(t, applied)
	rec = createTokenOrder(t, s, tok, `{"package_id":"tp-days-15","idempotency_key":"d2"}`)
	require.Equal(t, http.StatusCreated, rec.Code, "allowed while active: %s", rec.Body.String())
	require.EqualValues(t, 1, calls.Load())

	setTokenExpiryFor(t, s, "gsub", time.Now().Add(-time.Minute))
	rec = createTokenOrder(t, s, tok, `{"package_id":"tp-days-15","idempotency_key":"d3"}`)
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	require.EqualValues(t, 1, calls.Load())
}

func TestPortalTokenOrderRejectsDisabledPackage(t *testing.T) {
	s, tok, calls := tokenPortalServer(t)
	for _, id := range []string{"tp-50m", "nope"} {
		rec := createTokenOrder(t, s, tok, `{"package_id":"`+id+`","idempotency_key":"x-`+id+`"}`)
		require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	}
	require.Zero(t, calls.Load())

	// Not on sale at all.
	require.NoError(t, tokenpkg.Save(t.Context(), s.db.Settings(), tokenpkg.Settings{
		Enabled: false,
	}))
	rec := createTokenOrder(t, s, tok, `{"package_id":"tp-20m","idempotency_key":"x"}`)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	require.Zero(t, calls.Load())
}

func TestPortalTokenOrderCreatesPayment(t *testing.T) {
	s, tok, calls := tokenPortalServer(t)
	ctx := t.Context()
	u, err := s.db.PortalUsers().GetBySub(ctx, "gsub")
	require.NoError(t, err)

	rec := createTokenOrder(t, s, tok, `{"package_id":"tp-100m","idempotency_key":"k1"}`)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var out map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	id := out["id"].(string)
	require.Equal(t, "pending", out["status"])
	require.Equal(t, "https://pay.example/1", out["payment_link_url"])
	require.EqualValues(t, 259000, out["amount_idr"])

	o, err := s.db.PaymentOrders().Get(ctx, id)
	require.NoError(t, err)
	require.Equal(t, store.ProductToken, o.Product)
	require.Equal(t, "tp-100m", o.TokenPackageID)
	require.EqualValues(t, 259000, o.AmountIDR)
	require.Zero(t, o.CreditMicros)
	require.Equal(t, "gsub", o.GoogleSub)
	require.Equal(t, u.KeyID, o.KeyID)
	require.NotEmpty(t, o.ExpiresAt)

	// Same idempotency key: same order, no new payment.
	rec = createTokenOrder(t, s, tok, `{"package_id":"tp-100m","idempotency_key":"k1"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.Equal(t, id, out["id"])
	require.EqualValues(t, 1, calls.Load())

	// Another click for the same package reuses the open order.
	rec = createTokenOrder(t, s, tok, `{"package_id":"tp-100m","idempotency_key":"k2"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.Equal(t, id, out["id"])
	require.EqualValues(t, 1, calls.Load())

	// A different package gets its own order.
	rec = createTokenOrder(t, s, tok, `{"package_id":"tp-20m","idempotency_key":"k3"}`)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.NotEqual(t, id, out["id"])
	require.EqualValues(t, 59000, out["amount_idr"])
	require.EqualValues(t, 2, calls.Load())

	// A credit order's idempotency key cannot be replayed as a token order.
	require.NoError(t, s.db.PaymentOrders().Create(ctx, store.PaymentOrder{
		ID: "credit-1", TenantID: adminTenant, KeyID: u.KeyID, GoogleSub: "gsub", AmountIDR: 50000,
		Status: store.PaymentPending, Provider: "sumopod", IdempotencyKey: "credit-idem",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}))
	rec = createTokenOrder(t, s, tok, `{"package_id":"tp-20m","idempotency_key":"credit-idem"}`)
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())

	// Order history lists token orders only, with the package name.
	list := httptest.NewRecorder()
	s.handlePortalListTokenOrders(list, portalReq(http.MethodGet, "/portal/api/token/orders", "", tok))
	require.Equal(t, http.StatusOK, list.Code, list.Body.String())
	var orders []map[string]any
	require.NoError(t, json.Unmarshal(list.Body.Bytes(), &orders))
	require.Len(t, orders, 2)
	names := map[string]bool{}
	for _, o := range orders {
		names[o["package_name"].(string)] = true
		require.Equal(t, "pending", o["status"])
		require.Contains(t, o, "created_at")
		require.Contains(t, o, "paid_at")
	}
	require.Equal(t, map[string]bool{"100M Token": true, "20M Token": true}, names)
}

func TestPortalTokenStatus(t *testing.T) {
	s, tok, _ := tokenPortalServer(t)
	ctx := t.Context()

	st := tokenStatus(t, s, tok)
	require.Equal(t, false, st["exists"])
	require.Equal(t, false, st["active"])
	require.Equal(t, "", st["expires_at"])
	require.EqualValues(t, 0, st["limit_tokens"])
	require.Equal(t, "", st["key_display"])

	reveal := httptest.NewRecorder()
	s.handlePortalTokenKeyReveal(reveal, portalReq(http.MethodPost, "/portal/api/token/key/reveal", "", tok))
	require.Equal(t, http.StatusNotFound, reveal.Code, reveal.Body.String())

	rec := createTokenOrder(t, s, tok, `{"package_id":"tp-100m","idempotency_key":"s1"}`)
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
	recordTokens(t, s, "u1", sub.KeyID, 0, 1234)
	key, err := s.identity.Get(ctx, sub.KeyID)
	require.NoError(t, err)

	st = tokenStatus(t, s, tok)
	require.Equal(t, true, st["exists"])
	require.Equal(t, true, st["active"])
	require.EqualValues(t, 100_000_000, st["limit_tokens"])
	require.EqualValues(t, 1234, st["used_tokens"])
	require.Equal(t, key.Display, st["key_display"])
	exp, err := time.Parse(time.RFC3339, st["expires_at"].(string))
	require.NoError(t, err)
	require.WithinDuration(t, sub.ExpiresAt, exp, time.Second)
	require.ElementsMatch(t, []any{"gpt-6-sol", "gpt-6-terra", "gpt-6-luna"}, st["models"])

	reveal = httptest.NewRecorder()
	s.handlePortalTokenKeyReveal(reveal, portalReq(http.MethodPost, "/portal/api/token/key/reveal", "", tok))
	require.Equal(t, http.StatusOK, reveal.Code, reveal.Body.String())
	var rv map[string]string
	require.NoError(t, json.Unmarshal(reveal.Body.Bytes(), &rv))
	authed, err := s.identity.Authenticate(ctx, rv["key"])
	require.NoError(t, err)
	require.Equal(t, sub.KeyID, authed.ID)

	setTokenExpiryFor(t, s, "gsub", time.Now().Add(-time.Minute))
	st = tokenStatus(t, s, tok)
	require.Equal(t, true, st["exists"])
	require.Equal(t, false, st["active"])
}

func setTokenExpiryFor(t *testing.T, s *Server, sub string, at time.Time) {
	t.Helper()
	tx, err := s.db.SQL().BeginTx(t.Context(), nil)
	require.NoError(t, err)
	require.NoError(t, s.db.TokenSubscriptions().SetExpiresOnTx(t.Context(), tx, sub, at))
	require.NoError(t, tx.Commit())
}

func TestCreditTopupRefusesTokenKey(t *testing.T) {
	s, tok, _ := tokenPortalServer(t)
	ctx := t.Context()
	require.NoError(t, s.currencySvc.Save(ctx, currency.Settings{
		OverrideEnabled: true, OverrideRate: 16000, RefreshIntervalH: 24, SourceURL: currency.DefaultSourceURL,
	}))
	s.cfg.Payment.MinTopupIDR = 10000
	s.cfg.Payment.MaxTopupIDR = 10_000_000
	u, err := s.db.PortalUsers().GetBySub(ctx, "gsub")
	require.NoError(t, err)

	rec := createTokenOrder(t, s, tok, `{"package_id":"tp-20m","idempotency_key":"t1"}`)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var created map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created))
	o, err := s.db.PaymentOrders().Get(ctx, created["id"].(string))
	require.NoError(t, err)
	_, err = s.markTokenOrderPaid(ctx, o, store.PaymentCompleted, "webhook", "")
	require.NoError(t, err)
	sub, err := s.db.TokenSubscriptions().GetBySub(ctx, "gsub")
	require.NoError(t, err)

	topup := func(idem string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		s.handlePortalCreateOrder(rec, portalReq(http.MethodPost, "/portal/api/topup/orders",
			`{"amount_idr":50000,"idempotency_key":"`+idem+`"}`, tok))
		return rec
	}

	// The credit order lands on portal_users.key_id, never the token key.
	rec = topup("c1")
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created))
	credit, err := s.db.PaymentOrders().Get(ctx, created["order_id"].(string))
	require.NoError(t, err)
	require.Equal(t, u.KeyID, credit.KeyID)
	require.NotEqual(t, sub.KeyID, credit.KeyID)

	// Even if the portal binding pointed at the token key, credit is refused.
	u.KeyID = sub.KeyID
	require.NoError(t, s.db.PortalUsers().Upsert(ctx, u))
	rec = topup("c2")
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	orders, err := s.db.PaymentOrders().ListByKey(ctx, sub.KeyID)
	require.NoError(t, err)
	require.Empty(t, orders)
}
