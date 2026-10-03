package config

import (
	"os"
	"testing"
	"time"
)

func TestLoadAllowPrivateBaseURLFromEnv(t *testing.T) {
	tests := []struct {
		name string
		key  string
	}{
		{
			name: "canonical env var",
			key:  "KEIROUTER_SECURITY__ALLOW_PRIVATE_BASE_URL",
		},
		{
			name: "legacy missing underscore env var",
			key:  "KEIROUTER_SECURITY__ALLOW_PRIVATE_BASEURL",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			unsetEnv(t, "KEIROUTER_SECURITY__ALLOW_PRIVATE_BASE_URL")
			unsetEnv(t, "KEIROUTER_SECURITY__ALLOW_PRIVATE_BASEURL")
			t.Setenv(tt.key, "true")

			cfg, err := Load("")
			if err != nil {
				t.Fatalf("Load returned error: %v", err)
			}
			if !cfg.Security.AllowPrivateBaseURL {
				t.Fatalf("AllowPrivateBaseURL = false, want true")
			}
		})
	}
}

func TestStreamHeartbeatIntervalConfig(t *testing.T) {
	if got := Default().Server.StreamHeartbeatInterval; got != 15*time.Second {
		t.Fatalf("default StreamHeartbeatInterval = %v, want 15s", got)
	}

	path := t.TempDir() + "/config.yaml"
	if err := os.WriteFile(path, []byte("server:\n  stream_heartbeat_interval: 9s\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if got := cfg.Server.StreamHeartbeatInterval; got != 9*time.Second {
		t.Fatalf("StreamHeartbeatInterval = %v, want 9s", got)
	}
}

func TestStreamHeartbeatIntervalFromEnv(t *testing.T) {
	t.Setenv("KEIROUTER_SERVER__STREAM_HEARTBEAT_INTERVAL", "7s")
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if got := cfg.Server.StreamHeartbeatInterval; got != 7*time.Second {
		t.Fatalf("StreamHeartbeatInterval = %v, want 7s", got)
	}
}

func unsetEnv(t *testing.T, key string) {
	t.Helper()

	old, ok := os.LookupEnv(key)
	if err := os.Unsetenv(key); err != nil {
		t.Fatalf("unset %s: %v", key, err)
	}
	t.Cleanup(func() {
		if ok {
			_ = os.Setenv(key, old)
			return
		}
		_ = os.Unsetenv(key)
	})
}

func TestDefaultStreamStallTimeoutAllowsLongReasoningPauses(t *testing.T) {
	if got := Default().Server.StreamStallTimeout; got != 5*time.Minute {
		t.Fatalf("default stream stall timeout = %v, want 5m", got)
	}
}

func TestDefaultMaxRequestBodyBytesAllowsLargeConversations(t *testing.T) {
	if got := Default().Server.MaxRequestBodyBytes; got != 128<<20 {
		t.Fatalf("default max request body = %d, want %d", got, int64(128<<20))
	}
}

func TestLoadMaxRequestBodyBytesFromEnv(t *testing.T) {
	t.Setenv("KEIROUTER_SERVER__MAX_REQUEST_BODY_BYTES", "67108864")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if got := cfg.Server.MaxRequestBodyBytes; got != 64<<20 {
		t.Fatalf("max request body from env = %d, want %d", got, int64(64<<20))
	}
}

func TestPortalSSOValidation(t *testing.T) {
	// Enabled without credentials must fail closed.
	cfg := Default()
	cfg.PortalSSO.Enabled = true
	if err := cfg.validate(); err == nil {
		t.Fatal("expected error when portal_sso enabled without client id/secret")
	}

	cfg = Default()
	cfg.PortalSSO.Enabled = true
	cfg.PortalSSO.GoogleClientID = "id"
	cfg.PortalSSO.GoogleClientSecret = "secret"
	if err := cfg.validate(); err != nil {
		t.Fatalf("expected valid config, got %v", err)
	}

	// Disabled without credentials is fine.
	cfg = Default()
	if err := cfg.validate(); err != nil {
		t.Fatalf("disabled portal_sso must validate, got %v", err)
	}
}

func TestPaymentConfigDefaultsAndValidation(t *testing.T) {
	c := Default()
	if c.Payment.Enabled {
		t.Fatal("payment must default disabled")
	}
	if c.Payment.BaseURL != "https://api-pay-sandbox.sumopod.com" {
		t.Fatalf("unexpected default base url %q", c.Payment.BaseURL)
	}
	if c.Payment.MinTopupIDR != 10000 || c.Payment.MaxTopupIDR != 10000000 {
		t.Fatalf("unexpected default min/max: %d/%d", c.Payment.MinTopupIDR, c.Payment.MaxTopupIDR)
	}

	// enabled without api_key must fail validation
	bad := Default()
	bad.Payment.Enabled = true
	if err := bad.validate(); err == nil {
		t.Fatal("expected validation error for enabled payment without api_key")
	}

	// enabled with api_key and sane bounds passes
	ok := Default()
	ok.Payment.Enabled = true
	ok.Payment.APIKey = "key"
	ok.Payment.Packages = []PaymentPackage{{ID: "idr10k", Label: "Rp 10.000", AmountIDR: 10000}}
	if err := ok.validate(); err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}

	// package outside [min,max] rejected
	out := ok
	out.Payment.Packages = []PaymentPackage{{ID: "x", Label: "x", AmountIDR: 5}}
	if err := out.validate(); err == nil {
		t.Fatal("expected validation error for out-of-range package")
	}
}

func TestPaymentConfigEnvOverride(t *testing.T) {
	t.Setenv("KEIROUTER_PAYMENT__API_KEY", "sekret")
	t.Setenv("KEIROUTER_PAYMENT__WEBHOOK_SECRET", "whsec_x")
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Payment.APIKey != "sekret" || cfg.Payment.WebhookSecret != "whsec_x" {
		t.Fatalf("env override not applied: %+v", cfg.Payment)
	}
}

func TestTurnstileValidation(t *testing.T) {
	cfg := Default()
	if cfg.Turnstile.Enabled {
		t.Fatal("turnstile must default disabled")
	}
	// Enabled without keys must fail.
	cfg = Default()
	cfg.Turnstile.Enabled = true
	if err := cfg.validate(); err == nil {
		t.Fatal("expected error when enabled without keys")
	}
	// Enabled with both keys must pass.
	cfg = Default()
	cfg.Turnstile.Enabled = true
	cfg.Turnstile.SiteKey = "1x00000000000000000000AA"
	cfg.Turnstile.SecretKey = "1x0000000000000000000000000000000AA"
	if err := cfg.validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
