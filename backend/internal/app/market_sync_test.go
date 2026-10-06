package app

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mydisha/keirouter/backend/internal/config"
	"github.com/mydisha/keirouter/backend/internal/market"
	"github.com/mydisha/keirouter/backend/internal/store"
)

func newSyncTestApp(t *testing.T, db *store.DB) *App {
	t.Helper()
	return &App{db: db, log: slog.New(slog.NewTextHandler(io.Discard, nil)), marketCache: market.NewSnapshotCache()}
}

func newSyncTestDB(t *testing.T) *store.DB {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, config.DatabaseConfig{Driver: "sqlite", DSN: ":memory:"}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.Tenants().EnsureDefault(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestSyncChainMarketPricesPopulatesCache(t *testing.T) {
	db := newSyncTestDB(t)
	ctx := context.Background()

	if err := market.SaveSettings(ctx, db.Settings().Set, market.Settings{
		RefreshIntervalSeconds: 2, MarkupPercent: 0,
	}); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"models":[{"slug":"ocg/kimi-k2.6","minAskIn":1,"minAskOut":2},{"slug":"cmc/deepseek/deepseek-v4-pro","minAskIn":3,"minAskOut":4}]}`))
	}))
	defer srv.Close()

	a := newSyncTestApp(t, db)
	a.marketURL = srv.URL
	if _, err := a.syncChainMarketPrices(ctx); err != nil {
		t.Fatalf("sync: %v", err)
	}
	r, ok := a.MarketCache().Rate("ocg/kimi-k2.6")
	if !ok || r.InputPerM != 1 || r.OutputPerM != 2 {
		t.Fatalf("cache rate = %+v ok=%v", r, ok)
	}
}

func TestSyncChainMarketPricesWritesCheapest(t *testing.T) {
	db := newSyncTestDB(t)
	ctx := context.Background()

	c := store.Chain{
		ID: "c1", TenantID: store.DefaultTenantID, Name: "c1", Strategy: "priority",
		MarketSlugs: []string{"a/x", "b/y"},
		Steps:       []store.ChainStep{{ID: "s1", ChainID: "c1", Position: 0, Provider: "openai", Model: "gpt-4o"}},
	}
	if err := db.Chains().Create(ctx, c); err != nil {
		t.Fatal(err)
	}

	if err := market.SaveSettings(ctx, db.Settings().Set, market.Settings{
		RefreshIntervalSeconds: 2, MarkupPercent: 0,
	}); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"models":[{"slug":"a/x","minAskIn":1,"minAskOut":5},{"slug":"b/y","minAskIn":2,"minAskOut":3}]}`))
	}))
	defer srv.Close()

	a := newSyncTestApp(t, db)
	a.marketURL = srv.URL
	n, err := a.syncChainMarketPrices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("synced = %d, want 1", n)
	}

	got, err := db.Chains().Get(ctx, "c1")
	if err != nil {
		t.Fatal(err)
	}
	if got.InputPerM != 1 || got.OutputPerM != 3 {
		t.Fatalf("price = (%v,%v), want (1,3)", got.InputPerM, got.OutputPerM)
	}
	if got.CacheReadPerM != 0.1 || got.CacheWritePerM != 1.25 {
		t.Fatalf("cache = (%v,%v), want (0.1,1.25)", got.CacheReadPerM, got.CacheWritePerM)
	}
}

func TestSyncChainMarketPricesSkipsUnknownSlug(t *testing.T) {
	db := newSyncTestDB(t)
	ctx := context.Background()
	c := store.Chain{
		ID: "c2", TenantID: store.DefaultTenantID, Name: "c2", Strategy: "priority",
		InputPerM: 9, OutputPerM: 9, MarketSlugs: []string{"does/not/exist"},
	}
	if err := db.Chains().Create(ctx, c); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"models":[{"slug":"a/x","minAskIn":1,"minAskOut":1}]}`))
	}))
	defer srv.Close()

	a := newSyncTestApp(t, db)
	a.marketURL = srv.URL
	n, err := a.syncChainMarketPrices(ctx)
	if err != nil || n != 0 {
		t.Fatalf("n=%d err=%v, want 0,nil", n, err)
	}
	got, _ := db.Chains().Get(ctx, "c2")
	if got.InputPerM != 9 {
		t.Fatalf("price changed to %v, want untouched 9", got.InputPerM)
	}
}

func TestSyncChainMarketPricesSkipsZeroAskButUpdatesValid(t *testing.T) {
	db := newSyncTestDB(t)
	ctx := context.Background()

	zero := store.Chain{
		ID: "zero", TenantID: store.DefaultTenantID, Name: "zero", Strategy: "priority",
		InputPerM: 7, OutputPerM: 7, MarketSlugs: []string{"z/zero"},
	}
	valid := store.Chain{
		ID: "valid", TenantID: store.DefaultTenantID, Name: "valid", Strategy: "priority",
		MarketSlugs: []string{"a/x"},
	}
	if err := db.Chains().Create(ctx, zero); err != nil {
		t.Fatal(err)
	}
	if err := db.Chains().Create(ctx, valid); err != nil {
		t.Fatal(err)
	}
	if err := market.SaveSettings(ctx, db.Settings().Set, market.Settings{
		RefreshIntervalSeconds: 2, MarkupPercent: 0,
	}); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"models":[{"slug":"z/zero","minAskIn":0,"minAskOut":0},{"slug":"a/x","minAskIn":1,"minAskOut":5}]}`))
	}))
	defer srv.Close()

	a := newSyncTestApp(t, db)
	a.marketURL = srv.URL
	n, err := a.syncChainMarketPrices(ctx)
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v, want 1,nil", n, err)
	}

	gotZero, _ := db.Chains().Get(ctx, "zero")
	if gotZero.InputPerM != 7 || gotZero.OutputPerM != 7 {
		t.Fatalf("zero-ask chain price = (%v,%v), want untouched (7,7)", gotZero.InputPerM, gotZero.OutputPerM)
	}
	gotValid, _ := db.Chains().Get(ctx, "valid")
	if gotValid.InputPerM != 1 || gotValid.OutputPerM != 5 {
		t.Fatalf("valid chain price = (%v,%v), want (1,5)", gotValid.InputPerM, gotValid.OutputPerM)
	}
}
