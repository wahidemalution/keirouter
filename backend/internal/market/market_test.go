package market

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseSnapshot(t *testing.T) {
	body := `{"ts":1,"models":[{"slug":"ag/x","family":"Antigravity","minAskIn":0.005,"minAskOut":0.025,"maxAskIn":2.5,"maxAskOut":12.5,"lastRate":0.2}]}`
	models, err := ParseSnapshot(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].Slug != "ag/x" || models[0].MinAskIn != 0.005 {
		t.Fatalf("unexpected: %+v", models)
	}
}

func TestFetchParsesEnvelope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":[{"slug":"a/x","minAskIn":1,"minAskOut":2}]}`))
	}))
	defer srv.Close()

	models, err := Fetch(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].Slug != "a/x" || models[0].MinAskIn != 1 {
		t.Fatalf("got %+v", models)
	}
}

func TestFetchNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	if _, err := Fetch(context.Background(), srv.URL); err == nil {
		t.Fatal("expected error on non-200")
	}
}
