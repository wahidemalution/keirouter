package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/dispatch"
	"github.com/mydisha/keirouter/backend/internal/market"
	"github.com/mydisha/keirouter/backend/internal/store"
)

// A latency-ordered chain runs its fastest step first, but the price shown
// and reserved is the first step in declared order.
func TestPortalImageShownPriceIsDeclaredOrder(t *testing.T) {
	f := newPortalImageFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	require.NoError(t, f.srv.db.Chains().Create(ctx, store.Chain{
		ID: "fast", TenantID: store.DefaultTenantID, Name: "fast", Strategy: "latency",
		Steps: []store.ChainStep{
			{ID: "fast-0", Position: 0, Provider: "openai", Model: "gpt-image-1", StaticPerImage: 0.04},
			{ID: "fast-1", Position: 1, Provider: "azure", Model: "gpt-image-1", StaticPerImage: 0.02},
		},
		CreatedAt: now, UpdatedAt: now,
	}))
	f.srv.health = f.srv.db.Health()
	for provider, ms := range map[string]int{"openai": 900, "azure": 100} {
		require.NoError(t, f.srv.db.Accounts().Create(ctx, store.Account{
			ID: "acc-" + provider, TenantID: store.DefaultTenantID, Provider: provider, Label: provider,
			AuthKind: store.AuthAPIKey, Priority: 10, Metadata: "{}", CreatedAt: now, UpdatedAt: now,
		}))
		require.NoError(t, f.srv.db.Health().Upsert(ctx, store.AccountHealth{
			ID: "h-" + provider, TenantID: store.DefaultTenantID, AccountID: "acc-" + provider, Provider: provider,
			Model: "gpt-image-1", Status: "healthy", LatencyMS: ms, LastCheckedAt: now, UpdatedAt: now,
		}))
	}
	res, err := f.srv.resolveTargets(ctx, store.DefaultTenantID, "chain:fast")
	require.NoError(t, err)
	require.Equal(t, "azure", res.Targets[0].Provider, "the route runs fastest first")

	tok, keyID := f.user("sub-a", 10*picsPrice)
	rec := f.do(http.MethodGet, "/portal/api/images/models", "", tok)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"chain_id":"fast","name":"fast","family":"gpt-image","price_micro":44000`)

	rec = f.do(http.MethodPost, "/portal/api/images", `{"chain_id":"fast","expected_price_micro":22000,"prompt":"x","n":1}`, tok)
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	require.Equal(t, "price_changed", errorCode(t, rec))

	f.submit(tok, `{"chain_id":"fast","expected_price_micro":44000,"prompt":"x","n":1}`)
	job := f.waitDone(tok, 1)[0]
	require.Equal(t, store.ImageJobSucceeded, job.Status, job.Error)
	require.Equal(t, int64(22_000), f.spend(keyID), "billed at the step that answered, never above the shown price")
}

// A submit must carry the price the page showed; without it, or with a
// stale one, nothing is created or reserved.
func TestPortalImagePriceChanged(t *testing.T) {
	f := newPortalImageFixture(t)
	tok, keyID := f.user("sub-a", 10*picsPrice)
	for _, body := range []string{
		`{"chain_id":"pics","prompt":"x","n":1}`,
		`{"chain_id":"pics","expected_price_micro":0,"prompt":"x","n":1}`,
		`{"chain_id":"pics","expected_price_micro":40000,"prompt":"x","n":1}`,
	} {
		rec := f.do(http.MethodPost, "/portal/api/images", body, tok)
		require.Equal(t, http.StatusConflict, rec.Code, body)
		require.Equal(t, "price_changed", errorCode(t, rec))
	}
	require.Empty(t, f.list(tok))
	require.Zero(t, f.spend(keyID))
}

// A grok-imagine chain without a 2K cost cannot take a 2K job: billing it
// at the standard price would sell below the 2K cost.
func TestPortalImageGrok2KNeedsPrice(t *testing.T) {
	f := newPortalImageFixture(t)
	createImageChain(t, f.srv.db, "grok-pics", store.ChainStep{Provider: "xai", Model: "grok-imagine-image", StaticPerImage: 0.02})
	tok, keyID := f.user("sub-a", 10*picsPrice)
	rec := f.do(http.MethodPost, "/portal/api/images",
		`{"chain_id":"grok-pics","expected_price_micro":22000,"prompt":"x","n":1,"resolution":"2k"}`, tok)
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	require.Equal(t, "model_not_available", errorCode(t, rec))
	require.Empty(t, f.list(tok))
	require.Zero(t, f.spend(keyID))
}

// Every route except portal image generation drops every image step, even
// one that also carries a token price or market slug.
func TestChatDropsEveryImageStep(t *testing.T) {
	require.Empty(t, dropImageSteps([]dispatch.Target{
		{ImagePrice: 0.04},
		{ImagePrice: 0.04, StaticInputPerM: 1, StaticOutputPerM: 2, StaticPrice: true},
		{ImagePrice: 0.04, MarketSlug: "openai/gpt-image-1"},
	}))
	require.Len(t, dropImageSteps([]dispatch.Target{{Provider: "openai", Model: "gpt-4o"}}), 1)

	db, gw := newPublicTestGatewayWithDB(t)
	gw.marketCache = market.NewSnapshotCache()
	// A step stored before admin refused token prices on image steps.
	createImageChain(t, db, "legacy",
		store.ChainStep{Provider: "openai", Model: "gpt-image-1", StaticPerImage: 0.04, StaticInputPerM: 1, StaticOutputPerM: 2},
		store.ChainStep{Provider: "openai", Model: "gpt-4o", StaticInputPerM: 1, StaticOutputPerM: 2},
	)
	res, err := gw.resolveChatTargets(context.Background(), store.DefaultTenantID, "legacy")
	require.NoError(t, err)
	require.Len(t, res.Targets, 1)
	require.Equal(t, "gpt-4o", res.Targets[0].Model)
}

func TestAdminImageStepRules(t *testing.T) {
	cases := []struct{ name, step, want string }{
		{"grok without 2K", `{"provider":"xai","model":"grok-imagine-image","static_per_image":0.02}`, "Model Grok wajib diisi modal 2K"},
		{"price above cap", `{"provider":"openai","model":"gpt-image-1","static_per_image":100.01}`, "Modal per gambar maksimal $100"},
		{"2K above cap", `{"provider":"xai","model":"grok-imagine-image","static_per_image":1,"static_per_image_2k":101}`, "Modal per gambar maksimal $100"},
		{"market slug", `{"provider":"openai","model":"gpt-image-1","static_per_image":0.04,"market_slug":"openai/gpt-image-1"}`, "Step gambar tidak boleh punya harga token atau market slug"},
		{"token price", `{"provider":"openai","model":"gpt-image-1","static_per_image":0.04,"static_input_per_m":1,"static_output_per_m":2}`, "Step gambar tidak boleh punya harga token atau market slug"},
		{"cache price", `{"provider":"openai","model":"gpt-image-1","static_per_image":0.04,"static_cache_read_per_m":1}`, "Step gambar tidak boleh punya harga token atau market slug"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newChainPricingTestServer(t)
			code, _, body := postChain(t, s, `{"name":"img","steps":[`+c.step+`]}`)
			require.Equal(t, http.StatusBadRequest, code, body)
			var out map[string]any
			require.NoError(t, json.Unmarshal([]byte(body), &out))
			require.Contains(t, body, c.want)
		})
	}

	s := newChainPricingTestServer(t)
	code, _, body := postChain(t, s, `{"name":"img","steps":[
		{"provider":"xai","model":"grok-imagine-image","static_per_image":0.02,"static_per_image_2k":0.05,"markup_percent":20}]}`)
	require.Equal(t, http.StatusCreated, code, body)
	code, _, body = postChain(t, s, `{"name":"img100","steps":[
		{"provider":"openai","model":"gpt-image-1","static_per_image":100}]}`)
	require.Equal(t, http.StatusCreated, code, body)
}
