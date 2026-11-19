package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/identity"
	"github.com/mydisha/keirouter/backend/internal/store"
)

func callRotateKey(s *Server, id string) *httptest.ResponseRecorder {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", id)
	req := httptest.NewRequest(http.MethodPost, "/api/keys/"+id+"/rotate", nil)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	s.adminRotateKey(rec, req)
	return rec
}

func TestAdminRotateKeyKeepsIDAndBudget(t *testing.T) {
	srv := newPortalTestServer(t)
	ctx := context.Background()
	issued, err := srv.identity.Create(ctx, store.DefaultTenantID, "", "rotate-me")
	require.NoError(t, err)
	keyID := issued.Record.ID
	require.NoError(t, srv.budgets.Create(ctx, store.Budget{
		ID: "b-rotate", TenantID: adminTenant, ScopeKind: store.ScopeAPIKey, ScopeID: keyID,
		LimitMicros: 5_000_000, Period: "total", AlertPct: 80, HardCutoff: true,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}))
	// Warm the auth cache so the test proves rotation evicts it.
	_, err = srv.identity.Authenticate(ctx, issued.Plaintext)
	require.NoError(t, err)

	rec := callRotateKey(srv, keyID)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var out struct {
		ID      string `json:"id"`
		Key     string `json:"key"`
		Display string `json:"display"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.Equal(t, keyID, out.ID)
	require.NotEmpty(t, out.Key)
	require.NotEqual(t, issued.Plaintext, out.Key)

	_, err = srv.identity.Authenticate(ctx, issued.Plaintext)
	require.ErrorIs(t, err, identity.ErrUnauthorized, "old plaintext must stop working")
	got, err := srv.identity.Authenticate(ctx, out.Key)
	require.NoError(t, err)
	require.Equal(t, keyID, got.ID)

	budgets, err := srv.budgets.ListByScope(ctx, store.ScopeAPIKey, keyID)
	require.NoError(t, err)
	require.Len(t, budgets, 1)
	require.Equal(t, int64(5_000_000), budgets[0].LimitMicros)
}

func TestAdminRotateKeyReseals(t *testing.T) {
	srv := newPortalTestServer(t)
	ctx := context.Background()
	issued, err := srv.identity.Create(ctx, store.DefaultTenantID, "", "token-key")
	require.NoError(t, err)
	oldSealed, err := srv.vault.Sealer().SealString(issued.Plaintext)
	require.NoError(t, err)
	payg, err := srv.identity.Create(ctx, store.DefaultTenantID, "", "payg-key")
	require.NoError(t, err)
	require.NoError(t, srv.db.PortalUsers().Upsert(ctx, store.PortalUser{
		GoogleSub: "sub-tok", Email: "t@example.com", KeyID: payg.Record.ID,
	}))
	tx, err := srv.db.BeginTx(ctx, nil)
	require.NoError(t, err)
	now := time.Now()
	require.NoError(t, srv.db.TokenSubscriptions().CreateOnTx(ctx, tx, store.TokenSubscription{
		GoogleSub: "sub-tok", KeyID: issued.Record.ID, SealedKey: oldSealed,
		ExpiresAt: now.Add(24 * time.Hour), CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, tx.Commit())

	rec := callRotateKey(srv, issued.Record.ID)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var out struct {
		Key string `json:"key"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))

	sub, err := srv.db.TokenSubscriptions().GetByKey(ctx, issued.Record.ID)
	require.NoError(t, err)
	plain, err := srv.vault.Sealer().OpenString(sub.SealedKey)
	require.NoError(t, err)
	require.Equal(t, out.Key, plain, "token reveal must return the rotated key")
}

func TestAdminRotateKeyNotFound(t *testing.T) {
	srv := newPortalTestServer(t)
	rec := callRotateKey(srv, "missing")
	require.Equal(t, http.StatusNotFound, rec.Code)
}

// rotateHarnessKey rotates the e2e harness key in place and returns the new
// plaintext.
func rotateHarnessKey(t *testing.T, h *e2eHarness) string {
	t.Helper()
	ctx := context.Background()
	key, err := h.gateway.identity.Authenticate(ctx, h.apiKey)
	require.NoError(t, err)
	issued, err := h.gateway.rotateKeyMaterial(ctx, key)
	require.NoError(t, err)
	require.Equal(t, key.ID, issued.Record.ID)
	return issued.Plaintext
}

// Rotating a key must not reset or detach its balance: spend made with the old
// secret still counts, and a key that ran out stays out after rotation.
func TestE2E_RotateKeepsBudgetEnforced(t *testing.T) {
	var calls atomic.Int64
	h := newE2E(t, billedUpstream(&calls, 0, http.StatusOK), pricedGPT4o)
	scope := fundKey(t, h, 60_000) // $0.06: one $0.013 reply leaves $0.047

	short := `{"model":"openai/gpt-4o","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`
	resp := h.post(t, "/v1/chat/completions", short, h.apiKey)
	require.Equal(t, http.StatusOK, resp.StatusCode, drain(t, resp))

	newKey := rotateHarnessKey(t, h)

	resp = h.post(t, "/v1/chat/completions", short, h.apiKey)
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode, drain(t, resp))

	resp = h.post(t, "/v1/chat/completions", short, newKey)
	body := drain(t, resp)
	require.Equal(t, http.StatusPaymentRequired, resp.StatusCode, "rotated key must inherit the exhausted balance: %s", body)
	require.Equal(t, int64(1), calls.Load(), "no request after rotation may reach the upstream")

	spent, reserved := balance(t, h, scope)
	require.Equal(t, int64(replyCostMicros), spent)
	require.Zero(t, reserved)
}

// Spend made after rotation lands on the same balance as spend made before it.
func TestE2E_RotateSpendSharesBalance(t *testing.T) {
	var calls atomic.Int64
	h := newE2E(t, billedUpstream(&calls, 0, http.StatusOK), pricedGPT4o)
	scope := fundKey(t, h, 1_000_000)

	resp := h.post(t, "/v1/chat/completions", chatBody("hi", false), h.apiKey)
	require.Equal(t, http.StatusOK, resp.StatusCode, drain(t, resp))
	newKey := rotateHarnessKey(t, h)
	resp = h.post(t, "/v1/chat/completions", chatBody("hi", false), newKey)
	require.Equal(t, http.StatusOK, resp.StatusCode, drain(t, resp))

	spent, reserved := balance(t, h, scope)
	require.Equal(t, int64(2*replyCostMicros), spent)
	require.Zero(t, reserved)
}
