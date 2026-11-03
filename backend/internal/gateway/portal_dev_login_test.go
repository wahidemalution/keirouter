package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func devLoginRequest(remoteAddr, host string, headers map[string]string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/portal/auth/dev-login", nil)
	req.RemoteAddr = remoteAddr
	req.Host = host
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return req
}

func TestPortalDevLoginOffByDefault(t *testing.T) {
	srv := newPortalTestServer(t)
	srv.router = srv.routes()

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, devLoginRequest("127.0.0.1:5555", "127.0.0.1:20180", nil))
	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Empty(t, rec.Result().Cookies())

	status := httptest.NewRecorder()
	srv.handlePortalStatus(status, httptest.NewRequest(http.MethodGet, "/portal/auth/status", nil))
	require.NotContains(t, status.Body.String(), "dev_login")
}

func TestPortalDevLoginGuards(t *testing.T) {
	srv := newPortalTestServer(t)
	srv.cfg.PortalSSO.DevLogin = true
	srv.router = srv.routes()

	cases := []struct {
		name    string
		remote  string
		host    string
		headers map[string]string
	}{
		{"non-loopback remote", "203.0.113.5:1", "127.0.0.1:20180", nil},
		{"X-Forwarded-For", "127.0.0.1:5555", "127.0.0.1:20180", map[string]string{"X-Forwarded-For": "198.51.100.7"}},
		{"X-Real-IP", "127.0.0.1:5555", "127.0.0.1:20180", map[string]string{"X-Real-IP": "198.51.100.7"}},
		{"CF-Connecting-IP", "127.0.0.1:5555", "127.0.0.1:20180", map[string]string{"CF-Connecting-IP": "198.51.100.7"}},
		{"Forwarded", "127.0.0.1:5555", "127.0.0.1:20180", map[string]string{"Forwarded": "for=198.51.100.7"}},
		{"public host", "127.0.0.1:5555", "keirouter.example.com", nil},
		{"public host with port", "127.0.0.1:5555", "keirouter.example.com:20180", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, devLoginRequest(tc.remote, tc.host, tc.headers))
			require.Equal(t, http.StatusNotFound, rec.Code)
			require.Empty(t, rec.Result().Cookies())
		})
	}
}

func TestPortalDevLoginIssuesSession(t *testing.T) {
	for _, host := range []string{"127.0.0.1:20180", "localhost:5180", "[::1]:20180", "localhost"} {
		t.Run(host, func(t *testing.T) {
			srv := newPortalTestServer(t)
			srv.cfg.PortalSSO.DevLogin = true
			srv.router = srv.routes()

			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, devLoginRequest("127.0.0.1:5555", host, nil))
			require.Equal(t, http.StatusNoContent, rec.Code)

			var cookie *http.Cookie
			for _, c := range rec.Result().Cookies() {
				if c.Name == portalSessionCookie {
					cookie = c
				}
			}
			require.NotNil(t, cookie, "portal session cookie must be set")

			sub, ok := srv.auth.SessionSubject(cookie.Value)
			require.True(t, ok)
			require.Equal(t, "portal:dev-local", sub)

			req := httptest.NewRequest(http.MethodGet, "/portal/auth/status", nil)
			req.AddCookie(cookie)
			st := httptest.NewRecorder()
			srv.Handler().ServeHTTP(st, req)
			require.Equal(t, http.StatusOK, st.Code)
			var body map[string]any
			require.NoError(t, json.Unmarshal(st.Body.Bytes(), &body))
			require.Equal(t, true, body["authenticated"])
			require.Equal(t, true, body["dev_login"])
			require.Equal(t, "dev@localhost", body["email"])
		})
	}
}

func TestPortalStatusDevLoginFlagWhenSignedOut(t *testing.T) {
	srv := newPortalTestServer(t)
	srv.cfg.PortalSSO.DevLogin = true

	rec := httptest.NewRecorder()
	srv.handlePortalStatus(rec, httptest.NewRequest(http.MethodGet, "/portal/auth/status", nil))
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, false, body["authenticated"])
	require.Equal(t, true, body["dev_login"])
}

func TestPortalDevLoginDockerPeer(t *testing.T) {
	dockerGateway := "172.18.0.1:40000"

	srv := newPortalTestServer(t)
	srv.cfg.PortalSSO.DevLogin = true
	srv.router = srv.routes()
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, devLoginRequest(dockerGateway, "localhost:20180", nil))
	require.Equal(t, http.StatusNotFound, rec.Code, "a private peer needs dev_login_docker")

	srv = newPortalTestServer(t)
	srv.cfg.PortalSSO.DevLogin = true
	srv.cfg.PortalSSO.DevLoginDocker = true
	srv.router = srv.routes()
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, devLoginRequest(dockerGateway, "localhost:20180", nil))
	require.Equal(t, http.StatusNoContent, rec.Code)
	require.NotEmpty(t, rec.Result().Cookies())

	for _, tc := range []struct {
		name    string
		remote  string
		host    string
		headers map[string]string
	}{
		{"public peer", "203.0.113.5:1", "localhost:20180", nil},
		{"private peer behind a proxy", dockerGateway, "localhost:20180", map[string]string{"X-Forwarded-For": "198.51.100.7"}},
		{"private peer with a public host", dockerGateway, "keirouter.example.com", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, devLoginRequest(tc.remote, tc.host, tc.headers))
			require.Equal(t, http.StatusNotFound, rec.Code)
		})
	}
}
