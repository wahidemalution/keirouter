package market

import (
	"sync"
	"testing"
)

func TestSnapshotCacheRate(t *testing.T) {
	c := NewSnapshotCache()
	c.Replace([]Model{
		{Slug: "cmc/deepseek/deepseek-v4.1-flash", MinAskIn: 0.0225, MinAskOut: 0.09},
		{Slug: "zero/ask", MinAskIn: 0, MinAskOut: 0.5},
	})
	r, ok := c.Rate("cmc/deepseek/deepseek-v4.1-flash")
	if !ok || r.InputPerM != 0.0225 || r.OutputPerM != 0.09 {
		t.Fatalf("rate = %+v ok=%v", r, ok)
	}
	if _, ok := c.Rate("missing"); ok {
		t.Fatal("missing slug must be !ok")
	}
	if _, ok := c.Rate("zero/ask"); ok {
		t.Fatal("zero ask must be !ok")
	}
}

func TestSnapshotCacheReplaceIsAtomic(t *testing.T) {
	c := NewSnapshotCache()
	c.Replace([]Model{{Slug: "a/x", MinAskIn: 1, MinAskOut: 2}})
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); c.Replace([]Model{{Slug: "a/x", MinAskIn: 3, MinAskOut: 4}}) }()
		go func() { defer wg.Done(); _, _ = c.Rate("a/x") }()
	}
	wg.Wait()
}
