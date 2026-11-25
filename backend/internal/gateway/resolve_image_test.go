package gateway

import (
	"context"
	"net/http"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/dispatch"
	"github.com/mydisha/keirouter/backend/internal/market"
	"github.com/mydisha/keirouter/backend/internal/store"
)

func TestPriceImageStepsMarkup(t *testing.T) {
	steps := []dispatch.Target{
		{Provider: "wegoo", Model: "gpt-image-1", ImagePrice: 0.04, StepMarkupPercent: 50, StepMarkupSet: true},
		{Provider: "wegoo", Model: "gpt-image-1", ImagePrice: 0.04},
		{Provider: "openai", Model: "gpt-4o"},
	}
	out := priceImageSteps(steps, 100)
	require.Equal(t, 1.5, out[0].ImageBillMult)
	require.Equal(t, 2.0, out[1].ImageBillMult)
	require.Zero(t, out[2].ImageBillMult, "a step without an image price is left alone")
}

func TestPriceImageStepsIgnoresDiscount(t *testing.T) {
	steps := []dispatch.Target{{Provider: "wegoo", Model: "gpt-image-1", ImagePrice: 0.04, DiscountSet: true, DiscountPercent: 90}}
	require.Equal(t, 1.2, priceImageSteps(steps, 20)[0].ImageBillMult)
}

func TestImageFamily(t *testing.T) {
	cases := map[string]string{
		"gpt-image-2.5-4k":              "gpt-image",
		"grok-imagine-image-quality":    "grok-imagine",
		"gemini-3-pro-image-preview":    "gemini-image",
		"google/gemini-2.5-flash-image": "gemini-image",
		"openai/gpt-image-1":            "gpt-image",
		"gpt-4o":                        "",
		"gemini-2.5-pro":                "",
	}
	for model, want := range cases {
		require.Equal(t, want, imageFamily(model), model)
	}
}

func TestImageOnlyStep(t *testing.T) {
	require.True(t, imageOnlyStep(dispatch.Target{ImagePrice: 0.04}))
	require.False(t, imageOnlyStep(dispatch.Target{ImagePrice: 0.04, StaticInputPerM: 1, StaticOutputPerM: 2}))
	require.False(t, imageOnlyStep(dispatch.Target{ImagePrice: 0.04, MarketSlug: "openai/gpt-image-1"}))
	require.False(t, imageOnlyStep(dispatch.Target{}))
}

func TestTargetsFromChainCopiesImagePrice(t *testing.T) {
	two := 0.08
	ts := dispatch.TargetsFromChain(store.Chain{Steps: []store.ChainStep{
		{Provider: "wegoo", Model: "gpt-image-1", StaticPerImage: 0.04, StaticPerImage2K: &two},
		{Provider: "wegoo", Model: "gpt-image-1", StaticPerImage: 0.05},
	}})
	require.Equal(t, 0.04, ts[0].ImagePrice)
	require.Equal(t, 0.08, ts[0].ImagePrice2K)
	require.Equal(t, 0.05, ts[1].ImagePrice)
	require.Zero(t, ts[1].ImagePrice2K, "unset 2K means same as ImagePrice")
}

func createImageChain(t *testing.T, db *store.DB, name string, steps ...store.ChainStep) {
	t.Helper()
	now := time.Now().UTC()
	for i := range steps {
		steps[i].ID, steps[i].Position = name+"-s"+strconv.Itoa(i), i
	}
	require.NoError(t, db.Chains().Create(context.Background(), store.Chain{
		ID: name, TenantID: store.DefaultTenantID, Name: name, Strategy: "priority",
		Steps: steps, CreatedAt: now, UpdatedAt: now,
	}))
}

func TestChatRejectsImageOnlyChain(t *testing.T) {
	db, gw := newPublicTestGatewayWithDB(t)
	gw.marketCache = market.NewSnapshotCache()
	createImageChain(t, db, "pics", store.ChainStep{Provider: "openai", Model: "gpt-image-1", StaticPerImage: 0.04})
	ctx := context.Background()

	_, err := gw.resolveChatTargets(ctx, store.DefaultTenantID, "pics")
	require.Error(t, err)
	var bad badModelError
	require.True(t, asBadModel(err, &bad))
	require.Equal(t, http.StatusBadRequest, bad.HTTPStatus())

	// The media route still sees the image step, priced.
	res, err := gw.resolveTargets(ctx, store.DefaultTenantID, "pics")
	require.NoError(t, err)
	require.Len(t, res.Targets, 1)
	require.Equal(t, 0.04, res.Targets[0].ImagePrice)
	require.InDelta(t, 1.1, res.Targets[0].ImageBillMult, 1e-9, "global default markup 10%")
}

func TestChatDropsImageOnlyStepFromMixedChain(t *testing.T) {
	db, gw := newPublicTestGatewayWithDB(t)
	gw.marketCache = market.NewSnapshotCache()
	createImageChain(t, db, "mixed",
		store.ChainStep{Provider: "openai", Model: "gpt-image-1", StaticPerImage: 0.04},
		store.ChainStep{Provider: "openai", Model: "gpt-4o", StaticInputPerM: 1, StaticOutputPerM: 2},
	)
	res, err := gw.resolveChatTargets(context.Background(), store.DefaultTenantID, "mixed")
	require.NoError(t, err)
	require.Len(t, res.Targets, 1)
	require.Equal(t, "gpt-4o", res.Targets[0].Model)
}

func TestAdminRejectsDiscountOnImageStep(t *testing.T) {
	for _, step := range []string{`"discount_percent":10`, `"cost_multiplier":0.5`} {
		s := newChainPricingTestServer(t)
		code, _, body := postChain(t, s, `{"name":"img","steps":[
			{"provider":"openai","model":"gpt-image-1","static_per_image":0.04,`+step+`}]}`)
		require.Equal(t, http.StatusBadRequest, code, body)
		require.Contains(t, body, "Step gambar tidak bisa diberi diskon")
	}
}

func TestAdminRejectsMixedImageFamilies(t *testing.T) {
	s := newChainPricingTestServer(t)
	code, _, body := postChain(t, s, `{"name":"img","steps":[
		{"provider":"openai","model":"gpt-image-1","static_per_image":0.04},
		{"provider":"openai","model":"grok-imagine-image","static_per_image":0.05}]}`)
	require.Equal(t, http.StatusBadRequest, code, body)
	require.Contains(t, body, "Satu chain gambar hanya boleh berisi satu keluarga model")

	code, out, body := postChain(t, s, `{"name":"img2","steps":[
		{"provider":"openai","model":"gpt-image-1","static_per_image":0.04,"markup_percent":50},
		{"provider":"openai","model":"gpt-image-1.5","static_per_image":0.05,"static_per_image_2k":0.09}]}`)
	require.Equal(t, http.StatusCreated, code, body)
	code, body = patchChain(t, s, out["id"].(string), `{"steps":[
		{"provider":"openai","model":"gpt-image-1","static_per_image":0.04},
		{"provider":"openai","model":"gemini-3-pro-image-preview","static_per_image":0.05}]}`)
	require.Equal(t, http.StatusBadRequest, code, body)
	require.Contains(t, body, "Satu chain gambar hanya boleh berisi satu keluarga model")
}

func TestAdminRejectsUnknownImageModel(t *testing.T) {
	s := newChainPricingTestServer(t)
	code, _, body := postChain(t, s, `{"name":"img","steps":[
		{"provider":"openai","model":"dall-e-3","static_per_image":0.04}]}`)
	require.Equal(t, http.StatusBadRequest, code, body)
	require.Contains(t, body, "Model gambar tidak dikenali untuk harga per gambar")
}

// countingUpstream answers every request with a chat completion and counts
// the calls, so a test can prove a request never reached the provider.
func countingUpstream(calls *atomic.Int32) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"c1","model":"gpt-image-1","choices":[{"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}`))
	}
}

func TestImageOnlyChainRejectedOffImageRoutes(t *testing.T) {
	routes := []struct{ name, path, body string }{
		{"chat", "/v1/chat/completions", `{"model":"pics","messages":[{"role":"user","content":"hi"}]}`},
		{"gemini", "/v1beta/models/pics:generateContent", `{"contents":[{"parts":[{"text":"hi"}]}]}`},
		{"embeddings", "/v1/embeddings", `{"model":"pics","input":"hi"}`},
		{"image understanding", "/v1/images/understanding", `{"model":"pics","prompt":"what is this","images":["https://example.test/a.png"]}`},
	}
	for _, rt := range routes {
		t.Run(rt.name, func(t *testing.T) {
			var calls atomic.Int32
			h := newE2E(t, countingUpstream(&calls))
			createImageChain(t, h.db, "pics", store.ChainStep{Provider: "openai", Model: "gpt-image-1", StaticPerImage: 0.04})
			resp := h.post(t, rt.path, rt.body, h.apiKey)
			body := readAllString(t, resp)
			require.Equal(t, http.StatusBadRequest, resp.StatusCode, body)
			require.Contains(t, body, "pics")
			require.Zero(t, calls.Load(), "the upstream must never be called")
		})
	}
}

func TestAdminImageStepRoundTrip(t *testing.T) {
	s := newChainPricingTestServer(t)
	code, _, body := postChain(t, s, `{"name":"img","steps":[
		{"provider":"openai","model":"gpt-image-1","static_per_image":0.04,"static_per_image_2k":0.08,"markup_percent":50}]}`)
	require.Equal(t, http.StatusCreated, code, body)
	st := listChains(t, s)[0]["steps"].([]any)[0].(map[string]any)
	require.Equal(t, 0.04, st["static_per_image"])
	require.Equal(t, 0.08, st["static_per_image_2k"])
	require.Equal(t, 50.0, st["markup_percent"])
}
