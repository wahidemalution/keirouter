package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/mydisha/keirouter/backend/internal/store"
	"github.com/stretchr/testify/require"
)

func TestAdminApprovePaymentOrder_CreditsOnce(t *testing.T) {
	s := newPaymentTestServer(t)
	key, err := s.identity.Create(t.Context(), store.DefaultTenantID, "", "payer")
	require.NoError(t, err)
	_ = makePaidOrder(t, s, key.Record.ID, "oa1")

	req := httptest.NewRequest(http.MethodPost, "/payments/orders/oa1/approve", strings.NewReader(`{"reason":"bank transfer"}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "oa1")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	s.adminApprovePaymentOrder(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	b, err := s.budgets.ListByScope(t.Context(), store.ScopeAPIKey, key.Record.ID)
	require.NoError(t, err)
	require.Len(t, b, 1)
	require.EqualValues(t, 1_000_000, b[0].LimitMicros)

	// second approval -> 409, no double credit
	req2 := httptest.NewRequest(http.MethodPost, "/payments/orders/oa1/approve", strings.NewReader(`{"reason":"bank transfer"}`))
	req2 = req2.WithContext(context.WithValue(req2.Context(), chi.RouteCtxKey, rctx))
	rec2 := httptest.NewRecorder()
	s.adminApprovePaymentOrder(rec2, req2)
	require.Equal(t, http.StatusConflict, rec2.Code)
	b2, _ := s.budgets.ListByScope(t.Context(), store.ScopeAPIKey, key.Record.ID)
	require.EqualValues(t, 1_000_000, b2[0].LimitMicros)
}

func TestAdminListPaymentOrders(t *testing.T) {
	s := newPaymentTestServer(t)
	key, err := s.identity.Create(t.Context(), store.DefaultTenantID, "", "payer")
	require.NoError(t, err)
	_ = makePaidOrder(t, s, key.Record.ID, "ol1")
	rec := httptest.NewRecorder()
	s.adminListPaymentOrders(rec, httptest.NewRequest(http.MethodGet, "/payments/orders", nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "ol1")
}
