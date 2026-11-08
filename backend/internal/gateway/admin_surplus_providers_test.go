package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/market"
)

type surplusProvidersResp struct {
	Providers []surplusProviderLabel `json:"providers"`
	Complete  bool                   `json:"complete"`
}

func getSurplusProviders(t *testing.T, gw *Server, model string) (int, surplusProvidersResp) {
	t.Helper()
	rec := httptest.NewRecorder()
	gw.adminSurplusProviders(rec, httptest.NewRequest(http.MethodGet, "/market-pricing/surplus-providers?model="+model, nil))
	var body surplusProvidersResp
	if rec.Code == http.StatusOK {
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	}
	return rec.Code, body
}

// The endpoint lists every provider in the model's book, trusted or not, and
// fetches the book once per TTL.
func TestAdminSurplusProvidersListsBook(t *testing.T) {
	_, gw := newPublicTestGatewayWithDB(t)
	gw.marketCache = market.NewSnapshotCache()
	f := newFakeSurplus(t)
	gw.surplusURL = f.srv.URL + "/api/markets"

	code, body := getSurplusProviders(t, gw, "m")
	require.Equal(t, http.StatusOK, code)
	require.True(t, body.Complete)
	require.Equal(t, []surplusProviderLabel{
		{ID: "alpha", Name: "Alpha"}, {ID: "beta", Name: "Beta"}, {ID: "gamma", Name: "Gamma"}, {ID: "shady", Name: "Shady"},
	}, body.Providers)
	require.Equal(t, "Beta", gw.marketCache.ProviderLabel("beta"))

	_, _ = getSurplusProviders(t, gw, "m")
	require.EqualValues(t, 1, f.bookHits.Load())
}

// A failed fetch falls back to the labels the market sync already knows.
func TestAdminSurplusProvidersFallsBackToKnownLabels(t *testing.T) {
	_, gw := newPublicTestGatewayWithDB(t)
	gw.marketCache = market.NewSnapshotCache()
	gw.marketCache.MergeProviderLabels(map[string]string{"bedrock": "Amazon Bedrock"})
	f := newFakeSurplus(t)
	f.failBook.Store(true)
	gw.surplusURL = f.srv.URL + "/api/markets"

	code, body := getSurplusProviders(t, gw, "m")
	require.Equal(t, http.StatusOK, code)
	require.False(t, body.Complete)
	require.Equal(t, []surplusProviderLabel{{ID: "bedrock", Name: "Amazon Bedrock"}}, body.Providers)
}

func TestAdminSurplusProvidersRejectsBadModel(t *testing.T) {
	_, gw := newPublicTestGatewayWithDB(t)
	code, _ := getSurplusProviders(t, gw, "..%2Fsecret")
	require.Equal(t, http.StatusBadRequest, code)
}
