package meter

import (
	"errors"
	"math"
)

// ErrInvalidImageEvent refuses an image event whose count or per-image
// price is negative, not finite or out of range, so no image row can store
// a negative or wrapped cost.
var ErrInvalidImageEvent = errors.New("meter: invalid image event")

const (
	// maxImageCount is far above the four images a portal job may ask for.
	maxImageCount = 100
	// maxImageMicro is $10,000 per image, far above the $100 admin cap even
	// after markup; count × price then stays far inside int64.
	maxImageMicro = 10_000 * 1e6
)

// isImageEvent reports whether ev bills generated images rather than tokens.
func isImageEvent(ev Event) bool { return ev.ImageCount > 0 }

// checkImageEvent validates an image event's count and prices. A token
// event passes untouched.
func checkImageEvent(ev Event) error {
	if ev.ImageCount < 0 {
		return ErrInvalidImageEvent
	}
	if !isImageEvent(ev) {
		return nil
	}
	if ev.ImageCount > maxImageCount || !validImageMicro(ev.ImageBillMicro) || ev.ImageBillMicro <= 0 ||
		!validImageMicro(ev.ImageCostMicro) {
		return ErrInvalidImageEvent
	}
	return nil
}

func validImageMicro(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0 && v <= maxImageMicro
}

// imageCost bills an image event at ImageBillMicro per image. It reports
// false when ev is not a valid priced image event.
func imageCost(ev Event) (CostBreakdown, bool) {
	if checkImageEvent(ev) != nil || ev.ImageCount <= 0 || ev.ImageBillMicro <= 0 {
		return CostBreakdown{}, false
	}
	return perImageCost(ev, ev.ImageBillMicro), true
}

// imageUpstreamCost is the operator's cost for an image event:
// ImageCostMicro per image.
func imageUpstreamCost(ev Event) CostBreakdown {
	if checkImageEvent(ev) != nil || ev.ImageCostMicro <= 0 {
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
