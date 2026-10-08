package market

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const surplusFixture = `{"models":[
  {"model":"deepseek-v4.1-flash","displayName":"DeepSeek V4.1 Flash","providers":[
    {"provider":"venice","pricing":{"input":0.20,"output":0.90,"cacheRead":0.03}},
    {"provider":"openrouter","pricing":{"input":0.12,"output":0.48,"cacheRead":0.02,"cacheWrite":0.15}}
  ]},
  {"model":"no-pricing","providers":[{"provider":"x","pricing":{"input":0,"output":0}}]},
  {"model":"empty-providers","providers":[]}
]}`

func TestParseSurplusPicksCheapestOutput(t *testing.T) {
	models, err := ParseSurplus(strings.NewReader(surplusFixture))
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 {
		t.Fatalf("got %d models, want 1 (only the priced one)", len(models))
	}
	m := models[0]
	if m.Slug != "surplus:deepseek-v4.1-flash" {
		t.Fatalf("slug = %q", m.Slug)
	}
	if m.MinAskIn != 0.12 || m.MinAskOut != 0.48 {
		t.Fatalf("rates = (%v,%v), want (0.12,0.48) from openrouter", m.MinAskIn, m.MinAskOut)
	}
	if m.CacheRead != 0.02 || m.CacheWrite != 0.15 {
		t.Fatalf("cache = (%v,%v), want (0.02,0.15)", m.CacheRead, m.CacheWrite)
	}
}

func TestFetchSurplusNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	if _, err := FetchSurplus(context.Background(), srv.URL); err == nil {
		t.Fatal("expected error on non-200")
	}
}

func TestFetchSurplusParses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(surplusFixture))
	}))
	defer srv.Close()
	models, err := FetchSurplus(context.Background(), srv.URL)
	if err != nil || len(models) != 1 {
		t.Fatalf("models=%d err=%v", len(models), err)
	}
}
