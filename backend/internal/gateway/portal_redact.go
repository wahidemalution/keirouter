package gateway

import (
	"regexp"
	"strings"

	"github.com/mydisha/keirouter/backend/internal/store"
)

// The portal never names the upstream marketplace. Provider aliases follow
// "custom-openai-surplus[-<seller>]" (see README), and the generic Surplus
// provider is labelled "Surplus". These helpers rewrite portal responses only;
// the admin keeps the real ids.

// portalMarketID stands in for the generic marketplace provider, where the
// marketplace picks the seller per request.
const portalMarketID = "market"

// portalAutoName labels a request the marketplace routed to a seller it did
// not name.
const portalAutoName = "Otomatis"

func mentionsSurplus(s string) bool {
	return strings.Contains(strings.ToLower(s), "surplus")
}

// portalProviderID maps a provider alias to the id the portal sees:
// "custom-openai-surplus-dashscope-cn" becomes "dashscope-cn" (the seller's
// own id) and the generic "custom-openai-surplus" becomes "market". Other
// aliases pass through.
func portalProviderID(id string) string {
	lower := strings.ToLower(id)
	i := strings.Index(lower, "surplus")
	if i < 0 {
		return id
	}
	seller := strings.TrimLeft(id[i+len("surplus"):], "-_:@")
	if seller == "" {
		return portalMarketID
	}
	return seller
}

var surplusWord = regexp.MustCompile(`(?i)surplus`)

// portalProviderName keeps name unless it mentions the marketplace. Then the
// word is dropped ("Surplus OpenRouter" becomes "OpenRouter"); a name left
// empty falls back to the seller id, or "Otomatis" when the seller is unknown.
func portalProviderName(name, id string) string {
	if !mentionsSurplus(name) && name != "" {
		return name
	}
	// A name equal to the alias is no display name at all.
	if !strings.EqualFold(name, id) {
		if rest := strings.Trim(surplusWord.ReplaceAllString(name, ""), " -_:·—–|/"); rest != "" {
			return strings.Join(strings.Fields(rest), " ")
		}
	}
	if pid := portalProviderID(id); pid != portalMarketID && pid != "" {
		return pid
	}
	return portalAutoName
}

// resolvePortalProvider maps an id the portal sent back to the chain step's
// real provider alias. It accepts either form; an id two steps share after
// mapping is ambiguous and refused.
func resolvePortalProvider(c store.Chain, id string) (string, bool) {
	if chainHasStepProvider(c, id) {
		return id, true
	}
	match := ""
	for _, st := range c.Steps {
		if portalProviderID(st.Provider) != id || st.Provider == match {
			continue
		}
		if match != "" {
			return "", false
		}
		match = st.Provider
	}
	return match, match != ""
}

// redactModelSettingsRow rewrites one Model Settings row for the portal.
func redactModelSettingsRow(row *modelSettingsRow) {
	for i := range row.Providers {
		p := &row.Providers[i]
		p.Name = portalProviderName(p.Name, p.ID)
		p.ID = portalProviderID(p.ID)
	}
}

// redactUsagePayload rewrites the provider fields of a usage payload built by
// buildKeyUsageMap for the portal.
func redactUsagePayload(payload map[string]any) {
	for _, field := range []string{"models", "recent"} {
		rows, _ := payload[field].([]map[string]any)
		for _, row := range rows {
			id, _ := row["provider"].(string)
			if name, ok := row["provider_name"].(string); ok {
				row["provider_name"] = portalProviderName(name, id)
			}
			row["provider"] = portalProviderID(id)
		}
	}
}
