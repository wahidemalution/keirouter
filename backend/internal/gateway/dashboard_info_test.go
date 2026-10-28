package gateway

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestGatewayInfo_ReportsHostClockAndZone(t *testing.T) {
	s := newPortalTestServer(t)
	rec := httptest.NewRecorder()
	s.adminGatewayInfo(rec, httptest.NewRequest(http.MethodGet, "/gateway/info", nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	m := decodeBody(t, rec)

	at, err := time.Parse(time.RFC3339Nano, m["server_time"].(string))
	require.NoError(t, err)
	require.WithinDuration(t, time.Now(), at, 5*time.Second)
	name, offset := time.Now().Zone()
	require.Equal(t, name, m["tz_name"])
	require.EqualValues(t, offset, m["tz_offset_s"])
}
