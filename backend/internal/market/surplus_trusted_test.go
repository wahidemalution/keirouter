package market

import (
	"math"
	"reflect"
	"strings"
	"testing"
)

// Book prices are per 1M scaled by 1e6; direct list is $2/M in, $12/M out.
const trustedBookFixture = `{"model":"gpt-5.6-luna","offers":[
  {"provider_id":"openai","price_input_per_1m":2000,"price_output_per_1m":12000,"direct_input_per_1m":200000,"direct_output_per_1m":1200000,"available":false,"healthy":false,"trusted":true},
  {"provider_id":"inferhub","price_input_per_1m":2100,"price_output_per_1m":12600,"direct_input_per_1m":200000,"direct_output_per_1m":1200000,"available":true,"healthy":true,"trusted":false},
  {"provider_id":"bedrock","price_input_per_1m":2495,"price_output_per_1m":14969,"direct_input_per_1m":200000,"direct_output_per_1m":1200000,"available":true,"healthy":true,"trusted":true},
  {"provider_id":"bedrock","price_input_per_1m":2495,"price_output_per_1m":14969,"direct_input_per_1m":200000,"direct_output_per_1m":1200000,"available":true,"healthy":true,"trusted":true},
  {"provider_id":"openai","price_input_per_1m":9000,"price_output_per_1m":54000,"direct_input_per_1m":200000,"direct_output_per_1m":1200000,"available":true,"healthy":true,"trusted":true},
  {"provider_id":"x","price_input_per_1m":9500,"price_output_per_1m":57000,"direct_input_per_1m":200000,"direct_output_per_1m":1200000,"available":true,"healthy":false,"trusted":true},
  {"provider_id":"openai","price_input_per_1m":12000,"price_output_per_1m":72000,"direct_input_per_1m":200000,"direct_output_per_1m":1200000,"available":true,"healthy":true,"trusted":true},
  {"provider_id":"openai","price_input_per_1m":20000,"price_output_per_1m":120000,"direct_input_per_1m":200000,"direct_output_per_1m":1200000,"available":true,"healthy":true,"trusted":true}
]}`

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-12 }

func TestParseSurplusTrustedBookUsesOnlyTrustedAvailableHealthy(t *testing.T) {
	q, err := ParseSurplusTrustedBook(strings.NewReader(trustedBookFixture), 3, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !q.Found {
		t.Fatal("want found")
	}
	// Cheapest eligible is bedrock 2495/14969, not the unavailable openai 2000
	// nor the untrusted inferhub 2100.
	if !approx(q.BestIn, 0.002495) || !approx(q.BestOut, 0.014969) {
		t.Fatalf("best = (%v,%v)", q.BestIn, q.BestOut)
	}
	if q.Eligible != 5 {
		t.Fatalf("eligible = %d, want 5", q.Eligible)
	}
}

func TestParseSurplusTrustedBookDepthCapsAtThirdDistinctLevel(t *testing.T) {
	q, err := ParseSurplusTrustedBook(strings.NewReader(trustedBookFixture), 3, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Distinct levels: 2495, 9000, 12000 (the unhealthy 9500 is excluded).
	// Level 3 = 12000/72000 => discount 94% => ceiling 94-2 = 92 (margin for
	// Surplus's estimated discount) => cap 8% of list.
	if q.MinDiscountPct != 92 {
		t.Fatalf("min discount = %d, want 92", q.MinDiscountPct)
	}
	if !approx(q.CapIn, 0.016) || !approx(q.CapOut, 0.096) {
		t.Fatalf("cap = (%v,%v), want (0.016,0.096)", q.CapIn, q.CapOut)
	}
}

func TestParseSurplusTrustedBookCapNeverBelowLevel(t *testing.T) {
	// Level 1 discount is 98.7525% → ceiling 98-2 = 96 → cap 4% of list, which must be
	// at least the level price itself so the fallback bill can never undercut.
	q, err := ParseSurplusTrustedBook(strings.NewReader(trustedBookFixture), 1, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if q.MinDiscountPct != 96 {
		t.Fatalf("min discount = %d, want 96", q.MinDiscountPct)
	}
	if q.CapIn < q.BestIn || q.CapOut < q.BestOut {
		t.Fatalf("cap (%v,%v) below best (%v,%v)", q.CapIn, q.CapOut, q.BestIn, q.BestOut)
	}
}

func TestParseSurplusTrustedBookDepthBeyondLevelsUsesDeepest(t *testing.T) {
	q, err := ParseSurplusTrustedBook(strings.NewReader(trustedBookFixture), 10, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Deepest eligible level = 20000/120000 => 90% discount.
	if q.MinDiscountPct != 88 {
		t.Fatalf("min discount = %d, want 88 (90 minus margin)", q.MinDiscountPct)
	}
}

func TestParseSurplusTrustedBookNoEligible(t *testing.T) {
	const fixture = `{"offers":[{"price_input_per_1m":1,"price_output_per_1m":2,"direct_input_per_1m":10,"direct_output_per_1m":20,"available":true,"healthy":true,"trusted":false}]}`
	q, err := ParseSurplusTrustedBook(strings.NewReader(fixture), 3, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if q.Found {
		t.Fatal("untrusted-only book must not be found")
	}
}

func TestParseSurplusTrustedBookMissingDirectDisablesCap(t *testing.T) {
	const fixture = `{"offers":[{"provider_id":"google","price_input_per_1m":100,"price_output_per_1m":200,"available":true,"healthy":true,"trusted":true}]}`
	q, err := ParseSurplusTrustedBook(strings.NewReader(fixture), 3, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !q.Found {
		t.Fatal("want found")
	}
	// Without a list price no discount floor can be proven: no header, and the
	// fallback bill uses the deepest level's own price.
	if q.MinDiscountPct != 0 {
		t.Fatalf("min discount = %d, want 0", q.MinDiscountPct)
	}
	if !approx(q.CapIn, 0.0001) || !approx(q.CapOut, 0.0002) {
		t.Fatalf("cap = (%v,%v)", q.CapIn, q.CapOut)
	}
}

func TestTrustedModelCarriesQuote(t *testing.T) {
	q := TrustedQuote{Found: true, BestIn: 1, BestOut: 2, CapIn: 3, CapOut: 4, MinDiscountPct: 50}
	m := q.Model("gpt-5.6-luna")
	if m.Slug != "surplus-trusted:gpt-5.6-luna" || m.MinAskIn != 1 || m.MinAskOut != 2 ||
		m.FailoverIn != 3 || m.FailoverOut != 4 || m.MinDiscountPct != 50 {
		t.Fatalf("model = %+v", m)
	}
}

func TestParseSurplusTrustedBookProviderAllowList(t *testing.T) {
	q, err := ParseSurplusTrustedBook(strings.NewReader(trustedBookFixture), 3, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Untrusted inferhub and the unhealthy "x" must never be allowed.
	if got := strings.Join(q.Providers, ","); got != "bedrock,openai" {
		t.Fatalf("providers = %q, want bedrock,openai", got)
	}
	if m := q.Model("gpt-5.6-luna"); strings.Join(m.Providers, ",") != "bedrock,openai" {
		t.Fatalf("model providers = %v", m.Providers)
	}
}

func TestParseSurplusTrustedBookSkipsOfferWithoutProviderID(t *testing.T) {
	const fixture = `{"offers":[{"price_input_per_1m":100,"price_output_per_1m":200,"direct_input_per_1m":1000,"direct_output_per_1m":2000,"available":true,"healthy":true,"trusted":true}]}`
	q, err := ParseSurplusTrustedBook(strings.NewReader(fixture), 3, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if q.Found {
		t.Fatal("an offer with no provider id cannot be allow-listed and must be skipped")
	}
}

func TestParseSurplusTrustedBookFloorDropsShallowOffers(t *testing.T) {
	// List $1/M in, $4/M out. bedrock is 40% off on both rates; openai is 50%
	// off input but only 20% off output, so a min30 floor must drop it.
	const fixture = `{"offers":[
	  {"provider_id":"openai","price_input_per_1m":500000,"price_output_per_1m":3200000,"direct_input_per_1m":1000000,"direct_output_per_1m":4000000,"available":true,"healthy":true,"trusted":true},
	  {"provider_id":"bedrock","price_input_per_1m":600000,"price_output_per_1m":2400000,"direct_input_per_1m":1000000,"direct_output_per_1m":4000000,"available":true,"healthy":true,"trusted":true},
	  {"provider_id":"google","price_input_per_1m":700000,"price_output_per_1m":2800000,"direct_input_per_1m":1000000,"direct_output_per_1m":4000000,"available":true,"healthy":true,"trusted":true}
	]}`
	q, err := ParseSurplusTrustedBook(strings.NewReader(fixture), 3, 30, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !q.Found || q.Eligible != 2 {
		t.Fatalf("found=%v eligible=%d, want 2 (bedrock 40%%, google exactly 30%%)", q.Found, q.Eligible)
	}
	if !approx(q.BestIn, 0.6) || !approx(q.BestOut, 2.4) {
		t.Fatalf("best = (%v,%v), want bedrock (0.6,2.4)", q.BestIn, q.BestOut)
	}
	if got := strings.Join(q.Providers, ","); got != "bedrock,google" {
		t.Fatalf("providers = %q", got)
	}
	// Deepest level google is exactly 30% off → ceiling 30, cap = list × 0.7.
	if q.MinDiscountPct != 30 || !approx(q.CapIn, 0.7) || !approx(q.CapOut, 2.8) {
		t.Fatalf("ceiling=%d cap=(%v,%v)", q.MinDiscountPct, q.CapIn, q.CapOut)
	}
	if !approx(q.FloorCapIn, 0.7) || !approx(q.FloorCapOut, 2.8) {
		t.Fatalf("floor cap = (%v,%v), want (0.7,2.8)", q.FloorCapIn, q.FloorCapOut)
	}
}

func TestParseSurplusTrustedBookFloorNeedsListPrice(t *testing.T) {
	const fixture = `{"offers":[{"provider_id":"google","price_input_per_1m":100,"price_output_per_1m":200,"available":true,"healthy":true,"trusted":true}]}`
	q, err := ParseSurplusTrustedBook(strings.NewReader(fixture), 3, 30, nil)
	if err != nil {
		t.Fatal(err)
	}
	if q.Found {
		t.Fatal("an offer with no list price cannot prove a min30 discount")
	}
}

func TestMinDiscountFloor(t *testing.T) {
	cases := map[string]int{
		"https://api.surplusintelligence.ai/v1":         0,
		"https://api.surplusintelligence.ai/min30/v1":   30,
		"https://api.surplusintelligence.ai/min30/v1/":  30,
		"https://api.surplusintelligence.ai/min0/v1":    0,
		"https://api.surplusintelligence.ai/min100/v1":  100,
		"https://api.surplusintelligence.ai/min101/v1":  0,
		"https://api.surplusintelligence.ai/min030/v1":  0,
		"https://api.surplusintelligence.ai/min30.5/v1": 0,
		"https://api.surplusintelligence.ai/min-1/v1":   0,
		"https://api.surplusintelligence.ai/minimal/v1": 0,
		"https://example.com/anthropic/min45/v1":        45,
		"":                                              0,
	}
	for in, want := range cases {
		if got := MinDiscountFloor(in); got != want {
			t.Errorf("MinDiscountFloor(%q) = %d, want %d", in, got, want)
		}
	}
}

// A provider the Surplus account blocks would refuse the request; quoting it
// shows a price nobody can buy and sets a ceiling the real sellers fail.
func TestParseSurplusTrustedBookSkipsBlockedProvider(t *testing.T) {
	q, err := ParseSurplusTrustedBook(strings.NewReader(trustedBookFixture), 1, 0, []string{" Bedrock "})
	if err != nil {
		t.Fatal(err)
	}
	if !approx(q.BestIn, 0.009) || !approx(q.BestOut, 0.054) {
		t.Fatalf("best = (%v,%v), want openai's 0.009/0.054", q.BestIn, q.BestOut)
	}
	if len(q.Providers) != 1 || q.Providers[0] != "openai" {
		t.Fatalf("allow-list = %v, want [openai]", q.Providers)
	}
	if q.MinDiscountPct != 93 {
		t.Fatalf("ceiling = %d, want 93 from openai's 95.5%%", q.MinDiscountPct)
	}
}

func TestEligibleTrustedOffersKeepsProviderLabel(t *testing.T) {
	book := `{"offers":[{"provider":"OpenRouter","provider_id":"openrouter","price_input_per_1m":500000,"price_output_per_1m":2000000,"direct_input_per_1m":1000000,"direct_output_per_1m":4000000,"available":true,"healthy":true,"trusted":true}]}`
	got, err := EligibleTrustedOffers(strings.NewReader(book), 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []TrustedOffer{{ProviderID: "openrouter", Provider: "OpenRouter", In: 0.5, Out: 2, DirectIn: 1, DirectOut: 4}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("offers = %+v, want %+v", got, want)
	}
}

// The provider label is display only: a non-string one must not fail the
// book billing decodes, nor change its quote.
func TestTrustedBookIgnoresMalformedProviderLabel(t *testing.T) {
	const offer = `{"provider_id":"bedrock",%s"price_input_per_1m":2495,"price_output_per_1m":14969,"direct_input_per_1m":200000,"direct_output_per_1m":1200000,"available":true,"healthy":true,"trusted":true}`
	book := func(label string) string {
		return `{"offers":[` + strings.Replace(offer, "%s", label, 1) + `]}`
	}
	want, err := ParseSurplusTrustedBook(strings.NewReader(book(`"provider":"AWS Bedrock",`)), 3, 10, nil)
	if err != nil || !want.Found {
		t.Fatalf("string label: %+v, %v", want, err)
	}
	for _, label := range []string{`"provider":{"x":1},`, `"provider":42,`, `"provider":null,`, ``} {
		got, err := ParseSurplusTrustedBook(strings.NewReader(book(label)), 3, 10, nil)
		if err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: quote %+v, want %+v", label, got, want)
		}
		offers, err := EligibleTrustedOffers(strings.NewReader(book(label)), 10, nil)
		if err != nil || len(offers) != 1 || offers[0].Provider != "" || offers[0].ProviderID != "bedrock" {
			t.Fatalf("%s: offers %+v, %v", label, offers, err)
		}
	}
	offers, _ := EligibleTrustedOffers(strings.NewReader(book(`"provider":"AWS Bedrock",`)), 10, nil)
	if len(offers) != 1 || offers[0].Provider != "AWS Bedrock" {
		t.Fatalf("string label offers %+v", offers)
	}
}

// openrouter asks $1/$2, alibaba-cn asks $0.5/$1, both against a $2/$4 list.
const twoProviderBook = `{"model":"qwen-plus","offers":[
  {"provider_id":"openrouter","price_input_per_1m":1000000,"price_output_per_1m":2000000,"direct_input_per_1m":2000000,"direct_output_per_1m":4000000,"available":true,"healthy":true,"trusted":true},
  {"provider_id":"alibaba-cn","price_input_per_1m":500000,"price_output_per_1m":1000000,"direct_input_per_1m":2000000,"direct_output_per_1m":4000000,"available":true,"healthy":true,"trusted":true}
]}`

func TestParseSurplusTrustedBookForProvider(t *testing.T) {
	q, err := ParseSurplusTrustedBookFor(strings.NewReader(twoProviderBook), 3, 0, nil, "OpenRouter")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(q.Providers, []string{"openrouter"}) {
		t.Fatalf("providers = %v", q.Providers)
	}
	if !approx(q.BestIn, 1.0) || !approx(q.BestOut, 2.0) {
		t.Fatalf("best = (%v,%v), want (1,2)", q.BestIn, q.BestOut)
	}
	// 50% below list, ceiling 50-2 = 48 => cap 52% of list. Never alibaba's 0.5.
	if q.MinDiscountPct != 48 || !approx(q.CapIn, 1.04) || !approx(q.CapOut, 2.08) {
		t.Fatalf("ceiling = %d%% cap (%v,%v), want 48%% (1.04,2.08)", q.MinDiscountPct, q.CapIn, q.CapOut)
	}
	if q.BestDiscountPct != 50 {
		t.Fatalf("best discount = %d, want 50", q.BestDiscountPct)
	}
	if q.Eligible != 1 {
		t.Fatalf("eligible = %d, want 1", q.Eligible)
	}

	missing, err := ParseSurplusTrustedBookFor(strings.NewReader(twoProviderBook), 3, 0, nil, "missing")
	if err != nil {
		t.Fatal(err)
	}
	if missing.Found {
		t.Fatal("unknown provider must not be found")
	}

	all, err := ParseSurplusTrustedBookFor(strings.NewReader(twoProviderBook), 3, 0, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(all.Providers, []string{"alibaba-cn", "openrouter"}) {
		t.Fatalf("providers = %v", all.Providers)
	}
	if !approx(all.BestIn, 0.5) || all.BestDiscountPct != 75 {
		t.Fatalf("whole book best = %v at %d%%, want 0.5 at 75%%", all.BestIn, all.BestDiscountPct)
	}
}

func TestParseSurplusTrustedBookForProviderFloor(t *testing.T) {
	// A 60% floor drops openrouter (50% off); alibaba-cn (75% off) survives.
	q, _ := ParseSurplusTrustedBookFor(strings.NewReader(twoProviderBook), 3, 60, nil, "openrouter")
	if q.Found {
		t.Fatal("openrouter misses the floor")
	}
	q, _ = ParseSurplusTrustedBookFor(strings.NewReader(twoProviderBook), 3, 60, nil, "alibaba-cn")
	if !q.Found || !approx(q.FloorCapIn, 0.8) {
		t.Fatalf("alibaba-cn floor cap = %v found=%v, want 0.8", q.FloorCapIn, q.Found)
	}
}

func TestBestDiscountPctWithoutListPrice(t *testing.T) {
	q, _ := ParseSurplusTrustedBook(strings.NewReader(`{"offers":[{"provider_id":"a","price_input_per_1m":1000,"price_output_per_1m":2000,"available":true,"healthy":true,"trusted":true}]}`), 3, 0, nil)
	if !q.Found || q.BestDiscountPct != 0 {
		t.Fatalf("found=%v discount=%d, want found with 0", q.Found, q.BestDiscountPct)
	}
}

// List prices differ per offer: the dearer offer b sits deeper below its own
// list (50%) than the cheapest offer a (16%), and the quote reports the deeper.
func TestBestDiscountPctIsDeepestAcrossOffers(t *testing.T) {
	book := `{"offers":[
  {"provider_id":"a","price_input_per_1m":1000000,"price_output_per_1m":2000000,"direct_input_per_1m":1200000,"direct_output_per_1m":2400000,"available":true,"healthy":true,"trusted":true},
  {"provider_id":"b","price_input_per_1m":1500000,"price_output_per_1m":3000000,"direct_input_per_1m":3000000,"direct_output_per_1m":6000000,"available":true,"healthy":true,"trusted":true},
  {"provider_id":"c","price_input_per_1m":2000000,"price_output_per_1m":4000000,"available":true,"healthy":true,"trusted":true}
]}`
	q, err := ParseSurplusTrustedBook(strings.NewReader(book), 3, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !approx(q.BestIn, 1.0) || q.BestDiscountPct != 50 {
		t.Fatalf("best = %v at %d%%, want cheapest 1.0 and deepest discount 50%%", q.BestIn, q.BestDiscountPct)
	}
	pinned, _ := ParseSurplusTrustedBookFor(strings.NewReader(book), 3, 0, nil, "a")
	if pinned.BestDiscountPct != 16 {
		t.Fatalf("pinned to a: discount = %d, want 16", pinned.BestDiscountPct)
	}
}

func TestProviderModelUsesCanonicalSlug(t *testing.T) {
	q, _ := ParseSurplusTrustedBookFor(strings.NewReader(twoProviderBook), 3, 0, nil, "openrouter")
	m := q.ProviderModel("OpenRouter", "qwen-plus")
	if m.Slug != "surplus-trusted@openrouter:qwen-plus" {
		t.Fatalf("slug = %q", m.Slug)
	}
	if m.BestDiscountPct != 50 || !approx(m.FailoverIn, 1.04) || m.MinDiscountPct != 48 {
		t.Fatalf("model = %+v", m)
	}
}
