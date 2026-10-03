package turnstile

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDisabledIsNoop(t *testing.T) {
	v := New(Config{})
	if v.Enabled() {
		t.Fatal("empty config must be disabled")
	}
	if err := v.Verify(context.Background(), "", ""); err != nil {
		t.Fatalf("disabled Verify must be nil, got %v", err)
	}
}

func TestVerifySuccess(t *testing.T) {
	var gotSecret, gotResponse string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		gotSecret = r.Form.Get("secret")
		gotResponse = r.Form.Get("response")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()

	v := New(Config{Enabled: true, SiteKey: "site", SecretKey: "sec", VerifyURL: srv.URL})
	if !v.Enabled() {
		t.Fatal("expected enabled")
	}
	if err := v.Verify(context.Background(), "tok", "1.2.3.4"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotSecret != "sec" || gotResponse != "tok" {
		t.Fatalf("posted secret=%q response=%q", gotSecret, gotResponse)
	}
}

func TestVerifyRejectsEmptyToken(t *testing.T) {
	v := New(Config{Enabled: true, SiteKey: "site", SecretKey: "sec", VerifyURL: "http://unused"})
	if err := v.Verify(context.Background(), "   ", ""); err == nil {
		t.Fatal("empty token must error")
	}
}

func TestVerifyRejectsUnsuccessful(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":false,"error-codes":["timeout-or-duplicate"]}`))
	}))
	defer srv.Close()
	v := New(Config{Enabled: true, SiteKey: "site", SecretKey: "sec", VerifyURL: srv.URL})
	if err := v.Verify(context.Background(), "tok", ""); err == nil {
		t.Fatal("success=false must error")
	}
}
