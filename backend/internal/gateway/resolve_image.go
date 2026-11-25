package gateway

import (
	"context"

	"github.com/mydisha/keirouter/backend/internal/dispatch"
	"github.com/mydisha/keirouter/backend/internal/imagegen"
	"github.com/mydisha/keirouter/backend/internal/store"
)

// isImageStep reports whether t is billed per generated image.
func isImageStep(t dispatch.Target) bool { return t.ImagePrice > 0 }

// priceImageSteps stamps each image step with its customer price factor: 1 +
// the step markup, else 1 + chainMarkup (the chain-effective markup). Unlike
// staticBillMult there is no discount branch; admin refuses discounts on
// image steps.
func priceImageSteps(steps []dispatch.Target, chainMarkup float64) []dispatch.Target {
	for i := range steps {
		t := &steps[i]
		if !isImageStep(*t) {
			continue
		}
		markup := chainMarkup
		if t.StepMarkupSet {
			markup = t.StepMarkupPercent
		}
		t.ImageBillMult = 1 + markup/100
	}
	return steps
}

// imageFamily names the image model family of model; see imagegen.Family.
func imageFamily(model string) string { return imagegen.Family(model) }

// imageOnlyChain reports whether every step of c is an image step. Such a
// chain serves portal image generation only, so model lists that advertise
// chat models leave it out.
func imageOnlyChain(c store.Chain) bool {
	if len(c.Steps) == 0 {
		return false
	}
	for _, st := range c.Steps {
		if !st.HasImagePrice() {
			return false
		}
	}
	return true
}

// dropImageSteps returns ts without its image steps. An image step is billed
// per stored image by the portal image service only; on any other route it
// would be billed as tokens or not at all.
func dropImageSteps(ts []dispatch.Target) []dispatch.Target {
	out := ts[:0:0]
	for _, t := range ts {
		if !isImageStep(t) {
			out = append(out, t)
		}
	}
	return out
}

// resolveChatTargets is resolveTargets for the chat-style routes (chat
// completions, messages, responses, Gemini generateContent). Image steps are
// dropped; a route left empty answers like an unknown model.
func (s *Server) resolveChatTargets(ctx context.Context, tenantID, model string) (resolveResult, error) {
	res, err := s.resolveTargets(ctx, tenantID, model)
	if err != nil {
		return res, err
	}
	if res.Targets = dropImageSteps(res.Targets); len(res.Targets) == 0 {
		return resolveResult{}, errUnknownModel(model)
	}
	return res, nil
}
