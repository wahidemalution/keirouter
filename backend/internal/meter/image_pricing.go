package meter

import "math"

// isImageEvent reports whether ev bills generated images rather than tokens.
func isImageEvent(ev Event) bool { return ev.ImageCount > 0 }

// imageCost bills an image event at ImageBillMicro per image. It reports
// false when ev is not a priced image event.
func imageCost(ev Event) (CostBreakdown, bool) {
	if ev.ImageCount <= 0 || ev.ImageBillMicro <= 0 {
		return CostBreakdown{}, false
	}
	return perImageCost(ev, ev.ImageBillMicro), true
}

// imageUpstreamCost is the operator's cost for an image event:
// ImageCostMicro per image.
func imageUpstreamCost(ev Event) CostBreakdown {
	if ev.ImageCostMicro <= 0 {
		return CostBreakdown{}
	}
	return perImageCost(ev, ev.ImageCostMicro)
}

func perImageCost(ev Event, perImageMicro float64) CostBreakdown {
	total := float64(ev.ImageCount) * perImageMicro
	return CostBreakdown{
		Pricing: PricingMatch{
			Key:    "static:" + ev.Provider + "/" + ev.Model,
			Status: "priced", Source: "static_image", MatchKind: "static_image",
		},
		CostNanos:  int64(math.Round(total * 1000)),
		CostMicros: int64(math.Round(total)),
	}
}
