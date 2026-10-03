package gateway

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/mydisha/keirouter/backend/internal/currency"
	"github.com/mydisha/keirouter/backend/internal/identity"
	"github.com/mydisha/keirouter/backend/internal/store"
	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/config"
)

// newPaymentPostgresServer builds a payment-capable Server on the real Postgres
// test DSN. SQLite cannot exercise concurrent writers, so the exactly-once race
// test requires Postgres.
func newPaymentPostgresServer(t *testing.T) *Server {
	t.Helper()
	dsn := os.Getenv("KEIROUTER_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("KEIROUTER_TEST_POSTGRES_DSN not set; skipping concurrency test")
	}
	ctx := context.Background()
	db, err := store.Open(ctx, config.DatabaseConfig{Driver: "postgres", DSN: dsn}, "")
	require.NoError(t, err)
	require.NoError(t, db.Migrate(ctx))
	require.NoError(t, db.Tenants().EnsureDefault(ctx))
	t.Cleanup(func() { _ = db.Close() })
	s := &Server{
		db: db, identity: identity.New(db.APIKeys()), budgets: db.Budgets(),
		usage: db.Usage(), log: slog.Default(),
	}
	s.cfg.Payment.WebhookToken = "whtok_race"
	s.currencySvc = currency.New(db.Settings())
	return s
}

// TestPaymentCreditRace_WebhookAndApproveExactlyOnce fires a verified webhook
// delivery and an admin manual approve for the same pending order
// concurrently. Both paths call creditPaymentOrder; the guarded transition must
// let exactly one win, so the budget is incremented once and one ledger row is
// written.
func TestPaymentCreditRace_WebhookAndApproveExactlyOnce(t *testing.T) {
	s := newPaymentPostgresServer(t)
	ctx := context.Background()
	issued, err := s.identity.Create(ctx, store.DefaultTenantID, "", "pay-race")
	require.NoError(t, err)
	keyID := issued.Record.ID

	orderID := "race-order-" + uuid.NewString()
	providerID := "pay-" + orderID
	order := store.PaymentOrder{
		ID: orderID, TenantID: adminTenant, KeyID: keyID, GoogleSub: "sub",
		AmountIDR: 16000, CreditMicros: 1_000_000, FxRateMicros: 16_000_000_000,
		Status: store.PaymentPending, Provider: "sumopod", ProviderPaymentID: providerID,
		PaymentLinkURL: "https://pay/race", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	require.NoError(t, s.db.PaymentOrders().Create(ctx, order))

	payload := `{"event_type":"payment.completed","data":{"payment_id":"` + providerID + `","order_id":"` + orderID + `","amount":16000,"status":"completed"}}`
	signWebhook := func() *http.Request {
		req := httptest.NewRequest(http.MethodPost, "/webhooks/sumopod", strings.NewReader(payload))
		req.Header.Set("X-Webhook-Token", "whtok_race")
		return req
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		rec := httptest.NewRecorder()
		s.handleSumopodWebhook(rec, signWebhook())
	}()
	go func() {
		defer wg.Done()
		r := httptest.NewRequest(http.MethodPost, "/payments/orders/"+orderID+"/approve", strings.NewReader(`{"reason":"race"}`))
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", orderID)
		r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
		rec := httptest.NewRecorder()
		s.adminApprovePaymentOrder(rec, r)
	}()
	wg.Wait()

	bs, err := s.budgets.ListByScope(ctx, store.ScopeAPIKey, keyID)
	require.NoError(t, err)
	require.Len(t, bs, 1, "exactly one budget")
	require.EqualValues(t, 1_000_000, bs[0].LimitMicros, "credit applied exactly once")

	topups, err := s.db.Topups().ListByKey(ctx, keyID)
	require.NoError(t, err)
	require.Len(t, topups, 1, "exactly one key_topups row")

	got, err := s.db.PaymentOrders().Get(ctx, orderID)
	require.NoError(t, err)
	require.NotEqual(t, store.PaymentPending, got.Status, "order must leave pending exactly once")
}
