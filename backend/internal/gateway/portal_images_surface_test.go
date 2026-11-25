package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/store"
)

func chatStep() store.ChainStep {
	return store.ChainStep{Provider: "openai", Model: "gpt-4o", StaticInputPerM: 1, StaticOutputPerM: 2}
}

// An image-only chain serves the Buat Gambar page only: no chat model list
// advertises it. A chain with a chat step stays listed.
func TestImageOnlyChainsHiddenFromModelLists(t *testing.T) {
	t.Run("v1 models", func(t *testing.T) {
		gw, apiKey := newModelDiscoveryTestGateway(t, nil)
		createImageChain(t, gw.db, "chatty", chatStep())
		createImageChain(t, gw.db, "pics", store.ChainStep{Provider: "openai", Model: "gpt-image-1", StaticPerImage: 0.04})
		createImageChain(t, gw.db, "mixed", store.ChainStep{Provider: "openai", Model: "gpt-image-1", StaticPerImage: 0.04}, chatStep())
		models := modelIDsFromResponse(t, getAuthedJSON(t, gw, apiKey, "/v1/models"))
		require.ElementsMatch(t, []string{"chatty", "mixed"}, models)
	})

	t.Run("public models", func(t *testing.T) {
		db, gw := newPublicTestGatewayWithDB(t)
		createImageChain(t, db, "chatty", chatStep())
		createImageChain(t, db, "pics", store.ChainStep{Provider: "openai", Model: "gpt-image-1", StaticPerImage: 0.04})
		rec := httptest.NewRecorder()
		gw.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/public/models", nil))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var out struct {
			Models []struct {
				Name string `json:"name"`
			} `json:"models"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
		require.Len(t, out.Models, 1)
		require.Equal(t, "chatty", out.Models[0].Name)
	})

	t.Run("portal models", func(t *testing.T) {
		f := newPortalImageFixture(t) // has the image-only "pics" chain
		createImageChain(t, f.srv.db, "chatty", chatStep())
		tok, _ := f.user("sub-a", picsPrice)
		mux := chi.NewRouter()
		mux.Group(func(r chi.Router) {
			r.Use(f.srv.portalSessionMiddleware)
			r.Get("/portal/api/models", f.srv.handlePortalModels)
		})
		req := httptest.NewRequest(http.MethodGet, "/portal/api/models", nil)
		req.AddCookie(&http.Cookie{Name: portalSessionCookie, Value: tok})
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.Contains(t, rec.Body.String(), `"chatty"`)
		require.NotContains(t, rec.Body.String(), `"pics"`)
	})
}

// The routes that spend balance or delete jobs refuse cross-site requests.
func TestPortalImageWriteRoutesRefuseCrossSite(t *testing.T) {
	f := newPortalImageFixture(t)
	f.srv.cfg.PortalSSO.RedirectURL = "https://portal.example.com/portal/auth/google/callback"
	tok, keyID := f.user("sub-a", 10*picsPrice)
	body := `{"chain_id":"pics","expected_price_micro":44000,"prompt":"x","n":1}`

	send := func(method, path, ctype, origin string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Host = "gw.internal:8080"
		if ctype != "" {
			req.Header.Set("Content-Type", ctype)
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		req.AddCookie(&http.Cookie{Name: portalSessionCookie, Value: tok})
		rec := httptest.NewRecorder()
		f.mux.ServeHTTP(rec, req)
		return rec
	}

	for _, ctype := range []string{"", "text/plain", "application/x-www-form-urlencoded", "multipart/form-data; boundary=x"} {
		rec := send(http.MethodPost, "/portal/api/images", ctype, "")
		require.Equal(t, http.StatusUnsupportedMediaType, rec.Code, ctype)
		require.Equal(t, "json_required", errorCode(t, rec))
	}
	for _, origin := range []string{"https://evil.example", "null", "https://gw.internal:8080.evil.example"} {
		rec := send(http.MethodPost, "/portal/api/images", "application/json", origin)
		require.Equal(t, http.StatusForbidden, rec.Code, origin)
		require.Equal(t, "bad_origin", errorCode(t, rec))
	}
	require.Empty(t, f.list(tok))
	require.Zero(t, f.spend(keyID))

	// Same host, the configured portal URL, or no Origin at all pass.
	var jobs []portalJobJSON
	for i, origin := range []string{"http://gw.internal:8080", "https://portal.example.com", ""} {
		rec := send(http.MethodPost, "/portal/api/images", "application/json; charset=utf-8", origin)
		require.Equal(t, http.StatusCreated, rec.Code, origin+": "+rec.Body.String())
		jobs = f.waitDone(tok, i+1)
	}

	rec := send(http.MethodDelete, "/portal/api/images/"+jobs[0].ID, "", "https://evil.example")
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Equal(t, "bad_origin", errorCode(t, rec))
	rec = send(http.MethodDelete, "/portal/api/images/"+jobs[0].ID, "", "http://gw.internal:8080")
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
}

func TestPortalImageResponseFormatPerFamily(t *testing.T) {
	f := newPortalImageFixture(t)
	createImageChain(t, f.srv.db, "grok-pics", store.ChainStep{Provider: "xai", Model: "grok-imagine-image", StaticPerImage: 0.02, StaticPerImage2K: &grok2K})
	createImageChain(t, f.srv.db, "gem-pics", store.ChainStep{Provider: "google", Model: "gemini-2.5-flash-image", StaticPerImage: 0.02})
	tok, _ := f.user("sub-a", 100*picsPrice)
	for i, c := range []struct{ chain, want string }{{"pics", "b64_json"}, {"grok-pics", "b64_json"}, {"gem-pics", ""}} {
		f.submit(tok, `{"chain_id":"`+c.chain+`","expected_price_micro":`+map[string]string{"pics": "44000", "grok-pics": "22000", "gem-pics": "22000"}[c.chain]+`,"prompt":"x","n":1}`)
		f.waitDone(tok, i+1)
		require.Equal(t, c.want, f.gen.last().ResponseFormat, c.chain)
	}
}
