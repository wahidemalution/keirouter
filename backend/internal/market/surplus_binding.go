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
// Provider is the Surplus provider a surplus-trusted@<provider>:<model> step
// pins, lowercased; "" for the plain forms.
type TrustedBinding struct {
	Name     string
	Provider string
	FloorPct int
}

// BindTrustedSurplus returns the Surplus model names referenced by any chain
// (chain-level or per-step) through a slug with the given prefix
// (surplus-trusted: or surplus-fast:). floors maps provider id to its /min{N}
// floor. Each binding carries the lowest floor among the step providers
// serving it, so a quote is never stricter than the most permissive route
// that can reach Surplus.
//
// With the surplus-trusted: prefix, per-step surplus-trusted@<provider>:<model>
// slugs also bind, one binding per distinct (model, provider). Chain-level
// slugs never pin a provider.
func BindTrustedSurplus(chains []ChainRoute, prefix string, floors map[string]int) []TrustedBinding {
	type key struct{ name, provider string }
	idx := map[key]int{}
	var out []TrustedBinding
	bind := func(name, surplusProvider, provider string) {
		floor := floors[provider]
		k := key{name, surplusProvider}
		if i, seen := idx[k]; seen {
			out[i].FloorPct = min(out[i].FloorPct, floor)
			return
		}
		idx[k] = len(out)
		out = append(out, TrustedBinding{Name: name, Provider: surplusProvider, FloorPct: floor})
	}
	add := func(slug, provider string) {
		name, ok := strings.CutPrefix(slug, prefix)
		if !ok || name == "" {
			return
		}
		bind(name, "", provider)
	}
	var unserved []string
	for _, c := range chains {
		for _, st := range c.Steps {
			if st.MarketSlug != "" {
				if surplusProvider, name, ok := ParseProviderSlug(st.MarketSlug); ok {
					if prefix == SurplusTrustedPrefix {
						bind(name, surplusProvider, st.Provider)
					}
					continue
				}
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
		if _, seen := idx[key{strings.TrimPrefix(slug, prefix), ""}]; !seen {
			add(slug, "")
		}
	}
	return out
}
