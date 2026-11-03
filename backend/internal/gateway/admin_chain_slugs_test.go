package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func chainEntryByName(t *testing.T, chains []map[string]any, name string) map[string]any {
	t.Helper()
	for _, c := range chains {
		if c["name"] == name {
			return c
		}
	}
	t.Fatalf("chain %q not found in list", name)
	return nil
}

func TestAdminChainMarketSlugsRoundTrip(t *testing.T) {
	s, db, cookie := newMarketPricingTestServer(t)
	s.chains = db.Chains()

	rec := marketRequest(t, s, cookie, http.MethodPost, "/api/chains",
		`{"name":"slug-chain","steps":[{"provider":"openai","model":"gpt-4o"}],"market_slugs":["a/one","b/two"]}`)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	var created struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created))
	require.NotEmpty(t, created.ID)

	// A chain created without slugs must still serialize as a JSON array, not null.
	rec = marketRequest(t, s, cookie, http.MethodPost, "/api/chains",
		`{"name":"plain-chain","steps":[{"provider":"openai","model":"gpt-4o"}]}`)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	var list struct {
		Chains []map[string]any `json:"chains"`
	}
	require.NoError(t, json.Unmarshal(marketRequest(t, s, cookie, http.MethodGet, "/api/chains", "").Body.Bytes(), &list))
	require.Len(t, list.Chains, 2)
	require.Equal(t, []any{"a/one", "b/two"}, chainEntryByName(t, list.Chains, "slug-chain")["market_slugs"])
	require.Equal(t, []any{}, chainEntryByName(t, list.Chains, "plain-chain")["market_slugs"])

	rec = marketRequest(t, s, cookie, http.MethodPatch, "/api/chains/"+created.ID, `{"market_slugs":[]}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	list.Chains = nil
	require.NoError(t, json.Unmarshal(marketRequest(t, s, cookie, http.MethodGet, "/api/chains", "").Body.Bytes(), &list))
	require.Equal(t, []any{}, chainEntryByName(t, list.Chains, "slug-chain")["market_slugs"])
}

func TestAdminChainRejectsProviderSlugForms(t *testing.T) {
	s, db, cookie := newMarketPricingTestServer(t)
	s.chains = db.Chains()

	post := func(name, step, chainSlugs string) *httptest.ResponseRecorder {
		body := `{"name":"` + name + `","steps":[{"provider":"openai","model":"gpt-4o","market_slug":"` + step + `"}],"market_slugs":` + chainSlugs + `}`
		return marketRequest(t, s, cookie, http.MethodPost, "/api/chains", body)
	}

	rec := post("bad-step", "surplus-fast@openrouter:m", "[]")
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "surplus-fast has no per-provider form")

	rec = post("bad-chain", "", `["surplus-trusted@openrouter:m"]`)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "a chain cannot pin a Surplus provider")

	rec = post("bad-shape", "surplus-trusted@openrouter:", "[]")
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())

	rec = post("ok-step", "surplus-trusted@openrouter:m", "[]")
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	rec = post("mixed-case", "surplus-trusted@OpenRouter:m", "[]")
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var created struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created))

	// Update path: a chain-level provider slug is rejected on PATCH too.
	rec = marketRequest(t, s, cookie, http.MethodPatch, "/api/chains/"+created.ID,
		`{"market_slugs":["surplus-trusted@openrouter:m"]}`)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	rec = marketRequest(t, s, cookie, http.MethodPatch, "/api/chains/"+created.ID,
		`{"steps":[{"provider":"openai","model":"gpt-4o","market_slug":"surplus-fast@openrouter:m"}]}`)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())

	var list struct {
		Chains []map[string]any `json:"chains"`
	}
	require.NoError(t, json.Unmarshal(marketRequest(t, s, cookie, http.MethodGet, "/api/chains", "").Body.Bytes(), &list))
	require.Len(t, list.Chains, 2)
	for _, name := range []string{"ok-step", "mixed-case"} {
		steps := chainEntryByName(t, list.Chains, name)["steps"].([]any)
		require.Equal(t, "surplus-trusted@openrouter:m", steps[0].(map[string]any)["market_slug"], name)
	}
}
