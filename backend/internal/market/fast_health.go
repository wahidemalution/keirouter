package market

import (
	"strings"
	"sync"
	"time"
)

const (
	// FastSlowTTFT is the time to first output above which a surplus-fast
	// answer counts against its provider.
	FastSlowTTFT = 8 * time.Second
	// FastCooldownBase is how long a provider sits out after its first slow
	// or failed answer. Each further strike doubles it, up to FastCooldownMax.
	// Kept short: Surplus providers swing between fast and slow, so one slow
	// answer should not bench a provider that is fast again a minute later.
	FastCooldownBase = 30 * time.Second
	FastCooldownMax  = 2 * time.Minute
	// FastTrialEvery is how often a provider whose cooldown ended is offered
	// again: one request in this many, until one answer decides it.
	FastTrialEvery = 3
)

// FastHealth tracks how each Surplus provider is serving a model right now,
// from KeiRouter's own surplus-fast traffic. One slow or failed answer sets a
// provider aside for a cooldown; afterwards it gets trial requests, and one
// fast answer brings it fully back. It is safe for concurrent use, and a nil
// *FastHealth keeps every provider.
type FastHealth struct {
	mu    sync.Mutex
	now   func() time.Time
	state map[fastKey]*fastState
}

type fastKey struct{ model, provider string }

// fastState exists only for a provider with strikes; a fast answer deletes it.
type fastState struct {
	strikes int
	until   time.Time
	seen    int // requests filtered since the cooldown ended
}

func NewFastHealth() *FastHealth {
	return &FastHealth{now: time.Now, state: map[fastKey]*fastState{}}
}

// SetClock replaces the clock, so tests can move past a cooldown.
func (h *FastHealth) SetClock(now func() time.Time) {
	h.mu.Lock()
	h.now = now
	h.mu.Unlock()
}

// Peek is Filter without side effects: it drops providers still cooling down
// and keeps the rest, including those in their recovery trial, without
// counting a request or using up a trial slot. For displays that must not
// disturb live routing.
func (h *FastHealth) Peek(model string, providers []string) []string {
	if h == nil {
		return providers
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.state) == 0 {
		return providers
	}
	now := h.now()
	out := make([]string, 0, len(providers))
	for _, p := range providers {
		if st := h.state[fastKey{model, strings.ToLower(p)}]; st != nil && now.Before(st.until) {
			continue
		}
		out = append(out, p)
	}
	return out
}

// Observe records one answer from provider for model. ok=false, or ttft above
// FastSlowTTFT, is a strike.
func (h *FastHealth) Observe(model, provider string, ttft time.Duration, ok bool) {
	if h == nil || provider == "" {
		return
	}
	key := fastKey{model, strings.ToLower(provider)}
	h.mu.Lock()
	defer h.mu.Unlock()
	if ok && ttft <= FastSlowTTFT {
		delete(h.state, key)
		return
	}
	st := h.state[key]
	if st == nil {
		st = &fastState{}
		h.state[key] = st
	}
	st.strikes++
	cooldown := FastCooldownBase
	for i := 1; i < st.strikes && cooldown < FastCooldownMax; i++ {
		cooldown *= 2
	}
	st.until = h.now().Add(min(cooldown, FastCooldownMax))
	st.seen = 0
}

// Filter returns providers minus those cooling down for model. A provider
// whose cooldown has ended is kept on the first request after it and then on
// one request in FastTrialEvery, until an answer from it is observed.
func (h *FastHealth) Filter(model string, providers []string) []string {
	if h == nil {
		return providers
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.state) == 0 {
		return providers
	}
	now := h.now()
	out := make([]string, 0, len(providers))
	for _, p := range providers {
		st := h.state[fastKey{model, strings.ToLower(p)}]
		if st == nil {
			out = append(out, p)
			continue
		}
		if now.Before(st.until) {
			continue
		}
		st.seen++
		if st.seen%FastTrialEvery == 1 {
			out = append(out, p)
		}
	}
	return out
}
