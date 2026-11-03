package gateway

import (
	"fmt"
	"math"
	"net/http"

	"github.com/mydisha/keirouter/backend/internal/dispatch"
	"github.com/mydisha/keirouter/backend/internal/market"
	"github.com/mydisha/keirouter/backend/internal/store"
)

// maxDiscountFloorPct caps the floor a customer may set. Higher floors match
// no offer, so they would only turn every request into an error.
const maxDiscountFloorPct = 95

// upstreamDiscountForFloor converts a customer's discount floor (on the price
// they pay, markup included) into the discount Surplus must find on its own
// price: the user pays upstream × mult, so paying at most (1-floor) of list
// needs upstream at most (1-floor)/mult of list. It rounds up so the user's
// price never ends above their floor. A multiplier at or below 1 counts as
// no markup, which is stricter.
func upstreamDiscountForFloor(floorPct int, mult float64) int {
	if floorPct <= 0 {
		return 0
	}
	if mult < 1 {
		mult = 1
	}
	keep := (1 - float64(floorPct)/100) / mult
	// The epsilon keeps exact percentages exact: 1-0.5/1.25 = 0.6000000001.
	return int(math.Ceil((1-keep)*100 - 1e-9))
}

// keyDiscountFloor is the minimum discount routing enforces for key on model:
// the key's effective minimum on a chain that can guarantee a discount, else
// 0. Chains that cannot guarantee one run at their set price.
func keyDiscountFloor(key store.APIKey, model string, discountCapable bool) int {
	if !discountCapable {
		return 0
	}
	return key.EffectiveMinDiscount(model)
}

// finalFallbackRuns reports whether routing appends the chain fallback for
// key on model: it is on, and either turned on explicitly (exempt from the
// floor) or no floor applies. A fallback kept from before per-key routing is
// dropped by any floor, as applyDiscountFloor does.
func finalFallbackRuns(key store.APIKey, model string, discountCapable bool) bool {
	if !key.FinalFallbackOn(model) {
		return false
	}
	return key.FinalFallbackExempt(model) || keyDiscountFloor(key, model, discountCapable) <= 0
}

// applyDiscountFloor keeps only the chain steps that can guarantee the
// customer's floor: surplus-trusted steps, which Surplus refuses (without
// charging) when no offer is at least X-Min-Discount below list. Other steps
// cannot prove a discount and are dropped. A chain fallback the customer or
// admin turned on explicitly is kept as is: it was chosen knowing it runs at
// the chain's normal price. A fallback kept from before per-key routing is
// dropped, as it always was. The header becomes the stricter of the system ceiling and the customer's floor.
// A Surplus step whose cached best offer is already below the required
// discount is dropped here, since Surplus would refuse it anyway; with no
// list price (BestDiscountPct 0) Surplus decides. Billing is untouched: the
// bill stays upstream cost × markup, so a floor never makes a request cost
// less than what Surplus charges.
func applyDiscountFloor(targets []dispatch.Target, floorPct int, model string) ([]dispatch.Target, error) {
	if floorPct <= 0 {
		return targets, nil
	}
	out := make([]dispatch.Target, 0, len(targets))
	for _, t := range targets {
		if t.ChainFallback && t.FallbackFloorExempt {
			out = append(out, t)
			continue
		}
		if !t.BillUpstreamCost || !market.IsUpstreamCostSlug(t.MarketSlug) {
			continue
		}
		need := upstreamDiscountForFloor(floorPct, t.UpstreamCostMult)
		if t.BestDiscountPct > 0 && t.BestDiscountPct < need {
			continue
		}
		t.MinDiscountPct = max(t.MinDiscountPct, need)
		out = append(out, t)
	}
	if len(out) == 0 {
		return nil, errDiscountFloor(model, floorPct)
	}
	return out, nil
}

func errDiscountFloor(model string, floorPct int) error {
	return badModelError{
		msg: fmt.Sprintf("model %s cannot meet your %d%% minimum discount: no route for it can guarantee that price. "+
			"Lower your minimum discount in the portal or use another model", model, floorPct),
		status: http.StatusUnprocessableEntity,
	}
}
