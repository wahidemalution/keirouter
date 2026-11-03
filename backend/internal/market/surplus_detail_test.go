package market

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestMedianPercent(t *testing.T) {
	cases := []struct {
		in   []int
		want int
		ok   bool
	}{
		{[]int{90}, 90, true},
		{[]int{10, 90, 50}, 50, true},
		{[]int{10, 20}, 15, true},
		{[]int{10, 11}, 10, true},
		{nil, 0, false},
	}
	for _, c := range cases {
		got, ok := MedianPercent(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("MedianPercent(%v) = %d,%v want %d,%v", c.in, got, ok, c.want, c.ok)
		}
	}
	in := []int{3, 1, 2}
	MedianPercent(in)
	if in[0] != 3 || in[1] != 1 || in[2] != 2 {
		t.Errorf("input mutated: %v", in)
	}
}

func TestParsePerformanceRoutes(t *testing.T) {
	f, err := os.Open("testdata/perf-deepseek-v4.1-flash.json")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	routes, at, err := ParsePerformanceRoutes(f)
	if err != nil {
		t.Fatal(err)
	}
	if !at.Equal(time.UnixMilli(1793601464524)) {
		t.Errorf("computed_at = %v", at)
	}
	var found bool
	for _, r := range routes {
		switch r.Provider {
		case "inferhub":
			found = true
			if r.Requests != 76413 || r.UptimePct == nil || *r.UptimePct != 46.1 || r.TokensPerSec == nil || *r.TokensPerSec != 243.4 {
				t.Errorf("inferhub = %+v", r)
			}
		case "mordiem":
			if r.TokensPerSec != nil {
				t.Errorf("mordiem tps should be nil")
			}
		}
	}
	if !found {
		t.Fatal("inferhub missing")
	}
}

func TestShareRoutesKeepsOnlyEligible(t *testing.T) {
	perf := []RoutePerf{
		{Provider: "a", Requests: 300},
		{Provider: "b", Requests: 100},
		{Provider: "c", Requests: 9999},
	}
	got := ShareRoutes(perf, map[string]string{"a": "Alpha", "b": "Beta"})
	if len(got) != 2 || got[0].Name != "Alpha" || got[0].SharePct != 75 || got[1].Name != "Beta" || got[1].SharePct != 25 {
		t.Fatalf("got %+v", got)
	}
}

func TestPerformanceRoutesZeroTraffic(t *testing.T) {
	got := ShareRoutes([]RoutePerf{{Provider: "b"}, {Provider: "a"}}, map[string]string{"a": "A", "b": "B"})
	if len(got) != 2 || got[0].Name != "A" || got[0].SharePct != 0 || got[1].SharePct != 0 {
		t.Fatalf("got %+v", got)
	}
}

func TestFetchSurplusDetailEscapesModel(t *testing.T) {
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.EscapedPath()
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	FetchSurplusDetail(context.Background(), srv.URL+"/api/markets", "a/../b", 0, nil)
	if path != "/api/markets/a%2F..%2Fb" {
		t.Errorf("path = %q", path)
	}
}

func TestFetchSurplusDetailNotFoundIsError(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	if _, err := FetchSurplusDetail(context.Background(), srv.URL, "m", 0, nil); err == nil {
		t.Error("want error on 404")
	}
	routes, at, err := FetchSurplusPerformanceRoutes(context.Background(), srv.URL+"/api/markets", "m")
	if err != nil || len(routes) != 0 || !at.IsZero() {
		t.Errorf("perf 404 = %v %v %v", routes, at, err)
	}
}

func TestFetchCapsBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"providers":[],"pad":"` + strings.Repeat("x", 3<<20) + `"}`))
	}))
	defer srv.Close()
	if _, _, err := FetchSurplusPerformanceRoutes(context.Background(), srv.URL+"/api/markets", "m"); err == nil {
		t.Error("perf: want error on oversized body")
	}
	if _, err := FetchSurplusDetail(context.Background(), srv.URL, "m", 0, nil); err == nil {
		t.Error("detail: want error on oversized body")
	}
}
