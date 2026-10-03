package gateway

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/auth"
	"github.com/mydisha/keirouter/backend/internal/config"
	"github.com/mydisha/keirouter/backend/internal/store"
)

// newAuthGateway spins up a gateway wired with only what the /api/auth surface
// needs: a settings-backed auth service. Enough to exercise login, the session
// cookie, status, and the sessionMiddleware-protected onboarding endpoint.
func newAuthGateway(t *testing.T, cfg config.Config) *httptest.Server {
	t.Helper()
	ctx := context.Background()

	db, err := store.Open(ctx, config.DatabaseConfig{Driver: "sqlite", DSN: ":memory:"}, t.TempDir())
	require.NoError(t, err)
	require.NoError(t, db.Migrate(ctx))
	require.NoError(t, db.Tenants().EnsureDefault(ctx))
	t.Cleanup(func() { _ = db.Close() })

	authSvc := auth.New(db.Settings(), cfg.Security.JWTSecret, cfg.Security.SessionTTL)
	_, err = authSvc.EnsureDefaults(ctx)
	require.NoError(t, err)

	gw := New(Deps{Config: cfg, Auth: authSvc, DB: db, Settings: db.Settings()})
	srv := httptest.NewServer(gw.Handler())
	t.Cleanup(srv.Close)
	return srv
}

func findCookie(cookies []*http.Cookie, name string) *http.Cookie {
	for _, c := range cookies {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// TestLoginIssuesUsableSessionOverPlainHTTP is the regression test for
// https://github.com/mydisha/keirouter/issues/56: on a fresh install reached
// over plain HTTP (e.g. http://<vps-ip>:20180), POST /api/auth/login returned
// ok=true but the session cookie was marked Secure, which browsers refuse to
// store on insecure non-localhost origins. Every follow-up request then
// arrived without a cookie and /api/auth/status reported authenticated=false,
// leaving the user stuck on the login page with no error.
func TestLoginIssuesUsableSessionOverPlainHTTP_Issue56(t *testing.T) {
	srv := newAuthGateway(t, config.Default())

	// Login with the seeded default password, as a fresh install would.
	resp, err := http.Post(srv.URL+"/api/auth/login", "application/json",
		strings.NewReader(`{"password":"`+auth.DefaultPassword+`"}`))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var loginBody map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&loginBody))
	require.Equal(t, true, loginBody["ok"])

	// The cookie must exist and must NOT be Secure: over plain HTTP a Secure
	// cookie is silently dropped by the browser — the exact #56 failure.
	session := findCookie(resp.Cookies(), sessionCookie)
	require.NotNil(t, session, "login must set a session cookie")
	require.NotEmpty(t, session.Value)
	require.False(t, session.Secure, "session cookie must not be Secure over plain HTTP")
	require.True(t, session.HttpOnly)
	require.Equal(t, http.SameSiteLaxMode, session.SameSite)

	// Round-trip: replay the cookie exactly like a browser would on the next
	// navigation. Before the fix this request carried no cookie at all.
	req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/auth/status", nil)
	require.NoError(t, err)
	req.AddCookie(session)
	statusResp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer statusResp.Body.Close()
	require.Equal(t, http.StatusOK, statusResp.StatusCode)

	var statusBody map[string]any
	require.NoError(t, json.NewDecoder(statusResp.Body).Decode(&statusBody))
	require.Equal(t, true, statusBody["authenticated"],
		"status must report authenticated=true when the session cookie is replayed")

	// The same cookie must unlock sessionMiddleware-protected endpoints.
	req, err = http.NewRequest(http.MethodPost, srv.URL+"/api/auth/onboarding/complete", strings.NewReader("{}"))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(session)
	protResp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer protResp.Body.Close()
	require.Equal(t, http.StatusOK, protResp.StatusCode)

	// Sanity: without the cookie the protected endpoint stays locked.
	noCookieResp, err := http.Post(srv.URL+"/api/auth/onboarding/complete", "application/json", strings.NewReader("{}"))
	require.NoError(t, err)
	defer noCookieResp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, noCookieResp.StatusCode)
}

// TestLoginRejectsWhenTurnstileInvalid verifies that an enabled Turnstile whose
// verification fails blocks login with 403 before the password check runs.
func TestLoginRejectsWhenTurnstileInvalid(t *testing.T) {
	s, _, _ := newMarketPricingTestServer(t)
	fake := &fakeTurnstile{enabled: true, accept: false}
	s.turnstile = fake

	rec := marketRequest(t, s, &http.Cookie{Name: sessionCookie, Value: ""},
		http.MethodPost, "/api/auth/login", `{"password":"keirouter"}`)
	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	require.Equal(t, 1, fake.calls)
}

// TestLoginPassesWithValidTurnstile verifies that a valid Turnstile token lets
// login proceed to the password check, and the token is read from the body.
func TestLoginPassesWithValidTurnstile(t *testing.T) {
	s, _, _ := newMarketPricingTestServer(t)
	fake := &fakeTurnstile{enabled: true, accept: true}
	s.turnstile = fake

	rec := marketRequest(t, s, &http.Cookie{Name: sessionCookie, Value: ""},
		http.MethodPost, "/api/auth/login", `{"password":"keirouter","turnstile_token":"tok"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, 1, fake.calls)
}

// TestLogoutClearsCookieWithoutSecureFlag ensures the expiry cookie written on
// logout matches the non-Secure session cookie set over plain HTTP, so the
// browser actually deletes it.
func TestLogoutClearsCookieWithoutSecureFlag(t *testing.T) {
	srv := newAuthGateway(t, config.Default())

	resp, err := http.Post(srv.URL+"/api/auth/logout", "application/json", nil)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	session := findCookie(resp.Cookies(), sessionCookie)
	require.NotNil(t, session, "logout must emit an expiry cookie")
	require.False(t, session.Secure)
	require.Equal(t, "", session.Value)
}

// TestSessionCookieSecureFlag covers the decision table for the Secure
// attribute on the dashboard session cookie.
func TestSessionCookieSecureFlag(t *testing.T) {
	defaultCfg := config.Default() // TrustForwardedHeaders defaults to true.

	untrustedCfg := config.Default()
	untrustedCfg.Security.TrustForwardedHeaders = false

	trustedCfg := config.Default()
	trustedCfg.Security.TrustForwardedHeaders = true

	cases := []struct {
		name    string
		cfg     config.Config
		tls     bool
		headers map[string]string
		want    bool
	}{
		{name: "plain HTTP default", cfg: defaultCfg, want: false},
		{
			name:    "X-Forwarded-Proto ignored when trust disabled",
			cfg:     untrustedCfg,
			headers: map[string]string{"X-Forwarded-Proto": "https"},
			want:    false,
		},
		{
			name:    "trusted proxy asserting https",
			cfg:     trustedCfg,
			headers: map[string]string{"X-Forwarded-Proto": "https"},
			want:    true,
		},
		{
			name:    "trusted proxy asserting http",
			cfg:     trustedCfg,
			headers: map[string]string{"X-Forwarded-Proto": "http"},
			want:    false,
		},
		{
			name:    "trusted proxy chain: first hop wins",
			cfg:     trustedCfg,
			headers: map[string]string{"X-Forwarded-Proto": "https, http"},
			want:    true,
		},
		{
			name:    "RFC 7239 Forwarded with proto=https",
			cfg:     trustedCfg,
			headers: map[string]string{"Forwarded": `for=192.0.2.60;proto=https;by=203.0.113.43`},
			want:    true,
		},
		{
			name:    "RFC 7239 Forwarded with proto=http",
			cfg:     trustedCfg,
			headers: map[string]string{"Forwarded": "for=192.0.2.60;proto=http"},
			want:    false,
		},
		{name: "direct TLS connection", cfg: defaultCfg, tls: true, want: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{cfg: tc.cfg}
			r := httptest.NewRequest(http.MethodGet, "http://example.test/api/auth/login", nil)
			if tc.tls {
				r.TLS = &tls.ConnectionState{}
			}
			for k, v := range tc.headers {
				r.Header.Set(k, v)
			}
			require.Equal(t, tc.want, s.sessionCookieSecure(r))
		})
	}
}
