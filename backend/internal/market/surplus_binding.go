package market

import "strings"

// ChainRoute is the part of a chain that decides which Surplus books it
// binds: its chain-level market slugs and its steps in order.
type ChainRoute struct {
	MarketSlugs []string
	Steps       []StepRoute
}

// StepRoute is one chain step: its market slug (may be empty), model and
// provider id.
type StepRoute struct {
	MarketSlug, Model, Provider string
}

// TrustedBinding is one surplus-trusted or surplus-fast model referenced by
// the chains, with the /min{N} floor of the provider base URL serving it.
type TrustedBinding struct {
	Name     string
	FloorPct int
}

// BindTrustedSurplus returns the Surplus model names referenced by any chain
// (chain-level or per-step) through a slug with the given prefix
// (surplus-trusted: or surplus-fast:). floors maps provider id to its /min{N}
// floor. Each binding carries the lowest floor among the step providers
// serving it, so a quote is never stricter than the most permissive route
// that can reach Surplus.
func BindTrustedSurplus(chains []ChainRoute, prefix string, floors map[string]int) []TrustedBinding {
	idx := map[string]int{}
	var out []TrustedBinding
	add := func(slug, provider string) {
		name, ok := strings.CutPrefix(slug, prefix)
		if !ok || name == "" {
			return
		}
		floor := floors[provider]
		if i, seen := idx[name]; seen {
			out[i].FloorPct = min(out[i].FloorPct, floor)
			return
		}
		idx[name] = len(out)
		out = append(out, TrustedBinding{Name: name, FloorPct: floor})
	}
	var unserved []string
	for _, c := range chains {
		for _, st := range c.Steps {
			if st.MarketSlug != "" {
				add(st.MarketSlug, st.Provider)
				continue
			}
			for _, slug := range c.MarketSlugs {
				if strings.TrimPrefix(slug, prefix) == st.Model {
					add(slug, st.Provider)
				}
			}
		}
		unserved = append(unserved, c.MarketSlugs...)
	}
	// A chain-level slug no step serves is still quoted for display, without a
	// floor, unless some step already bound it.
	for _, slug := range unserved {
		if _, seen := idx[strings.TrimPrefix(slug, prefix)]; !seen {
			add(slug, "")
		}
	}
	return out
}
