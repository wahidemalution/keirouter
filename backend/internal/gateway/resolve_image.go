package gateway

import (
	"context"
	"strings"

	"github.com/mydisha/keirouter/backend/internal/dispatch"
)

// isImageStep reports whether t is billed per generated image.
func isImageStep(t dispatch.Target) bool { return t.ImagePrice > 0 }

// imageOnlyStep reports whether t has no price except its per-image price.
// Such a step would bill a chat request nothing, so chat routes drop it.
func imageOnlyStep(t dispatch.Target) bool {
	return isImageStep(t) && !isStaticStep(t) && t.MarketSlug == ""
}

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

// imageFamily names the image model family of model, matched on the id after
// its last "/"; "" when it is not a known image model.
func imageFamily(model string) string {
	id := strings.ToLower(model)
	if i := strings.LastIndex(id, "/"); i >= 0 {
		id = id[i+1:]
	}
	switch {
	case strings.HasPrefix(id, "gpt-image"):
		return "gpt-image"
	case strings.HasPrefix(id, "grok-imagine"):
		return "grok-imagine"
	case strings.HasPrefix(id, "gemini-") && strings.Contains(id, "image"):
		return "gemini-image"
	}
	return ""
}

// dropImageOnlySteps returns ts without its image-only steps.
func dropImageOnlySteps(ts []dispatch.Target) []dispatch.Target {
	out := ts[:0:0]
	for _, t := range ts {
		if !imageOnlyStep(t) {
			out = append(out, t)
		}
	}
	return out
}

// resolveChatTargets is resolveTargets for the chat-style routes (chat
// completions, messages, responses, Gemini generateContent). Image-only steps
// are dropped; a route left empty answers like an unknown model.
func (s *Server) resolveChatTargets(ctx context.Context, tenantID, model string) (resolveResult, error) {
	res, err := s.resolveTargets(ctx, tenantID, model)
	if err != nil {
		return res, err
	}
	if res.Targets = dropImageOnlySteps(res.Targets); len(res.Targets) == 0 {
		return resolveResult{}, errUnknownModel(model)
	}
	return res, nil
}
