package market

import (
	"reflect"
	"testing"
)

// Billing guard: pins the exact order and floors from BindTrustedSurplus.
// Order matters because fetches truncate the list at maxSurplusBooksPerSync.
func TestBindTrustedSurplusOrderGolden(t *testing.T) {
	p := SurplusTrustedPrefix
	floors := map[string]int{"p10": 10, "p30": 30}
	chains := []ChainRoute{
		{
			MarketSlugs: []string{p + "lone", p + "m2"},
			Steps: []StepRoute{
				{MarketSlug: p + "m1", Model: "m1", Provider: "p30"},
				// No step slug: bound through the chain-level slug for m2.
				{Model: "m2", Provider: "p10"},
			},
		},
		{
			MarketSlugs: []string{p + "m1"},
			Steps: []StepRoute{
				// m1 meets its lower floor last, m3 meets it first.
				{MarketSlug: p + "m1", Model: "m1", Provider: "p10"},
				{MarketSlug: p + "m3", Model: "m3", Provider: "p10"},
				{MarketSlug: p + "m3", Model: "m3", Provider: "p30"},
			},
		},
		{
			// No step serves these; foreign and empty names are skipped.
			MarketSlugs: []string{"surplus:x", p, p + "late"},
			Steps:       []StepRoute{{Model: "zz", Provider: "p30"}},
		},
	}
	got := BindTrustedSurplus(chains, p, floors)
	want := []TrustedBinding{
		{Name: "m1", FloorPct: 10},
		{Name: "m2", FloorPct: 10},
		{Name: "m3", FloorPct: 10},
		{Name: "lone", FloorPct: 0},
		{Name: "late", FloorPct: 0},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("bindings = %+v, want %+v", got, want)
	}
}
