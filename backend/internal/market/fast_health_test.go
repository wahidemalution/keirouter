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
