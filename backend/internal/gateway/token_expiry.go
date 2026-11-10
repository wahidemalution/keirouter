package gateway

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/mydisha/keirouter/backend/internal/store"
	"github.com/mydisha/keirouter/backend/internal/tokenpkg"
)

const tokenSubCacheTTL = 30 * time.Second

type tokenSubEntry struct {
	expiresAt time.Time
	isToken   bool
	// planID is the configured token plan when isToken; its allowed models
	// are the key's whitelist.
	planID string
	loaded time.Time
}

// tokenSubCache keeps the per-key expiry lookup off the request hot path. The
// zero value is ready to use.
type tokenSubCache struct {
	mu sync.RWMutex
	m  map[string]tokenSubEntry
}

// expired reports whether this is a token-package key whose subscription has
// run out. A key on the token plan without a subscription (its owner was
// deleted) has a zero expiry, so it can never run without one.
func (e tokenSubEntry) expired() bool {
	return e.isToken && !time.Now().Before(e.expiresAt)
}

// tokenKeyState returns the cached token-package state of key: whether it is
// a token key (it has a subscription or sits on the token plan), its expiry
// and the token plan id. PAYG keys are cached as non-token.
func (s *Server) tokenKeyState(ctx context.Context, key store.APIKey) (tokenSubEntry, error) {
	keyID := key.ID
	now := time.Now()
	c := &s.tokenSubs
	c.mu.RLock()
	ent, ok := c.m[keyID]
	c.mu.RUnlock()
	if ok && now.Sub(ent.loaded) < tokenSubCacheTTL {
		return ent, nil
	}
	sub, err := s.db.TokenSubscriptions().GetByKey(ctx, keyID)
	switch {
	case err == nil:
		ent = tokenSubEntry{expiresAt: sub.ExpiresAt, isToken: true, loaded: now}
	case errors.Is(err, store.ErrNotFound):
		// A zero expiry is always in the past.
		ent = tokenSubEntry{loaded: now}
	default:
		return tokenSubEntry{}, err
	}
	if ent.isToken || key.PlanID != "" {
		st, err := tokenpkg.Load(ctx, s.db.Settings())
		if err != nil {
			return tokenSubEntry{}, err
		}
		ent.isToken = ent.isToken || key.PlanID == st.PlanID
		if ent.isToken {
			ent.planID = st.PlanID
		}
	}
	c.mu.Lock()
	if c.m == nil {
		c.m = make(map[string]tokenSubEntry)
	}
	c.m[keyID] = ent
	c.mu.Unlock()
	return ent, nil
}

// tokenEndpointAllowed reports whether a token package key may call method
// path. Only the metered text endpoints and read-only discovery are listed;
// everything else, including endpoints added later, is denied.
func tokenEndpointAllowed(method, path string) bool {
	switch method {
	case http.MethodPost:
		switch path {
		case "/v1/chat/completions", "/v1/messages", "/v1/messages/count_tokens", "/v1/responses":
			return true
		}
		modelAction, ok := strings.CutPrefix(path, "/v1beta/models/")
		return ok && modelAction != "" && !strings.Contains(modelAction, "/")
	case http.MethodGet:
		switch path {
		case "/v1/models", "/v1/models/info", "/v1/keys/me/usage", "/v1/pricing":
			return true
		}
		kind, ok := strings.CutPrefix(path, "/v1/models/")
		return ok && kind != "" && !strings.Contains(kind, "/")
	}
	return false
}

// invalidateTokenSub drops the cached expiry so a renewal or purchase takes
// effect immediately on this instance.
func (s *Server) invalidateTokenSub(keyID string) {
	s.tokenSubs.mu.Lock()
	delete(s.tokenSubs.m, keyID)
	s.tokenSubs.mu.Unlock()
}
