package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/market"
)

func TestAdminModelReference(t *testing.T) {
	refs := market.NewReferenceCache()
	refs.Replace(map[string]market.Reference{
		"deepseek-v4-flash": {InputPerM: 0.28, OutputPerM: 0.42, CacheReadPerM: 0.028, Provider: "deepseek"},
	})
	s := &Server{references: refs}

	get := func(q string) (int, map[string]any) {
		rec := httptest.NewRecorder()
		s.adminModelReference(rec, httptest.NewRequest(http.MethodGet, "/model-reference"+q, nil))
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}

	code, out := get("?model=deepseek/deepseek-v4-flash")
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, 0.28, out["input_per_m"])
	require.Equal(t, 0.42, out["output_per_m"])
	require.Equal(t, 0.028, out["cache_read_per_m"])
	require.Nil(t, out["cache_write_per_m"])
	require.Equal(t, "deepseek", out["provider"])

	code, _ = get("?model=unknown-model")
	require.Equal(t, http.StatusNotFound, code)
	code, _ = get("")
	require.Equal(t, http.StatusBadRequest, code)
}
