package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/config"
	"github.com/mydisha/keirouter/backend/internal/identity"
	"github.com/mydisha/keirouter/backend/internal/store"
)

func newBrandingGateway(t *testing.T) (*Server, *identity.Service) {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, config.DatabaseConfig{Driver: "sqlite", DSN: ":memory:"}, t.TempDir())
	require.NoError(t, err)
	require.NoError(t, db.Migrate(ctx))
	t.Cleanup(func() { _ = db.Close() })

	idSvc := identity.New(db.APIKeys())
	s := New(Deps{Config: config.Default(), DB: db, Settings: db.Settings(), Identity: idSvc})
	return s, idSvc
}

func TestUpdateBrandingPersistsKeyPrefix(t *testing.T) {
	s, idSvc := newBrandingGateway(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/settings/branding", strings.NewReader(`{"api_key_prefix":"tkr_"}`))
	s.adminUpdateBranding(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var got BrandingSettings
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Equal(t, "tkr_", got.APIKeyPrefix)

	// Persisted value round-trips through the loader.
	require.Equal(t, "tkr_", s.loadBrandingSettings(context.Background()).APIKeyPrefix)

	// The running identity service mints new keys with the new prefix.
	issued, err := idSvc.Generate(store.DefaultTenantID, "", "k")
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(issued.Plaintext, "tkr_"))
}

func TestUpdateBrandingRejectsInvalidKeyPrefix(t *testing.T) {
	s, _ := newBrandingGateway(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/settings/branding", strings.NewReader(`{"api_key_prefix":"tkr!"}`))
	s.adminUpdateBranding(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code)

	// Nothing persisted: loader still returns the default.
	require.Equal(t, "kr_", s.loadBrandingSettings(context.Background()).APIKeyPrefix)
}

func TestLoadBrandingFallsBackForInvalidStoredPrefix(t *testing.T) {
	s, _ := newBrandingGateway(t)
	require.NoError(t, s.settings.Set(context.Background(), brandingSettingsKey, `{"name":"X","api_key_prefix":"BAD!"}`))
	require.Equal(t, "kr_", s.loadBrandingSettings(context.Background()).APIKeyPrefix)
}

func TestBrandingDefaultsKeyPrefix(t *testing.T) {
	s, _ := newBrandingGateway(t)
	require.Equal(t, "kr_", s.loadBrandingSettings(context.Background()).APIKeyPrefix)
}

type fakeTurnstile struct {
	enabled bool
	siteKey string
	accept  bool
	calls   int
}

func (f *fakeTurnstile) Enabled() bool   { return f.enabled }
func (f *fakeTurnstile) SiteKey() string { return f.siteKey }
func (f *fakeTurnstile) Verify(_ context.Context, token, _ string) error {
	f.calls++
	if f.accept {
		return nil
	}
	return errFakeTurnstile
}

var errFakeTurnstile = errors.New("fake turnstile rejected")

func TestPortalBrandingExposesTurnstile(t *testing.T) {
	srv := newPortalTestServer(t)
	srv.turnstile = &fakeTurnstile{enabled: true, siteKey: "site-abc"}
	srv.cfg = config.Default()

	req := httptest.NewRequest(http.MethodGet, "/v1/portal/branding", nil)
	rec := httptest.NewRecorder()
	srv.portalBranding(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"turnstile_enabled":true`)
	require.Contains(t, rec.Body.String(), `"turnstile_site_key":"site-abc"`)
}
