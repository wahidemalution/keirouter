package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/store"
)

func TestAdminChainDisableToggle(t *testing.T) {
	s, db, cookie := newMarketPricingTestServer(t)
	s.chains = db.Chains()

	rec := marketRequest(t, s, cookie, http.MethodPost, "/api/chains",
		`{"name":"dis","steps":[{"provider":"openai","model":"gpt-4o"}]}`)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var created struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created))

	entry := func() map[string]any {
		var list struct {
			Chains []map[string]any `json:"chains"`
		}
		require.NoError(t, json.Unmarshal(marketRequest(t, s, cookie, http.MethodGet, "/api/chains", "").Body.Bytes(), &list))
		return chainEntryByName(t, list.Chains, "dis")
	}
	require.Equal(t, false, entry()["disabled"])
	require.Equal(t, false, entry()["sold_out"])

	require.Equal(t, http.StatusOK, marketRequest(t, s, cookie, http.MethodPatch, "/api/chains/"+created.ID, `{"disabled":true}`).Code)
	require.Equal(t, true, entry()["disabled"])
	require.Equal(t, true, entry()["sold_out"])

	// An unrelated edit keeps the chain disabled and its steps intact.
	require.Equal(t, http.StatusOK, marketRequest(t, s, cookie, http.MethodPatch, "/api/chains/"+created.ID, `{"strategy":"priority"}`).Code)
	require.Equal(t, true, entry()["disabled"])
	require.Len(t, entry()["steps"], 1)

	require.Equal(t, http.StatusOK, marketRequest(t, s, cookie, http.MethodPatch, "/api/chains/"+created.ID, `{"disabled":false}`).Code)
	require.Equal(t, false, entry()["disabled"])
	require.Equal(t, false, entry()["sold_out"])
}

func TestDisabledChainIsSoldOutEverywhere(t *testing.T) {
	db, gw := newPublicTestGatewayWithDB(t)
	ctx := context.Background()
	require.NoError(t, db.Chains().Create(ctx, store.Chain{
		ID: "c-off", TenantID: store.DefaultTenantID, Name: "off-model",
		Strategy: "priority", InputPerM: 1, OutputPerM: 2, Disabled: true,
		Steps:     []store.ChainStep{{Provider: "openai", Model: "gpt-4o", Position: 0}},
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}))

	models := getPublicModels(t, gw)
	require.Len(t, models, 1)
	require.True(t, models[0].SoldOut)
	require.Zero(t, models[0].InputPerM, "a disabled chain shows no price")

	for _, name := range []string{"off-model", "chain:off-model"} {
		_, err := resolveTargetsWith(ctx, db.Chains(), nil, nil, nil, 0, store.DefaultTenantID, name)
		var bad badModelError
		require.ErrorAs(t, err, &bad, name)
		require.Equal(t, http.StatusServiceUnavailable, bad.HTTPStatus())
		require.Contains(t, bad.Error(), "sold out")
	}
}

func TestChainUpdateRefreshesPublicCatalog(t *testing.T) {
	s, db, cookie := newMarketPricingTestServer(t)
	s.chains = db.Chains()
	s.usage = db.Usage()
	rec := marketRequest(t, s, cookie, http.MethodPost, "/api/chains",
		`{"name":"fresh","steps":[{"provider":"openai","model":"gpt-4o"}]}`)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var created struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created))

	require.False(t, getPublicModels(t, s)[0].SoldOut) // primes the cache
	require.Equal(t, http.StatusOK, marketRequest(t, s, cookie, http.MethodPatch, "/api/chains/"+created.ID, `{"disabled":true}`).Code)
	require.True(t, getPublicModels(t, s)[0].SoldOut, "catalog must not serve the cached pre-disable body")
}

func TestMaintenanceChainIsOffEverywhere(t *testing.T) {
	db, gw := newPublicTestGatewayWithDB(t)
	ctx := context.Background()
	require.NoError(t, db.Chains().Create(ctx, store.Chain{
		ID: "c-mnt", TenantID: store.DefaultTenantID, Name: "mnt-model",
		Strategy: "priority", InputPerM: 1, OutputPerM: 2, Maintenance: true,
		Steps:     []store.ChainStep{{Provider: "openai", Model: "gpt-4o", Position: 0}},
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}))

	require.Empty(t, getPublicModels(t, gw), "a chain under maintenance is left out of the catalog")
	require.Empty(t, gw.withoutUnlistedChains(ctx, store.DefaultTenantID, []string{"mnt-model"}))

	for _, name := range []string{"mnt-model", "chain:mnt-model"} {
		_, err := resolveTargetsWith(ctx, db.Chains(), nil, nil, nil, 0, store.DefaultTenantID, name)
		var bad badModelError
		require.ErrorAs(t, err, &bad, name)
		require.Equal(t, http.StatusServiceUnavailable, bad.HTTPStatus())
		require.Contains(t, bad.Error(), "temporarily disabled")
	}
}

func TestHiddenChainIsUnlistedButCallable(t *testing.T) {
	db, gw := newPublicTestGatewayWithDB(t)
	ctx := context.Background()
	for _, c := range []store.Chain{
		{ID: "c-hid", Name: "hid-model", Hidden: true},
		{ID: "c-vis", Name: "vis-model"},
		{ID: "c-sold", Name: "sold-model", Disabled: true},
	} {
		c.TenantID, c.Strategy, c.InputPerM, c.OutputPerM = store.DefaultTenantID, "priority", 1, 2
		c.Steps = []store.ChainStep{{ID: c.ID + "-s0", Provider: "openai", Model: "gpt-4o", Position: 0}}
		c.CreatedAt, c.UpdatedAt = time.Now(), time.Now()
		require.NoError(t, db.Chains().Create(ctx, c))
	}

	byID := map[string]publicModelJSON{}
	for _, m := range getPublicModels(t, gw) {
		byID[m.ModelID] = m
	}
	require.Len(t, byID, 2)
	require.False(t, byID["vis-model"].SoldOut)
	require.True(t, byID["sold-model"].SoldOut, "a sold-out chain stays in the catalog")

	res, err := resolveTargetsWith(ctx, db.Chains(), nil, nil, nil, 0, store.DefaultTenantID, "hid-model")
	require.NoError(t, err, "a hidden chain still routes")
	require.NotEmpty(t, res.Targets)

	got := gw.withoutUnlistedChains(ctx, store.DefaultTenantID, []string{"hid-model", "chain:HID-MODEL", "vis-model", "sold-model", "*"})
	require.Equal(t, []string{"vis-model", "sold-model", "*"}, got)
}

func TestAdminChainMaintenanceAndHiddenToggles(t *testing.T) {
	s, db, cookie := newMarketPricingTestServer(t)
	s.chains = db.Chains()

	rec := marketRequest(t, s, cookie, http.MethodPost, "/api/chains",
		`{"name":"mh","steps":[{"provider":"openai","model":"gpt-4o"}]}`)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var created struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created))
	entry := func() map[string]any {
		var list struct {
			Chains []map[string]any `json:"chains"`
		}
		require.NoError(t, json.Unmarshal(marketRequest(t, s, cookie, http.MethodGet, "/api/chains", "").Body.Bytes(), &list))
		return chainEntryByName(t, list.Chains, "mh")
	}
	patch := func(body string) {
		require.Equal(t, http.StatusOK, marketRequest(t, s, cookie, http.MethodPatch, "/api/chains/"+created.ID, body).Code)
	}

	patch(`{"maintenance":true}`)
	require.Equal(t, true, entry()["maintenance"])
	require.Equal(t, false, entry()["disabled"], "maintenance does not mark the chain sold out")
	patch(`{"hidden":true}`)
	require.Equal(t, true, entry()["hidden"])
	require.Equal(t, true, entry()["maintenance"])
	patch(`{"maintenance":false,"hidden":false}`)
	require.Equal(t, false, entry()["maintenance"])
	require.Equal(t, false, entry()["hidden"])
}
