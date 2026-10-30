package market

import (
	"slices"
	"strings"
	"testing"
)

// List is $0.2/M in, $1.2/M out after scaling. Cheapest eligible is the
// untrusted inferhub offer (sum 14700); 30% tolerance admits sums up to 19110.
const fastBookFixture = `{"model":"m","offers":[
  {"provider_id":"openai","price_input_per_1m":2000,"price_output_per_1m":12000,"direct_input_per_1m":200000,"direct_output_per_1m":1200000,"available":false,"healthy":false,"trusted":true},
  {"provider_id":"inferhub","price_input_per_1m":2100,"price_output_per_1m":12600,"direct_input_per_1m":200000,"direct_output_per_1m":1200000,"available":true,"healthy":true,"trusted":false},
  {"provider_id":"x","price_input_per_1m":2100,"price_output_per_1m":12000,"direct_input_per_1m":200000,"direct_output_per_1m":1200000,"available":true,"healthy":false,"trusted":true},
  {"provider_id":"bedrock","price_input_per_1m":2495,"price_output_per_1m":14969,"direct_input_per_1m":200000,"direct_output_per_1m":1200000,"available":true,"healthy":true,"trusted":true},
  {"provider_id":"google","price_input_per_1m":2600,"price_output_per_1m":15600,"direct_input_per_1m":200000,"direct_output_per_1m":1200000,"available":true,"healthy":true,"trusted":true},
  {"provider_id":"google","price_input_per_1m":2700,"price_output_per_1m":16000,"direct_input_per_1m":200000,"direct_output_per_1m":1200000,"available":true,"healthy":true,"trusted":true},
  {"provider_id":"venice","price_input_per_1m":3000,"price_output_per_1m":18000,"direct_input_per_1m":200000,"direct_output_per_1m":1200000,"available":true,"healthy":true,"trusted":false}
]}`

func ms(v float64) *float64 { return &v }

func fastPerf() map[string]ProviderPerf {
	return map[string]ProviderPerf{
		"inferhub": {TTFTP50MS: ms(2000), LatencyP50MS: ms(3000)},
		"bedrock":  {TTFTP50MS: ms(300), LatencyP50MS: ms(1200)},
		"google":   {TTFTP50MS: ms(300), LatencyP50MS: ms(900)},
		"venice":   {TTFTP50MS: ms(100), LatencyP50MS: ms(500)},
	}
}

func TestParseSurplusFastBookRanksBandByTTFTThenLatency(t *testing.T) {
	q, err := ParseSurplusFastBook(strings.NewReader(fastBookFixture), fastPerf(), 30, 3, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	// venice is fastest but asks more than 30% over the cheapest, so it only
	// follows the band as overflow. The unavailable and unhealthy offers never
	// qualify.
	if want := []string{"google", "bedrock", "inferhub", "venice"}; !slices.Equal(q.ProviderOrder, want) {
		t.Fatalf("order = %v, want %v", q.ProviderOrder, want)
	}
	if want := []string{"bedrock", "google", "inferhub", "venice"}; !slices.Equal(q.Providers, want) {
		t.Fatalf("allow-list = %v, want %v", q.Providers, want)
	}
	if q.Eligible != 5 {
		t.Fatalf("eligible = %d, want 5", q.Eligible)
	}
	// The shown rate is the first provider's cheapest band offer.
	if !approx(q.BestIn, 0.0026) || !approx(q.BestOut, 0.0156) {
		t.Fatalf("best = (%v,%v), want google's 0.0026/0.0156", q.BestIn, q.BestOut)
	}
	// The ceiling admits the least discounted kept offer (venice 3000/18000,
	// 98.5% off) minus the 2-point margin.
	if q.MinDiscountPct != 96 {
		t.Fatalf("ceiling = %d, want 96", q.MinDiscountPct)
	}
	if !approx(q.CapIn, 0.008) || !approx(q.CapOut, 0.048) {
		t.Fatalf("cap = (%v,%v), want list × 4%%", q.CapIn, q.CapOut)
	}
}

func TestParseSurplusFastBookZeroToleranceLeadsWithCheapest(t *testing.T) {
	q, err := ParseSurplusFastBook(strings.NewReader(fastBookFixture), fastPerf(), 0, 1, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The band is inferhub alone; depth 1 adds only the next price level.
	if want := []string{"inferhub", "bedrock"}; !slices.Equal(q.ProviderOrder, want) {
		t.Fatalf("order = %v, want %v", q.ProviderOrder, want)
	}
	if !approx(q.BestIn, 0.0021) || !approx(q.BestOut, 0.0126) {
		t.Fatalf("best = (%v,%v), want inferhub", q.BestIn, q.BestOut)
	}
}

// The cheapest offer is often one Surplus will not route to. Overflow levels
// keep a request from failing when the whole band is refused.
func TestParseSurplusFastBookOverflowFollowsBandCheapestFirst(t *testing.T) {
	q, err := ParseSurplusFastBook(strings.NewReader(fastBookFixture), fastPerf(), 0, 10, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	// venice is the fastest overflow provider but the dearest, so it goes last.
	if want := []string{"inferhub", "bedrock", "google", "venice"}; !slices.Equal(q.ProviderOrder, want) {
		t.Fatalf("order = %v, want %v", q.ProviderOrder, want)
	}
	if q.MinDiscountPct != 96 {
		t.Fatalf("ceiling = %d, want 96 covering every kept offer", q.MinDiscountPct)
	}
}

func TestParseSurplusFastBookDepthZeroKeepsAllWithoutCeiling(t *testing.T) {
	q, err := ParseSurplusFastBook(strings.NewReader(fastBookFixture), fastPerf(), 0, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if q.Eligible != 5 || q.MinDiscountPct != 0 {
		t.Fatalf("eligible = %d ceiling = %d, want 5 and none", q.Eligible, q.MinDiscountPct)
	}
	if !approx(q.CapIn, 0.003) || !approx(q.CapOut, 0.018) {
		t.Fatalf("cap = (%v,%v), want the dearest offer", q.CapIn, q.CapOut)
	}
}

func TestParseSurplusFastBookWithoutPerfRanksByPrice(t *testing.T) {
	q, err := ParseSurplusFastBook(strings.NewReader(fastBookFixture), nil, 30, 3, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"inferhub", "bedrock", "google", "venice"}; !slices.Equal(q.ProviderOrder, want) {
		t.Fatalf("order = %v, want %v", q.ProviderOrder, want)
	}
	if !approx(q.BestIn, 0.0021) || !approx(q.BestOut, 0.0126) {
		t.Fatalf("best = (%v,%v), want the cheapest", q.BestIn, q.BestOut)
	}
}

func TestParseSurplusFastBookUnknownPerfRanksLast(t *testing.T) {
	perf := fastPerf()
	perf["inferhub"] = ProviderPerf{}
	perf["google"] = ProviderPerf{LatencyP50MS: ms(100)}
	q, err := ParseSurplusFastBook(strings.NewReader(fastBookFixture), perf, 30, 3, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	// bedrock has a TTFT; google only a latency; inferhub nothing.
	if want := []string{"bedrock", "google", "inferhub", "venice"}; !slices.Equal(q.ProviderOrder, want) {
		t.Fatalf("order = %v, want %v", q.ProviderOrder, want)
	}
}

func TestParseSurplusFastBookSkipsCeilingWithoutListPrice(t *testing.T) {
	book := `{"offers":[
	  {"provider_id":"a","price_input_per_1m":1000,"price_output_per_1m":2000,"direct_input_per_1m":100000,"direct_output_per_1m":200000,"available":true,"healthy":true},
	  {"provider_id":"b","price_input_per_1m":1100,"price_output_per_1m":2100,"available":true,"healthy":true}
	]}`
	q, err := ParseSurplusFastBook(strings.NewReader(book), nil, 30, 3, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if q.MinDiscountPct != 0 {
		t.Fatalf("ceiling = %d, want none: it would exclude b", q.MinDiscountPct)
	}
	if !approx(q.CapIn, 0.0011) || !approx(q.CapOut, 0.0021) {
		t.Fatalf("cap = (%v,%v), want the dearest band offer", q.CapIn, q.CapOut)
	}
}

func TestParseSurplusFastBookEmpty(t *testing.T) {
	q, err := ParseSurplusFastBook(strings.NewReader(`{"offers":[{"provider_id":"a","available":false,"healthy":true,"price_input_per_1m":1,"price_output_per_1m":1}]}`), nil, 30, 3, 0, nil)
	if err != nil || q.Found {
		t.Fatalf("q = %+v err = %v, want not found", q, err)
	}
}

func TestParseSurplusPerformance(t *testing.T) {
	perf, err := ParseSurplusPerformance(strings.NewReader(`{"providers":[
	  {"provider":"deepseek","ttft_p50_ms":2254,"p50_latency_ms":2254},
	  {"provider":"capminal","ttft_p50_ms":null,"p50_latency_ms":4108}
	]}`))
	if err != nil {
		t.Fatal(err)
	}
	if p := perf["deepseek"]; p.TTFTP50MS == nil || *p.TTFTP50MS != 2254 {
		t.Fatalf("deepseek = %+v", p)
	}
	if p := perf["capminal"]; p.TTFTP50MS != nil || p.LatencyP50MS == nil || *p.LatencyP50MS != 4108 {
		t.Fatalf("capminal = %+v, want unknown TTFT", p)
	}
}

func TestSurplusPerformanceURL(t *testing.T) {
	for in, want := range map[string]string{
		"":                            "https://api.surplusintelligence.ai/api/analytics/performance/m%2Fx",
		"https://h.test/api/markets/": "https://h.test/api/analytics/performance/m%2Fx",
		"https://h.test/api/markets":  "https://h.test/api/analytics/performance/m%2Fx",
	} {
		if got := SurplusPerformanceURL(in, "m/x"); got != want {
			t.Errorf("SurplusPerformanceURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCutUpstreamCostSlug(t *testing.T) {
	for slug, want := range map[string]string{
		"surplus-trusted:a": "a", "surplus-fast:b": "b", "surplus:c": "", "surplus-fast:": "", "d": "",
	} {
		got, ok := CutUpstreamCostSlug(slug)
		if got != want || ok != (want != "") {
			t.Errorf("CutUpstreamCostSlug(%q) = %q,%v", slug, got, ok)
		}
	}
}

func TestFastModelKeysSlugAndCarriesOrder(t *testing.T) {
	m := TrustedQuote{Found: true, BestIn: 1, BestOut: 2, ProviderOrder: []string{"b", "a"}}.FastModel("m")
	if m.Slug != "surplus-fast:m" || !slices.Equal(m.ProviderOrder, []string{"b", "a"}) {
		t.Fatalf("model = %+v", m)
	}
}

func TestParseSurplusFastBookSkipsBlockedProvider(t *testing.T) {
	q, err := ParseSurplusFastBook(strings.NewReader(fastBookFixture), fastPerf(), 30, 3, 0, []string{"inferhub"})
	if err != nil {
		t.Fatal(err)
	}
	// Without inferhub the cheapest is bedrock (sum 17464); the band now
	// reaches venice (21000 <= 22703), the fastest.
	if want := []string{"venice", "google", "bedrock"}; !slices.Equal(q.ProviderOrder, want) {
		t.Fatalf("order = %v, want %v", q.ProviderOrder, want)
	}
	if slices.Contains(q.Providers, "inferhub") {
		t.Fatalf("allow-list = %v still has the blocked provider", q.Providers)
	}
}

// Shaped like the deepseek-v4.1-flash book on 2026-10-29: the cheap offers
// beyond dashscope-cn belonged to oneapi (10.9% uptime, 18s TTFT) and inferhub
// (79% uptime), so surplus-fast routed most requests to them.
const slowSellersBook = `{"offers":[
  {"provider_id":"dashscope-cn","price_input_per_1m":179,"price_output_per_1m":1374,"direct_input_per_1m":45000,"direct_output_per_1m":500000,"available":true,"healthy":true},
  {"provider_id":"oneapi","price_input_per_1m":144,"price_output_per_1m":2736,"direct_input_per_1m":45000,"direct_output_per_1m":500000,"available":true,"healthy":true},
  {"provider_id":"inferhub","price_input_per_1m":1500,"price_output_per_1m":6000,"direct_input_per_1m":45000,"direct_output_per_1m":500000,"available":true,"healthy":true},
  {"provider_id":"inferhub","price_input_per_1m":656,"price_output_per_1m":7286,"direct_input_per_1m":45000,"direct_output_per_1m":500000,"available":true,"healthy":true},
  {"provider_id":"dashscope-cn","price_input_per_1m":1000,"price_output_per_1m":50000,"direct_input_per_1m":45000,"direct_output_per_1m":500000,"available":true,"healthy":true},
  {"provider_id":"openrouter","price_input_per_1m":13048,"price_output_per_1m":39144,"direct_input_per_1m":45000,"direct_output_per_1m":500000,"available":true,"healthy":true},
  {"provider_id":"dashscope-cn","price_input_per_1m":6727,"price_output_per_1m":74750,"direct_input_per_1m":45000,"direct_output_per_1m":500000,"available":true,"healthy":true},
  {"provider_id":"deepseek","price_input_per_1m":45000,"price_output_per_1m":500000,"direct_input_per_1m":45000,"direct_output_per_1m":500000,"available":true,"healthy":true}
]}`

func TestParseSurplusFastBookDropsChronicallyUnreliableProviders(t *testing.T) {
	perf := map[string]ProviderPerf{
		"dashscope-cn": {UptimePct: ms(98.3), TTFTP50MS: ms(5546)},
		"oneapi":       {UptimePct: ms(10.9), TTFTP50MS: ms(18420)},
		"inferhub":     {UptimePct: ms(79.3), TTFTP50MS: ms(5546)},
		"openrouter":   {UptimePct: ms(97.9), TTFTP50MS: ms(3043)},
		"deepseek":     {UptimePct: ms(99.5), TTFTP50MS: ms(2254)},
	}
	q, err := ParseSurplusFastBook(strings.NewReader(slowSellersBook), perf, 30, 3, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	// oneapi fails most requests and is dropped. inferhub stays: whether it
	// is fast right now is FastHealth's call, from live traffic. Depth 3
	// reaches three new providers past the band, cheapest first.
	if want := []string{"dashscope-cn", "inferhub", "openrouter", "deepseek"}; !slices.Equal(q.ProviderOrder, want) {
		t.Fatalf("order = %v, want %v", q.ProviderOrder, want)
	}
}

// With dashscope-cn blocked, inferhub owns the band and the next price level
// too. Counting that level toward depth would leave inferhub alone, and when
// its sellers refuse, Surplus answers no_healthy_sellers.
func TestParseSurplusFastBookDepthCountsNewProvidersOnly(t *testing.T) {
	perf := map[string]ProviderPerf{"oneapi": {UptimePct: ms(10.9)}}
	q, err := ParseSurplusFastBook(strings.NewReader(slowSellersBook), perf, 0, 1, 0, []string{"dashscope-cn"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"inferhub", "openrouter"}; !slices.Equal(q.ProviderOrder, want) {
		t.Fatalf("order = %v, want %v", q.ProviderOrder, want)
	}
	if q.Eligible != 3 {
		t.Fatalf("eligible = %d, want both inferhub offers and openrouter", q.Eligible)
	}
}

func TestParseSurplusFastBookAllProvidersUnreliableFindsNothing(t *testing.T) {
	perf := map[string]ProviderPerf{}
	for _, p := range []string{"dashscope-cn", "oneapi", "inferhub", "openrouter", "deepseek"} {
		perf[p] = ProviderPerf{UptimePct: ms(30)}
	}
	q, err := ParseSurplusFastBook(strings.NewReader(slowSellersBook), perf, 30, 3, 0, nil)
	if err != nil || q.Found {
		t.Fatalf("q = %+v err = %v, want not found so the chain moves on", q, err)
	}
}

func TestParseSurplusPerformanceReadsUptime(t *testing.T) {
	perf, err := ParseSurplusPerformance(strings.NewReader(`{"providers":[{"provider":"oneapi","uptime_pct":10.9,"ttft_p50_ms":18420}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if p := perf["oneapi"]; p.UptimePct == nil || *p.UptimePct != 10.9 {
		t.Fatalf("oneapi = %+v, want uptime 10.9", p)
	}
}
