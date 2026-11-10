package gateway

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/mydisha/keirouter/backend/internal/store"
)

const tokenSubCacheTTL = 30 * time.Second

type tokenSubEntry struct {
	expiresAt time.Time
	isToken   bool
	loaded    time.Time
}

// tokenSubCache keeps the per-key expiry lookup off the request hot path. The
// zero value is ready to use.
type tokenSubCache struct {
	mu sync.RWMutex
	m  map[string]tokenSubEntry
}

// tokenKeyExpired reports whether keyID is a token-package key whose
// subscription has run out. PAYG keys are cached as non-token.
func (s *Server) tokenKeyExpired(ctx context.Context, keyID string) (bool, error) {
	now := time.Now()
	c := &s.tokenSubs
	c.mu.RLock()
	ent, ok := c.m[keyID]
	c.mu.RUnlock()
	if !ok || now.Sub(ent.loaded) >= tokenSubCacheTTL {
		sub, err := s.db.TokenSubscriptions().GetByKey(ctx, keyID)
		switch {
		case err == nil:
			ent = tokenSubEntry{expiresAt: sub.ExpiresAt, isToken: true, loaded: now}
		case errors.Is(err, store.ErrNotFound):
			ent = tokenSubEntry{loaded: now}
		default:
			return false, err
		}
		c.mu.Lock()
		if c.m == nil {
			c.m = make(map[string]tokenSubEntry)
		}
		c.m[keyID] = ent
		c.mu.Unlock()
	}
	return ent.isToken && !now.Before(ent.expiresAt), nil
}

// invalidateTokenSub drops the cached expiry so a renewal or purchase takes
// effect immediately on this instance.
func (s *Server) invalidateTokenSub(keyID string) {
	s.tokenSubs.mu.Lock()
	delete(s.tokenSubs.m, keyID)
	s.tokenSubs.mu.Unlock()
}
