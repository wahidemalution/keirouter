// Package turnstile verifies Cloudflare Turnstile tokens against the
// Siteverify API. It has no external dependencies.
package turnstile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const defaultVerifyURL = "https://challenges.cloudflare.com/turnstile/v0/siteverify"

// Config configures a Verifier. When Enabled is false, Verify is a no-op. The
// zero value is disabled.
type Config struct {
	Enabled    bool
	SiteKey    string
	SecretKey  string
	HTTPClient *http.Client
	// VerifyURL overrides the Siteverify endpoint (tests only).
	VerifyURL string
}

// Verifier validates Turnstile tokens. A nil *Verifier is disabled.
type Verifier struct {
	enabled   bool
	siteKey   string
	secretKey string
	verifyURL string
	client    *http.Client
}

// New builds a Verifier. Enabled is only effective when a secret key is set.
func New(cfg Config) *Verifier {
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	url := cfg.VerifyURL
	if url == "" {
		url = defaultVerifyURL
	}
	return &Verifier{
		enabled:   cfg.Enabled && strings.TrimSpace(cfg.SecretKey) != "",
		siteKey:   cfg.SiteKey,
		secretKey: cfg.SecretKey,
		verifyURL: url,
		client:    client,
	}
}

// Enabled reports whether verification is active.
func (v *Verifier) Enabled() bool { return v != nil && v.enabled }

// SiteKey returns the public site key (safe to send to browsers).
func (v *Verifier) SiteKey() string {
	if v == nil {
		return ""
	}
	return v.siteKey
}

type verifyResponse struct {
	Success    bool     `json:"success"`
	ErrorCodes []string `json:"error-codes"`
}

// Verify posts the token to Siteverify. It returns nil only when Cloudflare
// answers success=true. A disabled verifier (or nil receiver) always returns
// nil. Failures never leak the secret key.
func (v *Verifier) Verify(ctx context.Context, token, remoteIP string) error {
	if !v.Enabled() {
		return nil
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return errors.New("empty turnstile token")
	}
	form := url.Values{}
	form.Set("secret", v.secretKey)
	form.Set("response", token)
	if ip := strings.TrimSpace(remoteIP); ip != "" {
		form.Set("remoteip", ip)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, v.verifyURL, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("turnstile request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := v.client.Do(req)
	if err != nil {
		return fmt.Errorf("turnstile verify: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("turnstile status: %d", resp.StatusCode)
	}

	var out verifyResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return fmt.Errorf("turnstile decode: %w", err)
	}
	if !out.Success {
		if len(out.ErrorCodes) > 0 {
			return fmt.Errorf("turnstile rejected: %s", strings.Join(out.ErrorCodes, ","))
		}
		return errors.New("turnstile rejected")
	}
	return nil
}
