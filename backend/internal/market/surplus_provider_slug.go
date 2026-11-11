package market

import (
	"fmt"
	"regexp"
	"strings"
)

// SurplusTrustedProviderPrefix keys a surplus-trusted model pinned to one
// Surplus provider: surplus-trusted@<provider>:<model>.
const SurplusTrustedProviderPrefix = "surplus-trusted@"

// surplusFastProviderPrefix is reserved: surplus-fast has no per-provider form.
const surplusFastProviderPrefix = "surplus-fast@"

var surplusProviderID = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// ParseProviderSlug splits surplus-trusted@<provider>:<model>. ok is true only
// for a valid provider id and a non-empty model; provider comes back lowercased.
func ParseProviderSlug(slug string) (provider, model string, ok bool) {
	rest, found := strings.CutPrefix(slug, SurplusTrustedProviderPrefix)
	if !found {
		return "", "", false
	}
	provider, model, found = strings.Cut(rest, ":")
	if !found || model == "" {
		return "", "", false
	}
	provider = strings.ToLower(provider)
	if !surplusProviderID.MatchString(provider) {
		return "", "", false
	}
	return provider, model, true
}

// ProviderSlug builds surplus-trusted@<provider>:<model>.
func ProviderSlug(provider, model string) string {
	return SurplusTrustedProviderPrefix + provider + ":" + model
}

// SlugProvider returns the Surplus provider id pinned by slug, or "" for any
// other form.
func SlugProvider(slug string) string {
	provider, _, _ := ParseProviderSlug(slug)
	return provider
}

// ValidateStepSlug rejects the Surplus slug forms a chain step may not use:
// surplus-fast@ anywhere, surplus-trusted@ at chain level (a provider is
// pinned per key, not per chain), and surplus-trusted@ with a malformed
// provider id or model. Empty slugs and slugs outside the Surplus families
// pass.
func ValidateStepSlug(slug string, chainLevel bool) error {
	if strings.HasPrefix(slug, surplusFastProviderPrefix) {
		return fmt.Errorf("slug %q: surplus-fast has no per-provider form, use surplus-fast:<model>", slug)
	}
	if !strings.HasPrefix(slug, SurplusTrustedProviderPrefix) {
		return nil
	}
	if chainLevel {
		return fmt.Errorf("slug %q: a chain cannot pin a Surplus provider, use surplus-trusted:<model> and pick the provider per key", slug)
	}
	if _, _, ok := ParseProviderSlug(slug); !ok {
		return fmt.Errorf("slug %q: expected surplus-trusted@<provider>:<model> with a provider id of lowercase letters, digits, '.', '_' or '-' and a non-empty model", slug)
	}
	return nil
}
