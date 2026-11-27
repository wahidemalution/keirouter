package gateway

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/mydisha/keirouter/backend/internal/store"
)

// publicProviderLabel turns a resolved provider name into the one a customer
// sees. It never returns a provider id: a name that is only the id, or only
// the marketplace word, becomes "Provider N" after the step's position in c,
// or "Provider" when c has no step on the provider.
func publicProviderLabel(name, provider string, c store.Chain, inChain bool) string {
	if name != "" && name != provider {
		if !mentionsSurplus(name) {
			return name
		}
		if rest := strings.Trim(surplusWord.ReplaceAllString(name, ""), " -_:·—–|/"); rest != "" {
			return strings.Join(strings.Fields(rest), " ")
		}
	}
	if inChain {
		for _, st := range c.Steps {
			if st.Provider == provider {
				return fmt.Sprintf("Provider %d", st.Position+1)
			}
		}
	}
	return "Provider"
}

// publicStepID is the id the portal uses for a chain's provider: the id of
// its first step on the provider, never the provider alias.
func publicStepID(c store.Chain, provider string) string {
	for _, st := range c.Steps {
		if st.Provider != provider {
			continue
		}
		if st.ID != "" {
			return st.ID
		}
		return fmt.Sprintf("step-%d", st.Position+1)
	}
	return ""
}

// publicNameTTL bounds how stale a renamed step can look in response headers.
const publicNameTTL = 30 * time.Second

type publicNameCache struct {
	mu sync.Mutex
	m  map[string]publicNameEntry
}

type publicNameEntry struct {
	info   requestLogInfo
	loaded time.Time
}

// publicNames returns the tenant's provider-name resolver, reloaded at most
// every publicNameTTL so response headers add no per-request queries.
func (s *Server) publicNames(ctx context.Context, tenantID string) requestLogInfo {
	c := &s.publicNameCache
	c.mu.Lock()
	ent, ok := c.m[tenantID]
	c.mu.Unlock()
	if !ok || time.Since(ent.loaded) >= publicNameTTL {
		ent = publicNameEntry{info: s.newRequestLogInfo(ctx, tenantID), loaded: time.Now()}
		c.mu.Lock()
		if c.m == nil {
			c.m = make(map[string]publicNameEntry)
		}
		c.m[tenantID] = ent
		c.mu.Unlock()
	}
	// chains and names are read-only once built; official is written by
	// discountPct, so each caller gets its own.
	info := ent.info
	info.official = map[string]*officialPrice{}
	return info
}

// publicProviderHeader is the X-KeiRouter-Provider value for a request that
// provider served through chainID.
func (s *Server) publicProviderHeader(ctx context.Context, tenantID, provider, chainID string) string {
	if provider == "" {
		return ""
	}
	return s.publicNames(ctx, tenantID).publicProviderName(provider, chainID)
}
