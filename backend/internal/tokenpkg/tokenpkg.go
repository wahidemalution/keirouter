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

// Limits are a subscription's or package's own key rate limits. A nil field
// follows the token plan.
type Limits struct {
	RPM         *int64
	Concurrency *int64
}

// PlanLimits are the token plan's key rate limits; 0 is unlimited.
type PlanLimits struct {
	RPM         int64
	Concurrency int64
}

// State is a user's current subscription, if any.
type State struct {
	Exists      bool
	ExpiresAt   time.Time
	UsedTokens  int64
	LimitTokens int64
	Limits      Limits
}

// Result is the subscription after a purchase is applied.
type Result struct {
	ExpiresAt   time.Time
	LimitTokens int64
	Limits      Limits
}

// Apply computes the subscription after buying p. A subscription is active
// while now is before ExpiresAt.
//
// A tokens package on an active subscription adds to the limit and keeps the
// expiry. On an expired or missing one it restarts the period; unused tokens
// from the old period are dropped, so the new limit is the used count plus
// the package tokens.
//
// Rate limits: a tokens package on an active subscription keeps the highest
// of the subscription's and the package's limits, with plan as the value of
// a nil limit. A new period takes the package's limits as they are. A days
// package leaves them.
func Apply(st State, p store.TokenPackage, plan PlanLimits, now time.Time) (Result, error) {
	active := st.Exists && now.Before(st.ExpiresAt)
	extend := time.Duration(p.Days) * 24 * time.Hour
	switch p.Kind {
	case store.TokenPackageTokens:
		if active {
			return Result{
				ExpiresAt: st.ExpiresAt, LimitTokens: st.LimitTokens + p.Tokens,
				Limits: Limits{
					RPM:         stack(st.Limits.RPM, p.RPMLimit, plan.RPM),
					Concurrency: stack(st.Limits.Concurrency, p.ConcurrencyLimit, plan.Concurrency),
				},
			}, nil
		}
		used := int64(0)
		if st.Exists {
			used = st.UsedTokens
		}
		return Result{
			ExpiresAt: now.Add(extend), LimitTokens: used + p.Tokens,
			Limits: Limits{RPM: clone(p.RPMLimit), Concurrency: clone(p.ConcurrencyLimit)},
		}, nil
	case store.TokenPackageDays:
		if !active {
			return Result{}, ErrNoActivePackage
		}
		return Result{
			ExpiresAt: st.ExpiresAt.Add(extend), LimitTokens: st.LimitTokens,
			Limits: Limits{RPM: clone(st.Limits.RPM), Concurrency: clone(st.Limits.Concurrency)},
		}, nil
	}
	return Result{}, fmt.Errorf("%w: %q", errUnknownKind, p.Kind)
}

// stack returns the higher of two limits, reading nil as the plan's value
// (plan 0 is unlimited and beats any number). When both are set it returns
// the larger, even if both sit below the plan. When the plan's value wins
// (ties included) it returns nil, so the subscription keeps following the
// plan if the admin changes it later.
func stack(old, pkg *int64, plan int64) *int64 {
	switch {
	case old == nil && pkg == nil:
		return nil
	case old != nil && pkg != nil:
		n := max(*old, *pkg)
		return &n
	}
	own := old
	if own == nil {
		own = pkg
	}
	if plan <= 0 || plan >= *own {
		return nil
	}
	return clone(own)
}

func clone(v *int64) *int64 {
	if v == nil {
		return nil
	}
	n := *v
	return &n
}
