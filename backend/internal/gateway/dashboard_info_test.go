package gateway

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestTimelineBucketStatus(t *testing.T) {
	cases := []struct {
		name                          string
		requests, failures, throttled int64
		rank                          int
		want                          string
	}{
		{"no traffic", 0, 0, 0, 0, "idle"},
		{"clean", 100, 1, 0, 1, "ok"},
		{"rate limited", 100, 0, 3, 1, "degraded"},
		{"elevated failures", 100, 6, 0, 1, "degraded"},
		{"degraded snapshot", 100, 0, 0, 2, "degraded"},
		{"heavy failures", 100, 30, 0, 1, "down"},
		{"unhealthy snapshot", 100, 0, 0, 3, "down"},
	}
	for _, tc := range cases {
		if got := timelineBucketStatus(tc.requests, tc.failures, tc.throttled, tc.rank); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestParseTimelineBuckets(t *testing.T) {
	for raw, want := range map[string]int{"": 24, "42": 42, "3": 24, "500": 24, "abc": 24} {
		if got := parseTimelineBuckets(raw); got != want {
			t.Errorf("parseTimelineBuckets(%q) = %d, want %d", raw, got, want)
		}
	}
}

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
