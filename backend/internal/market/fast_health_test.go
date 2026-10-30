package market

import (
	"slices"
	"testing"
	"time"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newTestHealth() (*FastHealth, *fakeClock) {
	clock := &fakeClock{t: time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)}
	h := NewFastHealth()
	h.now = clock.now
	return h, clock
}

var trio = []string{"inferhub", "openrouter", "dolphin"}

func TestFastHealthKeepsProvidersWithoutHistory(t *testing.T) {
	h, _ := newTestHealth()
	if got := h.Filter("m", trio); !slices.Equal(got, trio) {
		t.Fatalf("filter = %v, want all", got)
	}
}

func TestFastHealthSlowFirstTokenStartsCooldown(t *testing.T) {
	h, clock := newTestHealth()
	h.Observe("m", "inferhub", 2*time.Second, true)
	if got := h.Filter("m", trio); !slices.Equal(got, trio) {
		t.Fatalf("after a fast answer filter = %v, want all", got)
	}
	h.Observe("m", "inferhub", 14*time.Second, true)
	if got := h.Filter("m", trio); slices.Contains(got, "inferhub") {
		t.Fatalf("filter = %v, want inferhub cooling down", got)
	}
	// Another model on the same provider is unaffected.
	if got := h.Filter("other", trio); !slices.Equal(got, trio) {
		t.Fatalf("other model filter = %v, want all", got)
	}
	clock.advance(FastCooldownBase - time.Second)
	if got := h.Filter("m", trio); slices.Contains(got, "inferhub") {
		t.Fatalf("filter = %v, want inferhub still cooling down", got)
	}
}

func TestFastHealthFailureStartsCooldown(t *testing.T) {
	h, _ := newTestHealth()
	h.Observe("m", "Dolphin", 0, false)
	if got := h.Filter("m", trio); slices.Contains(got, "dolphin") {
		t.Fatalf("filter = %v, want dolphin cooling down (case-insensitive)", got)
	}
}

func TestFastHealthTrialsOneRequestInTenAfterCooldown(t *testing.T) {
	h, clock := newTestHealth()
	h.Observe("m", "inferhub", 0, false)
	clock.advance(FastCooldownBase)
	kept := 0
	for range FastTrialEvery * 2 {
		if slices.Contains(h.Filter("m", trio), "inferhub") {
			kept++
		}
	}
	if kept != 2 {
		t.Fatalf("inferhub kept in %d of %d requests, want 2", kept, FastTrialEvery*2)
	}
	if !slices.Contains(h.Filter("m", trio), "inferhub") {
		t.Fatal("want the 21st request to be a trial")
	}
}

func TestFastHealthGoodTrialRecovers(t *testing.T) {
	h, clock := newTestHealth()
	h.Observe("m", "inferhub", 0, false)
	clock.advance(FastCooldownBase)
	h.Observe("m", "inferhub", 2*time.Second, true)
	for range 3 {
		if got := h.Filter("m", trio); !slices.Equal(got, trio) {
			t.Fatalf("filter = %v, want inferhub fully back", got)
		}
	}
}

func TestFastHealthRepeatStrikesDoubleCooldownUpToMax(t *testing.T) {
	h, clock := newTestHealth()
	want := FastCooldownBase
	for range 6 {
		h.Observe("m", "inferhub", 0, false)
		clock.advance(want - time.Second)
		if slices.Contains(h.Filter("m", trio), "inferhub") {
			t.Fatalf("inferhub back before its %v cooldown ended", want)
		}
		clock.advance(time.Second)
		want = min(want*2, FastCooldownMax)
	}
}

func TestFastHealthNilIsNoop(t *testing.T) {
	var h *FastHealth
	h.Observe("m", "inferhub", 0, false)
	if got := h.Filter("m", trio); !slices.Equal(got, trio) {
		t.Fatalf("filter = %v, want all", got)
	}
}

// TestSurplusFastTimingPolicy pins the agreed surplus-fast timings: a stream
// hands over after 10s without output, an answer within 8s counts as fast,
// and a slow provider sits out 30s, then 1m, then at most 2m per strike, and
// is retried on one request in three once its cooldown ends.
func TestSurplusFastTimingPolicy(t *testing.T) {
	if SurplusFastFirstOutputTimeout != 10*time.Second {
		t.Fatalf("first output timeout = %v, want 10s", SurplusFastFirstOutputTimeout)
	}
	if FastSlowTTFT != 8*time.Second {
		t.Fatalf("slow TTFT = %v, want 8s", FastSlowTTFT)
	}
	if FastTrialEvery != 3 {
		t.Fatalf("trial every = %d, want 3", FastTrialEvery)
	}
	h, clock := newTestHealth()
	for i, want := range []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 2 * time.Minute} {
		h.Observe("m", "p", 0, false)
		clock.advance(want - time.Second)
		if got := h.Filter("m", []string{"p"}); len(got) != 0 {
			t.Fatalf("strike %d: provider back after %v, want cooldown %v", i+1, want-time.Second, want)
		}
		clock.advance(time.Second)
		if got := h.Filter("m", []string{"p"}); len(got) != 1 {
			t.Fatalf("strike %d: provider not retried after cooldown %v", i+1, want)
		}
	}
}
