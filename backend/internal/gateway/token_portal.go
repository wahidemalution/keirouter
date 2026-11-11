package gateway

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/mydisha/keirouter/backend/internal/budget"
	"github.com/mydisha/keirouter/backend/internal/payment"
	"github.com/mydisha/keirouter/backend/internal/store"
	"github.com/mydisha/keirouter/backend/internal/tokenpkg"
)

// tokenOnSale reports whether token packages can be bought right now.
func (s *Server) tokenOnSale(st tokenpkg.Settings) bool {
	return st.Enabled && s.cfg.Payment.Enabled && s.paymentClient != nil
}

// handlePortalTokenPackages lists the enabled token packages, the models
// token keys may call and the terms version checkout needs. Packages and
// models are empty while the product is off. It needs no
// session so the public page can show them before sign-in. The catalog shows
// whenever the product is enabled, even while payment is off; on_sale tells
// whether checkout is open.
func (s *Server) handlePortalTokenPackages(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	st, err := tokenpkg.Load(ctx, s.db.Settings())
	if err != nil {
		writeError(w, http.StatusInternalServerError, sanitizeError(s.log, err, "internal server error"))
		return
	}
	var pkgs []store.TokenPackage
	models := []string{}
	if st.Enabled {
		plan, err := s.db.Plans().Get(ctx, st.PlanID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusInternalServerError, sanitizeError(s.log, err, "internal server error"))
			return
		}
		models = tokenPlanModels(plan)
		if pkgs, err = s.db.TokenPackages().List(ctx); err != nil {
			writeError(w, http.StatusInternalServerError, sanitizeError(s.log, err, "internal server error"))
			return
		}
	}
	out := make([]map[string]any, 0, len(pkgs))
	for _, p := range pkgs {
		if !p.Enabled {
			continue
		}
		out = append(out, map[string]any{
			"id": p.ID, "name": p.Name, "kind": string(p.Kind), "tokens": p.Tokens,
			"days": p.Days, "price_idr": p.PriceIDR, "featured": p.Featured,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"on_sale": s.tokenOnSale(st), "packages": out, "models": models,
		"terms_version": tokenpkg.TermsVersion,
	})
}

// handlePortalTokenStatus returns the caller's token package: whether it is
// active or disabled, its expiry, token usage against the limit and the
// key's models.
// used_tokens counts the key's whole history; a new period raises the limit
// past the old usage, so limit - used is what is left.
func (s *Server) handlePortalTokenStatus(w http.ResponseWriter, r *http.Request) {
	sessionSub, ok := s.portalSubject(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "portal session required")
		return
	}
	ctx := r.Context()
	settings, err := tokenpkg.Load(ctx, s.db.Settings())
	if err != nil {
		writeError(w, http.StatusInternalServerError, sanitizeError(s.log, err, "internal server error"))
		return
	}
	plan, err := s.db.Plans().Get(ctx, settings.PlanID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusInternalServerError, sanitizeError(s.log, err, "internal server error"))
		return
	}
	out := map[string]any{
		"active": false, "exists": false, "disabled": false, "expires_at": "", "used_tokens": int64(0),
		"limit_tokens": int64(0), "key_display": "", "models": tokenPlanModels(plan),
	}
	sub, err := s.db.TokenSubscriptions().GetBySub(ctx, portalGoogleSub(sessionSub))
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusOK, out)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, sanitizeError(s.log, err, "internal server error"))
		return
	}
	now := time.Now()
	key, err := s.identity.Get(ctx, sub.KeyID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, sanitizeError(s.log, err, "internal server error"))
		return
	}
	used, limit, err := s.tokenUsage(ctx, sub.KeyID, now)
	if err != nil {
		writeError(w, http.StatusInternalServerError, sanitizeError(s.log, err, "internal server error"))
		return
	}
	models, _, err := s.identity.Keys().EffectiveAllowedModels(ctx, sub.KeyID, key.PlanID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, sanitizeError(s.log, err, "internal server error"))
		return
	}
	if len(models) > 0 {
		out["models"] = models
	}
	out["exists"] = true
	out["disabled"] = key.Disabled
	out["active"] = now.Before(sub.ExpiresAt)
	out["expires_at"] = sub.ExpiresAt.UTC().Format(time.RFC3339)
	out["used_tokens"] = used
	out["limit_tokens"] = limit
	out["key_display"] = key.Display
	writeJSON(w, http.StatusOK, out)
}

// tokenUsage returns the tokens a key has used over its whole history and
// its current token limit (0 when it has no budget).
func (s *Server) tokenUsage(ctx context.Context, keyID string, now time.Time) (used, limit int64, err error) {
	bs, err := s.budgets.ListByScope(ctx, store.ScopeAPIKey, keyID)
	if err != nil {
		return 0, 0, err
	}
	_, used, err = s.db.Usage().SpendAndTokens(ctx, store.ScopeAPIKey, keyID, budget.PeriodStart("total", now))
	if err != nil {
		return 0, 0, err
	}
	if len(bs) > 0 {
		limit = bs[0].LimitTokens
	}
	return used, limit, nil
}

// handlePortalTokenUsage returns usage for the caller's token key, in the same
// shape as handlePortalUsage returns for the credit key.
func (s *Server) handlePortalTokenUsage(w http.ResponseWriter, r *http.Request) {
	sessionSub, ok := s.portalSubject(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "portal session required")
		return
	}
	sub, err := s.db.TokenSubscriptions().GetBySub(r.Context(), portalGoogleSub(sessionSub))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusConflict, "no token key")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load token package")
		return
	}
	s.writePortalKeyUsage(w, r, sub.KeyID, true)
}

// handlePortalTokenKeyReveal returns the plaintext of the caller's token key.
func (s *Server) handlePortalTokenKeyReveal(w http.ResponseWriter, r *http.Request) {
	sessionSub, ok := s.portalSubject(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "portal session required")
		return
	}
	sub, err := s.db.TokenSubscriptions().GetBySub(r.Context(), portalGoogleSub(sessionSub))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "no token package")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, sanitizeError(s.log, err, "internal server error"))
		return
	}
	if sub.SealedKey.WrappedDEK == "" || sub.SealedKey.Ciphertext == "" {
		writeError(w, http.StatusNotFound, "this key cannot be revealed")
		return
	}
	if s.vault == nil {
		writeError(w, http.StatusInternalServerError, "vault not configured")
		return
	}
	plaintext, err := s.vault.Sealer().OpenString(sub.SealedKey)
	if err != nil {
		s.log.Error("portal token key reveal: open failed", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to reveal key")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"key": plaintext})
}

type createTokenOrderRequest struct {
	PackageID      string `json:"package_id"`
	TermsVersion   string `json:"terms_version"`
	IdempotencyKey string `json:"idempotency_key"`
	TurnstileToken string `json:"turnstile_token"`
}

// handlePortalCreateTokenOrder creates a SumoPod payment for a token package.
// The order carries the caller's PAYG key id only for ownership; fulfilment
// goes by GoogleSub to the separate token key.
func (s *Server) handlePortalCreateTokenOrder(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	settings, err := tokenpkg.Load(ctx, s.db.Settings())
	if err != nil {
		writeError(w, http.StatusInternalServerError, sanitizeError(s.log, err, "internal server error"))
		return
	}
	if !s.tokenOnSale(settings) {
		writeError(w, http.StatusServiceUnavailable, "token packages are not available")
		return
	}
	var body createTokenOrderRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.TermsVersion != tokenpkg.TermsVersion {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{
			"message": "accept the current token package terms first", "type": "invalid_request_error",
			"code": "terms_not_accepted", "terms_version": tokenpkg.TermsVersion,
		}})
		return
	}
	if !s.turnstileVerify(w, r, body.TurnstileToken) {
		return
	}
	u, keyID, ok := s.portalKeyID(w, r)
	if !ok {
		return
	}
	key, err := s.identity.Get(ctx, keyID)
	if err != nil || key.Disabled {
		writeError(w, http.StatusConflict, "api key is not usable")
		return
	}
	idem := strings.TrimSpace(body.IdempotencyKey)
	if idem == "" {
		idem = strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	}
	if len(idem) > maxIdempotencyKeyLen {
		writeError(w, http.StatusBadRequest, "idempotency key is too long")
		return
	}

	unlock := s.lockTokenOrders(u.GoogleSub)
	defer unlock()

	tsub, serr := s.db.TokenSubscriptions().GetBySub(ctx, u.GoogleSub)
	hasSub := serr == nil
	if serr != nil && !errors.Is(serr, store.ErrNotFound) {
		writeError(w, http.StatusInternalServerError, sanitizeError(s.log, serr, "internal server error"))
		return
	}
	if hasSub {
		tkey, err := s.identity.Get(ctx, tsub.KeyID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, sanitizeError(s.log, err, "internal server error"))
			return
		}
		// A disabled key cannot use what it buys; an admin enables it first.
		if tkey.Disabled {
			writeJSON(w, http.StatusConflict, map[string]any{"error": map[string]any{
				"message": "the token key is disabled", "type": "invalid_request_error",
				"code": "token_key_disabled",
			}})
			return
		}
	}

	if idem != "" {
		existing, gerr := s.db.PaymentOrders().GetByIdempotencyKey(ctx, idem)
		if gerr == nil {
			if existing.KeyID != keyID || existing.Product != store.ProductToken {
				writeError(w, http.StatusConflict, "idempotency key already used")
				return
			}
			writeJSON(w, http.StatusOK, tokenOrderResponse(existing))
			return
		}
		if !errors.Is(gerr, store.ErrNotFound) {
			writeError(w, http.StatusInternalServerError, sanitizeError(s.log, gerr, "internal server error"))
			return
		}
	}

	pkg, err := s.db.TokenPackages().Get(ctx, strings.TrimSpace(body.PackageID))
	if errors.Is(err, store.ErrNotFound) || (err == nil && !pkg.Enabled) {
		writeError(w, http.StatusNotFound, "package not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, sanitizeError(s.log, err, "internal server error"))
		return
	}
	now := time.Now()
	if pkg.Kind == store.TokenPackageDays {
		if !hasSub || !now.Before(tsub.ExpiresAt) {
			writeJSON(w, http.StatusConflict, map[string]any{"error": map[string]any{
				"message": "a days package needs an active token package", "type": "invalid_request_error",
				"code": "no_active_package",
			}})
			return
		}
	}

	orders, err := s.db.PaymentOrders().ListByKey(ctx, keyID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, sanitizeError(s.log, err, "internal server error"))
		return
	}
	if open, found := s.openTokenOrder(orders, pkg, now); found {
		writeJSON(w, http.StatusOK, tokenOrderResponse(open))
		return
	}

	p := s.cfg.Payment
	orderID := uuid.NewString()
	base := s.publicBaseURL(r)
	created, err := s.paymentClient.CreatePayment(ctx, payment.CreateRequest{
		OrderID: orderID, AmountIDR: pkg.PriceIDR, Currency: "IDR",
		ExpiresInHours:        p.ExpiresInHours,
		SuccessReturnURL:      base + "/portal/token?status=success",
		CancelReturnURL:       base + "/portal/token?status=cancel",
		PaymentMethodTypeCode: p.PaymentMethodTypeCode,
	})
	if err != nil {
		s.log.Error("token: create payment failed", "err", err)
		writeError(w, http.StatusBadGateway, "failed to create payment")
		return
	}

	now = time.Now()
	expires := created.ExpiresAt
	if _, perr := time.Parse(time.RFC3339, expires); perr != nil {
		expires = now.Add(time.Duration(paymentLinkHours(p.ExpiresInHours)) * time.Hour).UTC().Format(time.RFC3339)
	}
	o := store.PaymentOrder{
		ID: orderID, TenantID: adminTenant, KeyID: keyID, GoogleSub: u.GoogleSub,
		AmountIDR: pkg.PriceIDR, Status: store.PaymentPending, Provider: p.Provider,
		ProviderPaymentID: created.PaymentID, PaymentLinkURL: created.PaymentLinkURL,
		IdempotencyKey: idem, Actor: "portal", CreatedAt: now, UpdatedAt: now,
		ExpiresAt: expires, Product: store.ProductToken, TokenPackageID: pkg.ID,
		TokenKind: string(pkg.Kind), TokenAmount: pkg.Tokens, TokenDays: pkg.Days,
		TermsVersion: tokenpkg.TermsVersion, TermsAcceptedAt: now.UTC().Format(time.RFC3339),
	}
	if err := s.db.PaymentOrders().Create(ctx, o); err != nil {
		// Another user's order may hold this idempotency key.
		if idem != "" {
			if existing, gerr := s.db.PaymentOrders().GetByIdempotencyKey(ctx, idem); gerr == nil && existing.ID != orderID {
				writeError(w, http.StatusConflict, "idempotency key already used")
				return
			}
		}
		writeError(w, http.StatusInternalServerError, sanitizeError(s.log, err, "internal server error"))
		return
	}
	writeJSON(w, http.StatusCreated, tokenOrderResponse(o))
}

func tokenOrderResponse(o store.PaymentOrder) map[string]any {
	return map[string]any{
		"id": o.ID, "status": string(o.Status), "payment_link_url": o.PaymentLinkURL, "amount_idr": o.AmountIDR,
	}
}

// lockTokenOrders serializes token checkouts per portal user. It shares the
// credit lock map; the "token:" prefix keeps the keys apart from key ids.
func (s *Server) lockTokenOrders(googleSub string) func() {
	return s.lockCreditOrders("token:" + googleSub)
}

// openTokenOrder returns the newest token order for pkg whose payment link
// can still be paid. An order whose price or snapshot no longer matches the
// package is left alone, so a checkout after an admin edit pays the new terms.
func (s *Server) openTokenOrder(orders []store.PaymentOrder, pkg store.TokenPackage, now time.Time) (store.PaymentOrder, bool) {
	var best store.PaymentOrder
	found := false
	for _, o := range orders {
		if o.Product != store.ProductToken || o.TokenPackageID != pkg.ID || !s.paymentLinkOpen(o, now) {
			continue
		}
		if o.AmountIDR != pkg.PriceIDR || o.TokenKind != string(pkg.Kind) ||
			o.TokenAmount != pkg.Tokens || o.TokenDays != pkg.Days {
			continue
		}
		if !found || o.CreatedAt.After(best.CreatedAt) {
			best, found = o, true
		}
	}
	return best, found
}

// handlePortalListTokenOrders lists the caller's token package orders.
func (s *Server) handlePortalListTokenOrders(w http.ResponseWriter, r *http.Request) {
	_, keyID, ok := s.portalKeyID(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	orders, err := s.db.PaymentOrders().ListByKey(ctx, keyID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, sanitizeError(s.log, err, "internal server error"))
		return
	}
	pkgs, err := s.db.TokenPackages().List(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, sanitizeError(s.log, err, "internal server error"))
		return
	}
	names := make(map[string]string, len(pkgs))
	for _, p := range pkgs {
		names[p.ID] = p.Name
	}
	out := make([]map[string]any, 0, len(orders))
	for _, o := range orders {
		if o.Product != store.ProductToken {
			continue
		}
		out = append(out, map[string]any{
			"id": o.ID, "package_name": names[o.TokenPackageID], "amount_idr": o.AmountIDR,
			"status": string(o.Status), "created_at": o.CreatedAt, "paid_at": o.PaidAt,
		})
	}
	writeJSON(w, http.StatusOK, out)
}
