package gateway

import (
	"net/http"

	"github.com/mydisha/keirouter/backend/internal/dispatch"
	"github.com/mydisha/keirouter/backend/internal/store"
)

// filterStepsForKey keeps the chain steps whose provider key may use for
// model: inside the admin ceiling and not turned off by the customer. byAdmin
// is true when steps were dropped and every one of them fell outside the
// ceiling, so an empty result is the admin's doing, not the customer's.
func filterStepsForKey(steps []dispatch.Target, key store.APIKey, model string) (kept []dispatch.Target, byAdmin bool) {
	kept = make([]dispatch.Target, 0, len(steps))
	dropped, customerDropped := false, false
	for _, t := range steps {
		if key.ProviderEnabled(model, t.Provider) {
			kept = append(kept, t)
			continue
		}
		dropped = true
		if key.ProviderAllowed(t.Provider) {
			customerDropped = true
		}
	}
	return kept, dropped && !customerDropped
}

// errNoEnabledProvider reports a chain left with no step this key may use.
// It is the key's configuration, not the market, so it answers 422 like the
// discount-floor error.
func errNoEnabledProvider(model string, byAdmin bool) error {
	msg := "model " + model + " has no provider turned on for this key. Turn one on in Model Settings, or turn on the final fallback"
	if byAdmin {
		msg = "model " + model + " has no provider this key is allowed to use. Contact support to change the providers on your key"
	}
	return badModelError{msg: msg, status: http.StatusUnprocessableEntity}
}

// errSoldOutFallbackOff is the sold-out answer for a chain whose final
// fallback would have served but is turned off for this key.
func errSoldOutFallbackOff(name string) error {
	err := errSoldOut(name).(badModelError)
	err.msg += " Turn on the final fallback in Model Settings to avoid this error"
	return err
}
