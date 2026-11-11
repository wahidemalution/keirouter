package gateway

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAdminChainDiscountStepRoundTrip(t *testing.T) {
	s := newChainPricingTestServer(t)
	code, out, body := postChain(t, s, `{"name":"disc","steps":[
		{"provider":"openai","model":"gpt-4o","static_input_per_m":0.28,"static_output_per_m":0.42,
		 "static_cache_read_per_m":0.028,"discount_percent":90,"cost_multiplier":0.01}]}`)
	require.Equal(t, http.StatusCreated, code, body)
	st := listChains(t, s)[0]["steps"].([]any)[0].(map[string]any)
	require.Equal(t, 90.0, st["discount_percent"])
	require.Equal(t, 0.01, st["cost_multiplier"])
	require.Nil(t, st["markup_percent"])

	code, body = patchChain(t, s, out["id"].(string), `{"steps":[
		{"provider":"openai","model":"gpt-4o","static_input_per_m":1,"static_output_per_m":2,"discount_percent":50}]}`)
	require.Equal(t, http.StatusOK, code, body)
	st = listChains(t, s)[0]["steps"].([]any)[0].(map[string]any)
	require.Equal(t, 50.0, st["discount_percent"])
	require.Nil(t, st["cost_multiplier"])
}

func TestAdminChainDiscountStepRejects(t *testing.T) {
	price := `"static_input_per_m":1,"static_output_per_m":2,`
	cases := []struct{ name, step, want string }{
		{"discount without price", `"discount_percent":90`, "only apply to a static-price step"},
		{"cost without discount", price + `"cost_multiplier":0.01`, "cost_multiplier only applies to a discount step"},
		{"discount 100", price + `"discount_percent":100`, "discount_percent must be at least 0 and below 100"},
		{"discount negative", price + `"discount_percent":-1`, "discount_percent must be at least 0 and below 100"},
		{"markup with discount", price + `"discount_percent":90,"markup_percent":10`, "markup does not apply to a discount step"},
		{"cost zero", price + `"discount_percent":90,"cost_multiplier":0`, "cost_multiplier must be above 0 and at most 100"},
		{"cost too high", price + `"discount_percent":90,"cost_multiplier":101`, "cost_multiplier must be above 0 and at most 100"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newChainPricingTestServer(t)
			code, _, body := postChain(t, s, `{"name":"bad","steps":[{"provider":"openai","model":"gpt-4o",`+tc.step+`}]}`)
			require.Equal(t, http.StatusBadRequest, code, body)
			require.Contains(t, body, tc.want)
		})
	}
}
