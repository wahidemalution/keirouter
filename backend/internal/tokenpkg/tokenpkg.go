// Package tokenpkg holds the settings and fulfilment rules for prepaid
// token packages.
package tokenpkg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/mydisha/keirouter/backend/internal/store"
)

// SettingsKey is the settings row holding Settings as JSON.
const SettingsKey = "token_packages_settings"

// DefaultModels are the models seeded on the token plan. The plan's
// allowed_models is the only source of the list.
var DefaultModels = []string{"gpt-6-sol", "gpt-6-terra", "gpt-6-luna"}

// DefaultPlanID is the seeded plan whose RPM and concurrency limits apply to
// every token package key.
const DefaultPlanID = "plan-token-packages"

// TermsVersion is the token package terms version users must accept at
// checkout. Bump it whenever the token part of the terms page
// (/ketentuan#paket-token) changes materially, together with
// TOKEN_TERMS_VERSION in the frontend.
const TermsVersion = "2026-11-11"

// ErrNoActivePackage is returned when a days package is bought without a
// running subscription to extend.
var ErrNoActivePackage = errors.New("tokenpkg: no active package")

var errUnknownKind = errors.New("tokenpkg: unknown package kind")

// Settings configures the token package product. The models token keys may
// call live on the plan (PlanID), not here.
type Settings struct {
	Enabled bool   `json:"enabled"`
	PlanID  string `json:"plan_id"`
}

// Load returns the saved settings, or the defaults when none are saved.
func Load(ctx context.Context, settings *store.SettingsRepo) (Settings, error) {
	def := Settings{PlanID: DefaultPlanID}
	raw, err := settings.Get(ctx, SettingsKey)
	if errors.Is(err, store.ErrNotFound) || (err == nil && raw == "") {
		return def, nil
	}
	if err != nil {
		return def, err
	}
	var s Settings
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return def, fmt.Errorf("tokenpkg: decode settings: %w", err)
	}
	if s.PlanID == "" {
		s.PlanID = DefaultPlanID
	}
	return s, nil
}

// Save stores s.
func Save(ctx context.Context, settings *store.SettingsRepo, s Settings) error {
	b, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("tokenpkg: encode settings: %w", err)
	}
	return settings.Set(ctx, SettingsKey, string(b))
}

// State is a user's current subscription, if any.
type State struct {
	Exists      bool
	ExpiresAt   time.Time
	UsedTokens  int64
	LimitTokens int64
}

// Result is the subscription after a purchase is applied.
type Result struct {
	ExpiresAt   time.Time
	LimitTokens int64
}

// Apply computes the subscription after buying p. A subscription is active
// while now is before ExpiresAt.
//
// A tokens package on an active subscription adds to the limit and keeps the
// expiry. On an expired or missing one it restarts the period; unused tokens
// from the old period are dropped, so the new limit is the used count plus
// the package tokens.
func Apply(st State, p store.TokenPackage, now time.Time) (Result, error) {
	active := st.Exists && now.Before(st.ExpiresAt)
	extend := time.Duration(p.Days) * 24 * time.Hour
	switch p.Kind {
	case store.TokenPackageTokens:
		if active {
			return Result{ExpiresAt: st.ExpiresAt, LimitTokens: st.LimitTokens + p.Tokens}, nil
		}
		used := int64(0)
		if st.Exists {
			used = st.UsedTokens
		}
		return Result{ExpiresAt: now.Add(extend), LimitTokens: used + p.Tokens}, nil
	case store.TokenPackageDays:
		if !active {
			return Result{}, ErrNoActivePackage
		}
		return Result{ExpiresAt: st.ExpiresAt.Add(extend), LimitTokens: st.LimitTokens}, nil
	}
	return Result{}, fmt.Errorf("%w: %q", errUnknownKind, p.Kind)
}
