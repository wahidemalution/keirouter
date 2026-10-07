package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/market"
	"github.com/mydisha/keirouter/backend/internal/store"
)

type publicModelJSON struct {
	ModelID    string  `json:"model_id"`
	InputPerM  float64 `json:"input_per_m"`
	OutputPerM float64 `json:"output_per_m"`
	SoldOut    bool    `json:"sold_out"`
}

func getPublicModels(t *testing.T, gw *Server) []publicModelJSON {
	t.Helper()
	rec := httptest.NewRecorder()
	gw.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/public/models", nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var payload struct {
		Models []publicModelJSON `json:"models"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	return payload.Models
}

// All bound slugs absent from the snapshot -> sold out, prices stay 0 even
// though the first step (openai/gpt-4o) has a non-zero catalog price.
func TestPublicModelsSoldOutWhenNoMarketSlugResolves(t *testing.T) {
	db, gw := newPublicTestGatewayWithDB(t)
	require.NoError(t, db.Chains().Create(context.Background(), store.Chain{
		ID: "c-soldout", TenantID: store.DefaultTenantID, Name: "claude-fable-5.1",
		Strategy:    "priority",
		MarketSlugs: []string{"cc/claude-fable-5-1"},
		Steps:       []store.ChainStep{{Provider: "openai", Model: "gpt-4o", Position: 0}},
		CreatedAt:   time.Now(), UpdatedAt: time.Now(),
	}))
	gw.marketCache = market.NewSnapshotCache() // empty snapshot: nothing resolves

	models := getPublicModels(t, gw)
	require.Len(t, models, 1)
	require.True(t, models[0].SoldOut, "all slugs missing => sold out")
	require.Zero(t, models[0].InputPerM, "sold-out price must stay 0")
	require.Zero(t, models[0].OutputPerM)
}

// One bound slug resolves -> available, priced from the resolved slug.
func TestPublicModelsAvailableWhenOneMarketSlugResolves(t *testing.T) {
	db, gw := newPublicTestGatewayWithDB(t)
	require.NoError(t, db.Chains().Create(context.Background(), store.Chain{
		ID: "c-partial", TenantID: store.DefaultTenantID, Name: "partial-combo",
		Strategy:    "priority",
		MarketSlugs: []string{"cc/claude-fable-5-1", "ocg/deepseek-v4.1-flash"},
		Steps:       []store.ChainStep{{Provider: "custom-openai-inf", Model: "deepseek-v4.1-flash", Position: 0}},
		CreatedAt:   time.Now(), UpdatedAt: time.Now(),
	}))
	cache := market.NewSnapshotCache()
	cache.Replace([]market.Model{{Slug: "ocg/deepseek-v4.1-flash", MinAskIn: 0.042, MinAskOut: 0.168}})
	gw.marketCache = cache

	models := getPublicModels(t, gw)
	require.Len(t, models, 1)
	require.False(t, models[0].SoldOut, "one resolving slug => not sold out")
}

// No market slugs -> never sold out; catalog price shown as today.
func TestPublicModelsNoMarketSlugsNeverSoldOut(t *testing.T) {
	db, gw := newPublicTestGatewayWithDB(t)
	require.NoError(t, db.Chains().Create(context.Background(), store.Chain{
		ID: "c-catalog", TenantID: store.DefaultTenantID, Name: "catalog-combo",
		Strategy:  "priority",
		Steps:     []store.ChainStep{{Provider: "openai", Model: "gpt-4o", Position: 0}},
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}))
	gw.marketCache = market.NewSnapshotCache()

	models := getPublicModels(t, gw)
	require.Len(t, models, 1)
	require.False(t, models[0].SoldOut, "no market slugs => never sold out")
	require.Greater(t, models[0].InputPerM, 0.0, "catalog price still shown")
}

// nil cache must not panic and must not mark a no-slug chain sold out.
func TestPublicModelsNilMarketCacheNoSlugs(t *testing.T) {
	db, gw := newPublicTestGatewayWithDB(t)
	require.NoError(t, db.Chains().Create(context.Background(), store.Chain{
		ID: "c-nil", TenantID: store.DefaultTenantID, Name: "nil-combo",
		Strategy:  "priority",
		Steps:     []store.ChainStep{{Provider: "openai", Model: "gpt-4o", Position: 0}},
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}))
	// gw.marketCache stays nil (New(Deps{...}) does not wire it).
	models := getPublicModels(t, gw)
	require.Len(t, models, 1)
	require.False(t, models[0].SoldOut)
}
