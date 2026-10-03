package app

import (
	"testing"

	"github.com/mydisha/keirouter/backend/internal/market"
)

func TestGlobalMarketSnapshotRoundTrip(t *testing.T) {
	setGlobalMarketSnapshot([]market.Model{{Slug: "a/b", MinAskIn: 1}})
	got := globalMarketSnapshot()
	if len(got) != 1 || got[0].Slug != "a/b" {
		t.Fatalf("unexpected snapshot %+v", got)
	}
}
