package gateway

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/mydisha/keirouter/backend/internal/budget"
	"github.com/mydisha/keirouter/backend/internal/crypto"
	"github.com/mydisha/keirouter/backend/internal/store"
	"github.com/mydisha/keirouter/backend/internal/tokenpkg"
)

// markTokenOrderPaid settles a token package order exactly once: the guarded
// transition out of a settleable status must win, then the user's token key
// gains the package (created on the first purchase). It never adds PAYG
// credit. A payment that cannot be applied (package deleted, or a days
// package with no active package) moves the order to manual with reason
// refund_required, grants nothing and reports applied=false.
func (s *Server) markTokenOrderPaid(ctx context.Context, order store.PaymentOrder, to store.PaymentOrderStatus, actor, reason string) (bool, error) {
	if !order.Status.Settleable() {
		return false, nil
	}
	if order.Product != store.ProductToken {
		return false, fmt.Errorf("token: order %s is a %s order", order.ID, productOrCredit(order.Product))
	}
	if order.TokenPackageID == "" {
		return false, errors.New("token: order has no package")
	}
	reason = latePaymentReason(order.Status, reason)

	tx, err := s.db.SQL().BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	// Serialize fulfilments per user so two paid orders cannot both read the
	// same limit, or both create the token key.
	if err := s.budgets.LockKeyTopup(ctx, tx, "token_sub:"+order.GoogleSub); err != nil {
		return false, err
	}

	pkg, err := s.db.TokenPackages().GetOnTx(ctx, tx, order.TokenPackageID)
	if errors.Is(err, store.ErrNotFound) {
		return s.markTokenOrderManualOnTx(ctx, tx, order, actor, "package deleted", reason)
	}
	if err != nil {
		return false, err
	}

	now := time.Now()
	var (
		state tokenpkg.State
		sub   store.TokenSubscription
		bud   store.Budget
	)
	sub, err = s.db.TokenSubscriptions().GetBySubOnTx(ctx, tx, order.GoogleSub)
	switch {
	case err == nil:
		if bud, err = s.tokenBudgetOnTx(ctx, tx, sub.KeyID, now); err != nil {
			return false, err
		}
		_, used, err := s.db.Usage().SpendAndTokens(ctx, store.ScopeAPIKey, sub.KeyID, budget.PeriodStart("total", now))
		if err != nil {
			return false, err
		}
		state = tokenpkg.State{Exists: true, ExpiresAt: sub.ExpiresAt, UsedTokens: used, LimitTokens: bud.LimitTokens}
	case !errors.Is(err, store.ErrNotFound):
		return false, err
	}

	res, err := tokenpkg.Apply(state, pkg, now)
	if errors.Is(err, tokenpkg.ErrNoActivePackage) {
		return s.markTokenOrderManualOnTx(ctx, tx, order, actor, "no active package", reason)
	}
	if err != nil {
		return false, err
	}

	applied, err := s.db.PaymentOrders().TransitionFromOnTx(ctx, tx, order.ID, store.PaymentSettleable, to, actor, reason, now.UTC().Format(time.RFC3339))
	if err != nil || !applied {
		return false, err
	}
	if !state.Exists {
		user, err := s.db.PortalUsers().GetBySub(ctx, order.GoogleSub)
		if err != nil {
			return false, fmt.Errorf("token: load portal user: %w", err)
		}
		settings, err := tokenpkg.Load(ctx, s.db.Settings())
		if err != nil {
			return false, err
		}
		if sub, bud, err = s.ensureTokenKeyOnTx(ctx, tx, order.GoogleSub, user.Email, settings); err != nil {
			return false, err
		}
	}
	if err := s.budgets.SetTokenLimitOnTx(ctx, tx, bud.ID, res.LimitTokens); err != nil {
		return false, err
	}
	if err := s.db.TokenSubscriptions().SetExpiresOnTx(ctx, tx, order.GoogleSub, res.ExpiresAt); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	if s.budgetEngine != nil {
		s.budgetEngine.InvalidateBudgetCacheForScope(store.ScopeAPIKey, sub.KeyID)
	}
	s.invalidateTokenSub(sub.KeyID)
	return true, nil
}

// markTokenOrderManualOnTx records a payment that cannot be applied, so an
// admin refunds it. It commits tx and always reports applied=false.
func (s *Server) markTokenOrderManualOnTx(ctx context.Context, tx *sql.Tx, order store.PaymentOrder, actor, why, reason string) (bool, error) {
	if reason = strings.TrimSpace(reason); reason != "" {
		why += ". " + reason
	}
	reason = "refund_required: " + why
	moved, err := s.db.PaymentOrders().TransitionFromOnTx(ctx, tx, order.ID, store.PaymentSettleable, store.PaymentManual, actor, reason, time.Now().UTC().Format(time.RFC3339))
	if err != nil || !moved {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	s.log.Error("token: payment cannot be applied; refund it", "order", order.ID, "package", order.TokenPackageID, "reason", reason)
	return false, nil
}

// tokenBudgetOnTx returns the token key's budget, recreating it when it was
// removed so the purchase still lands on a token-only budget.
func (s *Server) tokenBudgetOnTx(ctx context.Context, tx *sql.Tx, keyID string, now time.Time) (store.Budget, error) {
	bs, err := s.budgets.ListByScope(ctx, store.ScopeAPIKey, keyID)
	if err != nil {
		return store.Budget{}, err
	}
	if len(bs) > 0 {
		return bs[0], nil
	}
	b := newTokenBudget(keyID, now)
	return b, s.budgets.CreateOnTx(ctx, tx, b)
}

func newTokenBudget(keyID string, now time.Time) store.Budget {
	return store.Budget{
		ID: uuid.NewString(), TenantID: store.DefaultTenantID, ScopeKind: store.ScopeAPIKey, ScopeID: keyID,
		LimitMicros: 0, LimitTokens: 0, Period: "total", AlertPct: 80, HardCutoff: true,
		CreatedAt: now, UpdatedAt: now,
	}
}

// ensureTokenKeyOnTx creates a user's token key: the key, its token-only
// budget, its model access and the subscription holding the sealed key. The
// subscription starts expired; the caller sets the real expiry.
func (s *Server) ensureTokenKeyOnTx(ctx context.Context, tx *sql.Tx, sub, email string, settings tokenpkg.Settings) (store.TokenSubscription, store.Budget, error) {
	issued, err := s.identity.Generate(store.DefaultTenantID, "", "token:"+email)
	if err != nil {
		return store.TokenSubscription{}, store.Budget{}, err
	}
	issued.Record.PlanID = settings.PlanID
	if err := s.identity.Keys().CreateOnTx(ctx, tx, issued.Record); err != nil {
		return store.TokenSubscription{}, store.Budget{}, err
	}
	now := time.Now()
	bud := newTokenBudget(issued.Record.ID, now)
	if err := s.budgets.CreateOnTx(ctx, tx, bud); err != nil {
		return store.TokenSubscription{}, store.Budget{}, err
	}
	if err := s.identity.Keys().SetAllowedModelsOnTx(ctx, tx, issued.Record.ID, settings.AllowedModels); err != nil {
		return store.TokenSubscription{}, store.Budget{}, err
	}
	sealed := crypto.Sealed{}
	if s.vault != nil {
		if sealed, err = s.vault.Sealer().SealString(issued.Plaintext); err != nil {
			return store.TokenSubscription{}, store.Budget{}, err
		}
	}
	ts := store.TokenSubscription{
		GoogleSub: sub, KeyID: issued.Record.ID, SealedKey: sealed,
		ExpiresAt: now, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.db.TokenSubscriptions().CreateOnTx(ctx, tx, ts); err != nil {
		return store.TokenSubscription{}, store.Budget{}, err
	}
	return ts, bud, nil
}

// invalidateTokenSub is a no-op until Task 4 adds the subscription cache.
func (s *Server) invalidateTokenSub(keyID string) {}
