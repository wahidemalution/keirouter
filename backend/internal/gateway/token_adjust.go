package gateway

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/mydisha/keirouter/backend/internal/budget"
	"github.com/mydisha/keirouter/backend/internal/store"
)

// maxTokensLeft caps one adjustment so used + tokens_left cannot overflow.
const maxTokensLeft int64 = 1_000_000_000_000

// adjustTokensRequest takes exactly one of TokensLeft (set the balance) or
// DeltaTokens (add to it, or remove from it when negative).
type adjustTokensRequest struct {
	TokensLeft     *int64 `json:"tokens_left"`
	DeltaTokens    *int64 `json:"delta_tokens"`
	Reason         string `json:"reason"`
	IdempotencyKey string `json:"idempotency_key"`
}

// adminAdjustTokenKey sets how many tokens a subscriber's token key has left,
// or adds or removes tokens. The server turns the request into a limit from
// usage read under the same per-user lock as package fulfilment, so a
// purchase landing at the same time cannot be lost. Every change is recorded
// in key_token_adjustments.
func (s *Server) adminAdjustTokenKey(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	googleSub := chi.URLParam(r, "sub")
	sub, err := s.db.TokenSubscriptions().GetBySub(ctx, googleSub)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "token subscription not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, sanitizeError(s.log, err, "internal server error"))
		return
	}

	var body adjustTokensRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	if (body.TokensLeft == nil) == (body.DeltaTokens == nil) {
		writeError(w, http.StatusBadRequest, "send exactly one of tokens_left or delta_tokens")
		return
	}
	if body.TokensLeft != nil {
		if *body.TokensLeft < 0 {
			writeError(w, http.StatusBadRequest, "tokens_left must not be negative")
			return
		}
		if *body.TokensLeft > maxTokensLeft {
			writeError(w, http.StatusBadRequest, "tokens_left is too large")
			return
		}
	} else {
		if *body.DeltaTokens == 0 {
			writeError(w, http.StatusBadRequest, "delta_tokens must not be 0")
			return
		}
		if *body.DeltaTokens > maxTokensLeft || *body.DeltaTokens < -maxTokensLeft {
			writeError(w, http.StatusBadRequest, "delta_tokens is too large")
			return
		}
	}
	reason := strings.TrimSpace(body.Reason)
	if reason == "" {
		writeError(w, http.StatusBadRequest, "reason is required")
		return
	}
	if len(reason) > 500 {
		writeError(w, http.StatusBadRequest, "reason must be 500 characters or fewer")
		return
	}
	idem := strings.TrimSpace(body.IdempotencyKey)
	if idem == "" {
		idem = strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	}

	if idem != "" {
		if existing, gerr := s.db.TokenAdjustments().GetByIdempotencyKey(ctx, sub.KeyID, idem); gerr == nil {
			writeJSON(w, http.StatusOK, map[string]any{"adjustment": tokenAdjustmentView(existing)})
			return
		} else if !errors.Is(gerr, store.ErrNotFound) {
			writeError(w, http.StatusInternalServerError, sanitizeError(s.log, gerr, "internal server error"))
			return
		}
	}

	tx, err := s.db.SQL().BeginTx(ctx, nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, sanitizeError(s.log, err, "internal server error"))
		return
	}
	defer func() { _ = tx.Rollback() }()
	// The lock markTokenOrderPaid takes: a purchase and an adjustment cannot
	// both read the old limit and overwrite each other.
	if err := s.budgets.LockKeyTopup(ctx, tx, "token_sub:"+googleSub); err != nil {
		writeError(w, http.StatusInternalServerError, sanitizeError(s.log, err, "internal server error"))
		return
	}

	now := time.Now()
	bud, err := s.tokenBudgetOnTx(ctx, tx, sub.KeyID, now)
	if err != nil {
		writeError(w, http.StatusInternalServerError, sanitizeError(s.log, err, "internal server error"))
		return
	}
	_, used, err := s.db.Usage().SpendAndTokens(ctx, store.ScopeAPIKey, sub.KeyID, budget.PeriodStart("total", now))
	if err != nil {
		writeError(w, http.StatusInternalServerError, sanitizeError(s.log, err, "internal server error"))
		return
	}
	var newLimit int64
	if body.TokensLeft != nil {
		newLimit = used + *body.TokensLeft
	} else {
		if bud.LimitTokens <= 0 {
			// 0 reads as unlimited, so there is no balance to add to.
			writeError(w, http.StatusBadRequest, "this key has no token limit; set an exact balance instead")
			return
		}
		left := max(0, bud.LimitTokens-used)
		if *body.DeltaTokens < 0 && -*body.DeltaTokens > left {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("only %d tokens are left, so at most %d can be removed", left, left))
			return
		}
		// A key used past its limit starts again from its usage.
		newLimit = max(bud.LimitTokens, used) + *body.DeltaTokens
		if newLimit-used > maxTokensLeft {
			writeError(w, http.StatusBadRequest, "tokens left would be too large")
			return
		}
	}
	if newLimit <= 0 {
		// A zero token limit reads as unlimited.
		writeError(w, http.StatusBadRequest, "this key has used no tokens, so 0 tokens left would make it unlimited; disable the key instead")
		return
	}
	if newLimit == bud.LimitTokens {
		if s.replayTokenAdjustment(ctx, w, sub.KeyID, idem) {
			return
		}
		writeError(w, http.StatusBadRequest, "tokens left is unchanged")
		return
	}
	if err := s.budgets.SetTokenLimitOnTx(ctx, tx, bud.ID, newLimit); err != nil {
		writeError(w, http.StatusInternalServerError, sanitizeError(s.log, err, "internal server error"))
		return
	}

	rec := store.KeyTokenAdjustment{
		ID: uuid.NewString(), TenantID: store.DefaultTenantID, KeyID: sub.KeyID, BudgetID: bud.ID,
		DeltaTokens: newLimit - bud.LimitTokens, Reason: reason,
		LimitBeforeTokens: bud.LimitTokens, LimitAfterTokens: newLimit, UsedTokens: used,
		IdempotencyKey: idem, Actor: "dashboard", CreatedAt: now,
	}
	if err := s.db.TokenAdjustments().CreateOnTx(ctx, tx, rec); err != nil {
		// A unique-index violation means a concurrent request already applied
		// this idempotency key.
		if s.replayTokenAdjustment(ctx, w, sub.KeyID, idem) {
			return
		}
		writeError(w, http.StatusInternalServerError, sanitizeError(s.log, err, "internal server error"))
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, sanitizeError(s.log, err, "internal server error"))
		return
	}
	if s.budgetEngine != nil {
		s.budgetEngine.InvalidateBudgetCacheForScope(store.ScopeAPIKey, sub.KeyID)
	}
	writeJSON(w, http.StatusCreated, map[string]any{"adjustment": tokenAdjustmentView(rec)})
}

// replayTokenAdjustment writes the adjustment already recorded under idem and
// reports true, or reports false when there is none.
func (s *Server) replayTokenAdjustment(ctx context.Context, w http.ResponseWriter, keyID, idem string) bool {
	if idem == "" {
		return false
	}
	existing, err := s.db.TokenAdjustments().GetByIdempotencyKey(ctx, keyID, idem)
	if err != nil {
		return false
	}
	writeJSON(w, http.StatusOK, map[string]any{"adjustment": tokenAdjustmentView(existing)})
	return true
}

func tokenAdjustmentView(a store.KeyTokenAdjustment) map[string]any {
	return map[string]any{
		"id": a.ID, "key_id": a.KeyID, "delta_tokens": a.DeltaTokens, "reason": a.Reason,
		"limit_before_tokens": a.LimitBeforeTokens, "limit_after_tokens": a.LimitAfterTokens,
		"tokens_left_before": max(0, a.LimitBeforeTokens-a.UsedTokens),
		"tokens_left_after":  max(0, a.LimitAfterTokens-a.UsedTokens),
		"created_at":         a.CreatedAt,
	}
}

func (s *Server) adminListTokenAdjustments(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sub, err := s.db.TokenSubscriptions().GetBySub(ctx, chi.URLParam(r, "sub"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "token subscription not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, sanitizeError(s.log, err, "internal server error"))
		return
	}
	list, err := s.db.TokenAdjustments().ListByKey(ctx, sub.KeyID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, sanitizeError(s.log, err, "internal server error"))
		return
	}
	out := make([]map[string]any, 0, len(list))
	for _, a := range list {
		out = append(out, tokenAdjustmentView(a))
	}
	writeJSON(w, http.StatusOK, map[string]any{"adjustments": out})
}
