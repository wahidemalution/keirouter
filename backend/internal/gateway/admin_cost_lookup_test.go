package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAdminCostLookupReportOff(t *testing.T) {
	s, _ := newCustomProviderTestServer(t)
	id := createSubrouter(t, s)
	code, body := callCustomProvider(t, s.adminCostLookupReport, http.MethodGet, id, nil)
	require.Equal(t, http.StatusBadRequest, code, body)
}

func TestAdminCostLookupReportBadDay(t *testing.T) {
	s, _ := newCustomProviderTestServer(t)
	id := createSubrouter(t, s)
	code, body := callCustomProvider(t, s.adminUpdateCustomProvider, http.MethodPatch, id, enableBody())
	require.Equal(t, http.StatusOK, code, body)
	code, body = callCustomProviderURL(t, s.adminCostLookupReport, "/custom-providers/"+id+"/cost-lookup/report?day=31-10-2026", id)
	require.Equal(t, http.StatusBadRequest, code, body)
}

func TestAdminCostLookupReportUnknownProvider(t *testing.T) {
	s, _ := newCustomProviderTestServer(t)
	code, _ := callCustomProvider(t, s.adminCostLookupReport, http.MethodGet, "custom-openai-missing", nil)
	require.Equal(t, http.StatusNotFound, code)
}

func TestAdminCostLookupReportOK(t *testing.T) {
	s, db := newCustomProviderTestServer(t)
	id := createSubrouter(t, s)
	code, body := callCustomProvider(t, s.adminUpdateCustomProvider, http.MethodPatch, id, enableBody())
	require.Equal(t, http.StatusOK, code, body)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/log/self/stat", r.URL.Path)
		require.Equal(t, "Bearer sys-secret-token", r.Header.Get("Authorization"))
		require.Equal(t, "1793404800", r.URL.Query().Get("start_timestamp"))
		_, _ = w.Write([]byte(`{"data":{"quota":101715,"rpm":0,"token":18701168,"tpm":0},"message":"","success":true}`))
	}))
	defer srv.Close()
	// The editor rejects loopback URLs; point the stored provider at the fake
	// log server directly.
	p, err := db.CustomProviders().GetProvider(context.Background(), id)
	require.NoError(t, err)
	p.CostLookupBaseURL = srv.URL
	require.NoError(t, db.CustomProviders().UpdateProvider(context.Background(), p))

	code, body = callCustomProviderURL(t, s.adminCostLookupReport, "/custom-providers/"+id+"/cost-lookup/report?day=2026-10-31", id)
	require.Equal(t, http.StatusOK, code, body)
	var r struct {
		Day          string  `json:"day"`
		SubrouterUSD float64 `json:"subrouter_usd"`
		Warn         bool    `json:"warn"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &r))
	require.Equal(t, "2026-10-31", r.Day)
	require.InDelta(t, 0.20343, r.SubrouterUSD, 1e-12)
	require.True(t, r.Warn, "nothing recorded on the keirouter side")
}

func TestAdminCostLookupReportUpstreamDown(t *testing.T) {
	s, db := newCustomProviderTestServer(t)
	id := createSubrouter(t, s)
	code, body := callCustomProvider(t, s.adminUpdateCustomProvider, http.MethodPatch, id, enableBody())
	require.Equal(t, http.StatusOK, code, body)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) }))
	defer srv.Close()
	p, err := db.CustomProviders().GetProvider(context.Background(), id)
	require.NoError(t, err)
	p.CostLookupBaseURL = srv.URL
	require.NoError(t, db.CustomProviders().UpdateProvider(context.Background(), p))

	code, body = callCustomProviderURL(t, s.adminCostLookupReport, "/custom-providers/"+id+"/cost-lookup/report", id)
	require.Equal(t, http.StatusBadGateway, code, body)
	require.NotContains(t, body, "sys-secret-token")
}
