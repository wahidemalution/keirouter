package gateway

import (
	"context"
	"testing"
	"time"

	"github.com/mydisha/keirouter/backend/internal/identity"
	"github.com/mydisha/keirouter/backend/internal/store"
	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/config"
	"log/slog"
)

func newPaymentTestServer(t *testing.T) *Server {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, config.DatabaseConfig{Driver: "sqlite", DSN: ":memory:"}, t.TempDir())
	require.NoError(t, err)
	require.NoError(t, db.Migrate(ctx))
	require.NoError(t, db.Tenants().EnsureDefault(ctx))
	t.Cleanup(func() { _ = db.Close() })
	return &Server{
		db:       db,
		identity: identity.New(db.APIKeys()),
		budgets:  db.Budgets(),
		usage:    db.Usage(),
		log:      slog.Default(),
	}
}

func makePaidOrder(t *testing.T, s *Server, keyID, id string) store.PaymentOrder {
	t.Helper()
	o := store.PaymentOrder{
		ID: id, TenantID: store.DefaultTenantID, KeyID: keyID, GoogleSub: "sub",
		AmountIDR: 16000, CreditMicros: 1_000_000, FxRateMicros: 16_000_000_000,
		Status: store.PaymentPending, Provider: "sumopod", ProviderPaymentID: "pay-" + id,
		PaymentLinkURL: "https://pay/" + id, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	require.NoError(t, s.db.PaymentOrders().Create(t.Context(), o))
	return o
}

func TestCreditPaymentOrder_OnceAndLedger(t *testing.T) {
	s := newPaymentTestServer(t)
	ctx := context.Background()
	issued, err := s.identity.Create(ctx, store.DefaultTenantID, "", "payer")
	require.NoError(t, err)
	o := makePaidOrder(t, s, issued.Record.ID, "o1")

	ord, budget, applied, err := s.creditPaymentOrder(ctx, o, store.PaymentCompleted, "webhook", "")
	require.NoError(t, err)
	require.True(t, applied)
	require.Equal(t, store.PaymentCompleted, ord.Status)
	require.EqualValues(t, 1_000_000, budget.LimitMicros)

	bs, err := s.budgets.ListByScope(ctx, store.ScopeAPIKey, issued.Record.ID)
	require.NoError(t, err)
	require.Len(t, bs, 1)
	require.EqualValues(t, 1_000_000, bs[0].LimitMicros)
	require.Equal(t, "total", bs[0].Period)

	topups, err := s.db.Topups().ListByKey(ctx, issued.Record.ID)
	require.NoError(t, err)
	require.Len(t, topups, 1)
	require.Equal(t, "payment_order:o1", topups[0].IdempotencyKey)

	// Second call must be a no-op (already handled), no double credit.
	_, _, applied2, err := s.creditPaymentOrder(ctx, o, store.PaymentManual, "dashboard", "manual")
	require.NoError(t, err)
	require.False(t, applied2)
	b2, err := s.budgets.Get(ctx, bs[0].ID)
	require.NoError(t, err)
	require.EqualValues(t, 1_000_000, b2.LimitMicros)
}

func TestCreditPaymentOrder_DisabledKeyFailsClosed(t *testing.T) {
	s := newPaymentTestServer(t)
	ctx := context.Background()
	issued, err := s.identity.Create(ctx, store.DefaultTenantID, "", "payer")
	require.NoError(t, err)
	require.NoError(t, s.identity.SetDisabled(ctx, issued.Record.ID, true))
	o := makePaidOrder(t, s, issued.Record.ID, "o1")

	_, _, _, err = s.creditPaymentOrder(ctx, o, store.PaymentCompleted, "webhook", "")
	require.Error(t, err)
	bs, err := s.budgets.ListByScope(ctx, store.ScopeAPIKey, issued.Record.ID)
	require.NoError(t, err)
	require.Len(t, bs, 0)
}

// A pre-existing periodic budget must be converted to a non-resetting "total"
// budget before credit is added, otherwise the budget engine would re-grant the
// purchased credit on every period reset.
func TestCreditPaymentOrder_ConvertsPeriodicBudgetToTotal(t *testing.T) {
	s := newPaymentTestServer(t)
	ctx := context.Background()
	issued, err := s.identity.Create(ctx, store.DefaultTenantID, "", "payer")
	require.NoError(t, err)
	budgetID := "b-monthly-" + issued.Record.ID
	require.NoError(t, s.budgets.Create(ctx, store.Budget{
		ID: budgetID, TenantID: adminTenant, ScopeKind: store.ScopeAPIKey, ScopeID: issued.Record.ID,
		LimitMicros: 2_000_000, Period: "monthly", AlertPct: 80, HardCutoff: true,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}))
	o := makePaidOrder(t, s, issued.Record.ID, "o-period")

	_, budget, applied, err := s.creditPaymentOrder(ctx, o, store.PaymentCompleted, "webhook", "")
	require.NoError(t, err)
	require.True(t, applied)
	require.Equal(t, "total", budget.Period)

	got, err := s.budgets.Get(ctx, budgetID)
	require.NoError(t, err)
	require.Equal(t, "total", got.Period, "periodic budget must be converted to total")
	require.EqualValues(t, 3_000_000, got.LimitMicros, "limit must increase by the order credit")
}

// A non-positive order credit must fail closed before any write.
func TestCreditPaymentOrder_NonPositiveCreditRejected(t *testing.T) {
	s := newPaymentTestServer(t)
	ctx := context.Background()
	issued, err := s.identity.Create(ctx, store.DefaultTenantID, "", "payer")
	require.NoError(t, err)
	o := makePaidOrder(t, s, issued.Record.ID, "o-zero")
	o.CreditMicros = 0

	_, _, _, err = s.creditPaymentOrder(ctx, o, store.PaymentCompleted, "webhook", "")
	require.Error(t, err)

	bs, err := s.budgets.ListByScope(ctx, store.ScopeAPIKey, issued.Record.ID)
	require.NoError(t, err)
	require.Len(t, bs, 0, "no budget may be written for a zero-credit order")
	orders, err := s.db.PaymentOrders().ListByStatus(ctx, store.PaymentPending)
	require.NoError(t, err)
	require.Len(t, orders, 1, "order must remain pending")
}
