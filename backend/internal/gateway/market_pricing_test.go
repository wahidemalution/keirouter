package gateway

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/auth"
	"github.com/mydisha/keirouter/backend/internal/config"
	"github.com/mydisha/keirouter/backend/internal/store"
)

func newMarketPricingTestServer(t *testing.T) (*Server, *store.DB, *http.Cookie) {
	t.Helper()
	ctx := context.Background()

	db, err := store.Open(ctx, config.DatabaseConfig{Driver: "sqlite", DSN: ":memory:"}, t.TempDir())
	require.NoError(t, err)
	require.NoError(t, db.Migrate(ctx))
	require.NoError(t, db.Tenants().EnsureDefault(ctx))
	t.Cleanup(func() { _ = db.Close() })

	authSvc := auth.New(db.Settings(), "", time.Hour)
	_, err = authSvc.EnsureDefaults(ctx)
	require.NoError(t, err)
	token, err := authSvc.IssueSession()
	require.NoError(t, err)

	s := New(Deps{
		Config:   config.Default(),
		DB:       db,
		Settings: db.Settings(),
		Auth:     authSvc,
		Accounts: db.Accounts(),
	})
	return s, db, &http.Cookie{Name: sessionCookie, Value: token}
}

func marketRequest(t *testing.T, s *Server, cookie *http.Cookie, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	r.RemoteAddr = "127.0.0.1:12345"
	r.AddCookie(cookie)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, r)
	return rec
}

func TestMarketPricingSettingsRoundTrip(t *testing.T) {
	s, _, cookie := newMarketPricingTestServer(t)

	rec := marketRequest(t, s, cookie, http.MethodGet, "/api/market-pricing/settings", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "markup_percent")

	rec = marketRequest(t, s, cookie, http.MethodPatch, "/api/market-pricing/settings", `{"markup_percent":25}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"markup_percent":25`)
}

func TestMarketPricingSettingsValidation(t *testing.T) {
	s, _, cookie := newMarketPricingTestServer(t)

	for _, tc := range []struct {
		name string
		body string
	}{
		{"markup too high", `{"markup_percent":1001}`},
		{"markup negative", `{"markup_percent":-1}`},
		{"refresh too low", `{"refresh_interval_minutes":0}`},
	} {
		rec := marketRequest(t, s, cookie, http.MethodPatch, "/api/market-pricing/settings", tc.body)
		require.Equal(t, http.StatusBadRequest, rec.Code, tc.name+": "+rec.Body.String())
	}

	rec := marketRequest(t, s, cookie, http.MethodGet, "/api/market-pricing/settings", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"markup_percent":10`)
}

func TestMarketBindingCRUD(t *testing.T) {
	s, _, cookie := newMarketPricingTestServer(t)

	rec := marketRequest(t, s, cookie, http.MethodPut, "/api/market-pricing/bindings/openai/gpt-4o", `{"market_slug":"ag/gpt-4o"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	rec = marketRequest(t, s, cookie, http.MethodGet, "/api/market-pricing/bindings", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := rec.Body.String()
	require.Contains(t, body, "ag/gpt-4o")
	require.Contains(t, body, `"market_slug"`)
	require.Contains(t, body, `"provider_id"`)
	require.NotContains(t, body, `"MarketSlug"`)
	require.NotContains(t, body, `"ProviderID"`)

	rec = marketRequest(t, s, cookie, http.MethodDelete, "/api/market-pricing/bindings/openai/gpt-4o", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	rec = marketRequest(t, s, cookie, http.MethodGet, "/api/market-pricing/bindings", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotContains(t, rec.Body.String(), "ag/gpt-4o")
}

func TestMarketRefreshReturnsCounts(t *testing.T) {
	s, _, cookie := newMarketPricingTestServer(t)
	s.syncMarketPrices = func(context.Context) (int, error) { return 3, nil }

	rec := marketRequest(t, s, cookie, http.MethodPost, "/api/market-pricing/refresh", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"synced":3`)
	require.Contains(t, rec.Body.String(), "last_fetched_at")
}

func TestMarketRefreshErrorRecords(t *testing.T) {
	s, _, cookie := newMarketPricingTestServer(t)
	s.syncMarketPrices = func(context.Context) (int, error) { return 0, errors.New("market down") }

	rec := marketRequest(t, s, cookie, http.MethodPost, "/api/market-pricing/refresh", "")
	require.Equal(t, http.StatusBadGateway, rec.Code, rec.Body.String())

	rec = marketRequest(t, s, cookie, http.MethodGet, "/api/market-pricing/settings", "")
	require.Contains(t, rec.Body.String(), "market down")
}
