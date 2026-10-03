package gateway

import (
	"encoding/json"
	"net/http"
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
