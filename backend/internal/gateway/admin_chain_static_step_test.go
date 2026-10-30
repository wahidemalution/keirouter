package gateway

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAdminChainStaticStepRoundTrip(t *testing.T) {
	s := newChainPricingTestServer(t)
	code, out, body := postChain(t, s, `{"name":"mixed","steps":[
		{"provider":"openai","model":"gpt-4o","market_slug":"surplus-fast:gpt-4o"},
		{"provider":"openai","model":"gpt-4o","static_input_per_m":0.5,"static_output_per_m":2,"markup_percent":25}]}`)
	require.Equal(t, http.StatusCreated, code, body)

	chains := listChains(t, s)
	require.Len(t, chains, 1)
	steps := chains[0]["steps"].([]any)
	st := steps[1].(map[string]any)
	require.Equal(t, 0.5, st["static_input_per_m"])
	require.Equal(t, 2.0, st["static_output_per_m"])
	require.Equal(t, 25.0, st["markup_percent"])
	require.Nil(t, st["static_cache_read_per_m"])
	require.Nil(t, steps[0].(map[string]any)["markup_percent"])

	code, body = patchChain(t, s, out["id"].(string), `{"steps":[
		{"provider":"openai","model":"gpt-4o","static_input_per_m":1,"static_output_per_m":3,"static_cache_write_per_m":1.5}]}`)
	require.Equal(t, http.StatusOK, code, body)
	st = listChains(t, s)[0]["steps"].([]any)[0].(map[string]any)
	require.Equal(t, 1.0, st["static_input_per_m"])
	require.Equal(t, 1.5, st["static_cache_write_per_m"])
	require.Nil(t, st["markup_percent"])
}

func TestAdminChainStaticStepRejects(t *testing.T) {
	cases := []struct{ name, step, want string }{
		{"slug and static", `"market_slug":"surplus-fast:gpt-4o","static_input_per_m":1,"static_output_per_m":2`, "market slug or a static price, not both"},
		{"input only", `"static_input_per_m":1`, "static input and output price must be greater than 0"},
		{"negative", `"static_input_per_m":-1,"static_output_per_m":2`, "rates must be finite and non-negative"},
		{"markup without price", `"markup_percent":25`, "only apply to a static-price step"},
		{"cache without price", `"static_cache_read_per_m":0.1`, "only apply to a static-price step"},
		{"zero cache override", `"static_input_per_m":1,"static_output_per_m":2,"static_cache_read_per_m":0`, "cache overrides must be greater than 0"},
		{"markup too high", `"static_input_per_m":1,"static_output_per_m":2,"markup_percent":2000`, "markup_percent must be between 0 and 1000"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newChainPricingTestServer(t)
			code, _, body := postChain(t, s, `{"name":"bad","steps":[{"provider":"openai","model":"gpt-4o",`+tc.step+`}]}`)
			require.Equal(t, http.StatusBadRequest, code, body)
			require.Contains(t, body, tc.want)
			require.Contains(t, body, "step 1:")
		})
	}
}

func TestAdminChainStaticStepRejectsOnUpdate(t *testing.T) {
	s := newChainPricingTestServer(t)
	code, out, body := postChain(t, s, `{"name":"ok","steps":[{"provider":"openai","model":"gpt-4o"}]}`)
	require.Equal(t, http.StatusCreated, code, body)
	code, body = patchChain(t, s, out["id"].(string), `{"steps":[{"provider":"openai","model":"gpt-4o"},
		{"provider":"openai","model":"gpt-4o","market_slug":"x/y","static_input_per_m":1,"static_output_per_m":2}]}`)
	require.Equal(t, http.StatusBadRequest, code, body)
	require.Contains(t, body, "step 2: set a market slug or a static price, not both")
}
