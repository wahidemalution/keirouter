package store

import (
	"context"
	"testing"
	"time"

	"github.com/mydisha/keirouter/backend/internal/crypto"
	"github.com/stretchr/testify/require"
)

func TestTokenPackagesSeeded(t *testing.T) {
	db := newTestDB(t)
	list, err := db.TokenPackages().List(context.Background())
	require.NoError(t, err)
	require.Len(t, list, 7)

	featured := 0
	for _, p := range list {
		require.False(t, p.Enabled, p.ID)
		if p.Featured {
			featured++
			require.Equal(t, int64(100_000_000), p.Tokens)
			require.Equal(t, int64(259_000), p.PriceIDR)
		}
		if p.Kind == TokenPackageDays {
			require.Equal(t, 15, p.Days)
			require.Equal(t, int64(15_000), p.PriceIDR)
		}
	}
	require.Equal(t, 1, featured)
}

func TestTokenPackageCRUD(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	repo := db.TokenPackages()

	now := time.Now().UTC().Truncate(time.Second)
	p := TokenPackage{
		ID: "p1", TenantID: DefaultTenantID, Name: "5M Token", Kind: TokenPackageTokens,
		Tokens: 5_000_000, Days: 15, PriceIDR: 9_000, Enabled: true, Featured: false, Sort: 5,
		CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, repo.Create(ctx, p))
	got, err := repo.Get(ctx, "p1")
	require.NoError(t, err)
	require.Equal(t, p, got)

	p.PriceIDR = 12_000
	require.NoError(t, repo.Update(ctx, p))
	got, err = repo.Get(ctx, "p1")
	require.NoError(t, err)
	require.Equal(t, int64(12_000), got.PriceIDR)

	require.NoError(t, repo.Delete(ctx, "p1"))
	_, err = repo.Get(ctx, "p1")
	require.ErrorIs(t, err, ErrNotFound)
	require.ErrorIs(t, repo.Update(ctx, p), ErrNotFound)
	require.ErrorIs(t, repo.Delete(ctx, "p1"), ErrNotFound)
}

func TestTokenSubscriptionRoundTrip(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	require.NoError(t, db.APIKeys().Create(ctx, APIKey{
		ID: "key-t", TenantID: DefaultTenantID, Name: "token:a@example.com",
		KeyHash: "h", LookupHash: "l", Display: "d", CreatedAt: time.Now(),
	}))
	require.NoError(t, db.PortalUsers().Upsert(ctx, PortalUser{
		GoogleSub: "sub-t", Email: "a@example.com", KeyID: "key-t",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}))

	exp := time.Now().Add(15 * 24 * time.Hour).UTC().Truncate(time.Second)
	sub := TokenSubscription{
		GoogleSub: "sub-t", KeyID: "key-t",
		SealedKey: crypto.Sealed{WrappedDEK: "dek", Ciphertext: "ct"},
		ExpiresAt: exp, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	tx, err := db.SQL().BeginTx(ctx, nil)
	require.NoError(t, err)
	require.NoError(t, db.TokenSubscriptions().CreateOnTx(ctx, tx, sub))
	require.NoError(t, tx.Commit())

	got, err := db.TokenSubscriptions().GetByKey(ctx, "key-t")
	require.NoError(t, err)
	require.Equal(t, exp, got.ExpiresAt)
	require.Equal(t, "sub-t", got.GoogleSub)
	require.Equal(t, sub.SealedKey, got.SealedKey)

	bySub, err := db.TokenSubscriptions().GetBySub(ctx, "sub-t")
	require.NoError(t, err)
	require.Equal(t, "key-t", bySub.KeyID)

	later := exp.Add(48 * time.Hour)
	tx, err = db.SQL().BeginTx(ctx, nil)
	require.NoError(t, err)
	require.NoError(t, db.TokenSubscriptions().SetExpiresOnTx(ctx, tx, "sub-t", later))
	onTx, err := db.TokenSubscriptions().GetBySubOnTx(ctx, tx, "sub-t")
	require.NoError(t, err)
	require.Equal(t, later, onTx.ExpiresAt)
	require.NoError(t, tx.Commit())

	got, err = db.TokenSubscriptions().GetByKey(ctx, "key-t")
	require.NoError(t, err)
	require.Equal(t, later, got.ExpiresAt)

	all, err := db.TokenSubscriptions().List(ctx)
	require.NoError(t, err)
	require.Len(t, all, 1)

	tx, err = db.SQL().BeginTx(ctx, nil)
	require.NoError(t, err)
	require.ErrorIs(t, db.TokenSubscriptions().SetExpiresOnTx(ctx, tx, "nobody", later), ErrNotFound)
	require.NoError(t, tx.Rollback())

	_, err = db.TokenSubscriptions().GetByKey(ctx, "missing")
	require.ErrorIs(t, err, ErrNotFound)
}

func TestPaymentOrderTokenPackageID(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	o := sampleOrder("p1")
	o.Product = ProductToken
	o.TokenPackageID = "p1"
	require.NoError(t, db.PaymentOrders().Create(ctx, o))

	got, err := db.PaymentOrders().Get(ctx, "p1")
	require.NoError(t, err)
	require.Equal(t, ProductToken, got.Product)
	require.Equal(t, "p1", got.TokenPackageID)
}

func TestSetTokenLimitOnTx(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Now()
	require.NoError(t, db.Budgets().Create(ctx, Budget{
		ID: "b1", TenantID: DefaultTenantID, ScopeKind: ScopeAPIKey, ScopeID: "k1",
		LimitMicros: 7, LimitTokens: 10, Period: "total", HardCutoff: true,
		CreatedAt: now, UpdatedAt: now,
	}))

	tx, err := db.SQL().BeginTx(ctx, nil)
	require.NoError(t, err)
	require.NoError(t, db.Budgets().SetTokenLimitOnTx(ctx, tx, "b1", 50))
	require.ErrorIs(t, db.Budgets().SetTokenLimitOnTx(ctx, tx, "nope", 1), ErrNotFound)
	require.ErrorIs(t, db.Budgets().SetTokenLimitOnTx(ctx, tx, "b1", -1), ErrInvalidLimit)
	require.NoError(t, tx.Commit())

	got, err := db.Budgets().Get(ctx, "b1")
	require.NoError(t, err)
	require.Equal(t, int64(50), got.LimitTokens)
	require.Equal(t, int64(7), got.LimitMicros)
}
