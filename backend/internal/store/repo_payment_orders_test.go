package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func sampleOrder(id string) PaymentOrder {
	return PaymentOrder{
		ID: id, TenantID: DefaultTenantID, KeyID: "key1", GoogleSub: "sub1",
		AmountIDR: 50000, CreditMicros: 3_100_000, FxRateMicros: 16_100_000_000,
		Status: PaymentPending, Provider: "sumopod", ProviderPaymentID: "pay-" + id,
		PaymentLinkURL: "https://pay/pay-" + id, IdempotencyKey: "idem-" + id,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
}

func TestPaymentOrderRepo_CRUD(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	o := sampleOrder("p1")
	require.NoError(t, db.PaymentOrders().Create(ctx, o))

	got, err := db.PaymentOrders().Get(ctx, "p1")
	require.NoError(t, err)
	require.Equal(t, int64(50000), got.AmountIDR)
	require.Equal(t, int64(3_100_000), got.CreditMicros)
	require.Equal(t, PaymentPending, got.Status)

	byProvider, err := db.PaymentOrders().GetByProviderPaymentID(ctx, "sumopod", "pay-p1")
	require.NoError(t, err)
	require.Equal(t, "p1", byProvider.ID)

	byIdem, err := db.PaymentOrders().GetByIdempotencyKey(ctx, "idem-p1")
	require.NoError(t, err)
	require.Equal(t, "p1", byIdem.ID)

	_, err = db.PaymentOrders().Get(ctx, "missing")
	require.ErrorIs(t, err, ErrNotFound)

	// duplicate provider payment id rejected
	dup := sampleOrder("p2")
	dup.IdempotencyKey = "other"
	dup.ProviderPaymentID = "pay-p1"
	require.Error(t, db.PaymentOrders().Create(ctx, dup))
}

func TestPaymentOrderRepo_TransitionOnTx(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	require.NoError(t, db.PaymentOrders().Create(ctx, sampleOrder("p1")))

	tx, err := db.SQL().BeginTx(ctx, nil)
	require.NoError(t, err)
	ok, err := db.PaymentOrders().TransitionOnTx(ctx, tx, "p1", PaymentPending, PaymentCompleted, "webhook", "", "2026-01-01T00:00:00Z")
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, tx.Commit())

	// second transition from pending must not apply
	tx2, err := db.SQL().BeginTx(ctx, nil)
	require.NoError(t, err)
	defer tx2.Rollback()
	ok2, err := db.PaymentOrders().TransitionOnTx(ctx, tx2, "p1", PaymentPending, PaymentManual, "dashboard", "r", "")
	require.NoError(t, err)
	require.False(t, ok2)
}

func TestPaymentOrderRepo_ListAndSum(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	require.NoError(t, db.PaymentOrders().Create(ctx, sampleOrder("p1")))

	c, err := db.PaymentOrders().Get(ctx, "p1")
	require.NoError(t, err)
	require.Equal(t, PaymentPending, c.Status)

	sum, err := db.PaymentOrders().SumCompletedCreditByKey(ctx, "key1")
	require.NoError(t, err)
	require.EqualValues(t, 0, sum)

	// complete p1
	tx, err := db.SQL().BeginTx(ctx, nil)
	require.NoError(t, err)
	ok, err := db.PaymentOrders().TransitionOnTx(ctx, tx, "p1", PaymentPending, PaymentCompleted, "webhook", "", "")
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, tx.Commit())

	sum, err = db.PaymentOrders().SumCompletedCreditByKey(ctx, "key1")
	require.NoError(t, err)
	require.EqualValues(t, 3_100_000, sum)
}
