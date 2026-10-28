package finance

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mydisha/keirouter/backend/internal/market"
	"github.com/mydisha/keirouter/backend/internal/store"
)

// ErrBadRange is returned (wrapped) for an unknown range name.
var ErrBadRange = errors.New("finance: unknown range")

// Loader reads the data the finance math needs from the store. Admin finance
// is single-tenant and always reads store.DefaultTenantID.
type Loader struct {
	DB       *store.DB
	Settings func(ctx context.Context, key string) (string, error)
	InFlight func() map[string]int64 // may be nil
	Now      func() time.Time        // nil = time.Now
}

// Report is the finance dashboard payload for one range.
type Report struct {
	Range    string
	Overview Overview
}

func (l *Loader) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now()
}

func (l *Loader) inflight() map[string]int64 {
	if l.InFlight == nil {
		return map[string]int64{}
	}
	if m := l.InFlight(); m != nil {
		return m
	}
	return map[string]int64{}
}

// Markups returns the global market markup and each chain's effective markup.
func (l *Loader) Markups(ctx context.Context) (Markups, error) {
	var global float64
	if l.Settings != nil {
		global = market.LoadSettings(ctx, l.Settings).MarkupPercent
	} else {
		global = market.DefaultSettings().MarkupPercent
	}
	chains, err := l.DB.Chains().ListByTenant(ctx, store.DefaultTenantID)
	if err != nil {
		return Markups{}, fmt.Errorf("finance: list chains: %w", err)
	}
	m := Markups{Global: global, ByChain: make(map[string]float64, len(chains))}
	for _, c := range chains {
		m.ByChain[c.ID] = market.EffectiveMarkup(c.MarkupPercent, global)
	}
	return m, nil
}

// Report builds the overview for a range.
func (l *Loader) Report(ctx context.Context, rangeName string) (Report, error) {
	now := l.now()
	start, err := RangeStart(rangeName, now)
	if err != nil {
		return Report{}, fmt.Errorf("%w: %q", ErrBadRange, rangeName)
	}
	m, err := l.Markups(ctx)
	if err != nil {
		return Report{}, err
	}
	rangeRows, err := l.DB.Usage().CostBasis(ctx, store.DefaultTenantID, start)
	if err != nil {
		return Report{}, err
	}
	paid, err := l.DB.PaymentOrders().PaidTotalsSince(ctx, start)
	if err != nil {
		return Report{}, err
	}
	outstanding, err := l.DB.Usage().OutstandingPortalCredit(ctx, store.DefaultTenantID)
	if err != nil {
		return Report{}, err
	}
	return Report{
		Range:    rangeName,
		Overview: BuildOverview(rangeRows, m, paid, outstanding, l.inflight()),
	}, nil
}
