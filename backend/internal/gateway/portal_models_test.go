package gateway

import "testing"

func TestPortalPriceFromRowDiscount(t *testing.T) {
	live := portalPriceFromRow(publicModelRow{Name: "glm", PriceBasis: "live", InputPerM: 0.5, OutputPerM: 2, UpToDiscountPct: 40, DiscountPct: 12})
	if live.DiscountUpToPct == nil || *live.DiscountUpToPct != 40 {
		t.Fatalf("live chain: want up-to 40, got %v", live.DiscountUpToPct)
	}
	fixed := portalPriceFromRow(publicModelRow{Name: "gpt", PriceBasis: "fixed", DiscountPct: 25, UpToDiscountPct: 0})
	if fixed.DiscountUpToPct == nil || *fixed.DiscountUpToPct != 25 {
		t.Fatalf("fixed chain: want 25, got %v", fixed.DiscountUpToPct)
	}
	none := portalPriceFromRow(publicModelRow{Name: "x", PriceBasis: "live"})
	if none.DiscountUpToPct != nil || none.MaxInputPerM != nil || none.OfficialIn != nil {
		t.Fatalf("no discount/max/official: want nils, got %+v", none)
	}
}
