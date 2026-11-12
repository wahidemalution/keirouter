package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/tokenpkg"
)

func adminTokenCall(s *Server, method, path, body string, params map[string]string, h http.HandlerFunc) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	rctx := chi.NewRouteContext()
	for k, v := range params {
		rctx.URLParams.Add(k, v)
	}
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

func TestAdminTokenPackageValidation(t *testing.T) {
	s, _ := newTokenTestServer(t)
	for name, body := range map[string]string{
		"tokens zero":   `{"name":"x","kind":"tokens","tokens":0,"days":15,"price_idr":1000}`,
		"days zero":     `{"name":"x","kind":"tokens","tokens":5,"days":0,"price_idr":1000}`,
		"price zero":    `{"name":"x","kind":"tokens","tokens":5,"days":15,"price_idr":0}`,
		"unknown kind":  `{"name":"x","kind":"bogus","tokens":5,"days":15,"price_idr":1000}`,
		"missing name":  `{"name":" ","kind":"tokens","tokens":5,"days":15,"price_idr":1000}`,
		"days no days":  `{"name":"x","kind":"days","days":0,"price_idr":1000}`,
		"days no price": `{"name":"x","kind":"days","days":5,"price_idr":-1}`,
	} {
		rec := adminTokenCall(s, http.MethodPost, "/token-packages", body, nil, s.adminCreateTokenPackage)
		require.Equal(t, http.StatusBadRequest, rec.Code, name+": "+rec.Body.String())
	}

	rec := adminTokenCall(s, http.MethodPost, "/token-packages",
		`{"name":"Extra","kind":"days","tokens":999,"days":7,"price_idr":9000}`, nil, s.adminCreateTokenPackage)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var out map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	got, err := s.db.TokenPackages().Get(t.Context(), out["id"].(string))
	require.NoError(t, err)
	require.Zero(t, got.Tokens)
}

func TestAdminTokenPackageCRUD(t *testing.T) {
	s, _ := newTokenTestServer(t)
	rec := adminTokenCall(s, http.MethodPost, "/token-packages",
		`{"name":"Mini","kind":"tokens","tokens":5000000,"days":10,"price_idr":50000,"enabled":true,"sort":9}`,
		nil, s.adminCreateTokenPackage)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var created map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created))
	id := created["id"].(string)
	require.Equal(t, true, created["enabled"])

	rec = adminTokenCall(s, http.MethodPut, "/token-packages/"+id,
		`{"name":"Mini 2","kind":"tokens","tokens":6000000,"days":10,"price_idr":55000,"featured":true,"sort":9}`,
		map[string]string{"id": id}, s.adminUpdateTokenPackage)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	got, err := s.db.TokenPackages().Get(t.Context(), id)
	require.NoError(t, err)
	require.Equal(t, "Mini 2", got.Name)
	require.EqualValues(t, 6000000, got.Tokens)
	require.True(t, got.Featured)
	require.True(t, got.Enabled, "omitted enabled keeps its value")

	rec = adminTokenCall(s, http.MethodGet, "/token-packages", "", nil, s.adminListTokenPackages)
	require.Equal(t, http.StatusOK, rec.Code)
	var list []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	var found bool
	for _, p := range list {
		found = found || p["id"] == id
	}
	require.True(t, found)

	rec = adminTokenCall(s, http.MethodPut, "/token-packages/nope", `{"name":"x","kind":"days","days":1,"price_idr":1}`,
		map[string]string{"id": "nope"}, s.adminUpdateTokenPackage)
	require.Equal(t, http.StatusNotFound, rec.Code)

	rec = adminTokenCall(s, http.MethodDelete, "/token-packages/"+id, "", map[string]string{"id": id}, s.adminDeleteTokenPackage)
	require.Equal(t, http.StatusNoContent, rec.Code)
	rec = adminTokenCall(s, http.MethodDelete, "/token-packages/"+id, "", map[string]string{"id": id}, s.adminDeleteTokenPackage)
	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestAdminTokenSettingsRoundTrip(t *testing.T) {
	s, _ := newTokenTestServer(t)
	rec := adminTokenCall(s, http.MethodPut, "/token-packages/settings",
		`{"enabled":true,"allowed_models":["m-a"," m-b ","m-a"],"rpm_limit":30,"concurrency_limit":2}`,
		nil, s.adminPutTokenSettings)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	rec = adminTokenCall(s, http.MethodGet, "/token-packages/settings", "", nil, s.adminGetTokenSettings)
	var out map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.Equal(t, true, out["enabled"])
	require.Equal(t, []any{"m-a", "m-b"}, out["allowed_models"])
	require.EqualValues(t, 30, out["rpm_limit"])
	require.EqualValues(t, 2, out["concurrency_limit"])

	plan, err := s.db.Plans().Get(t.Context(), tokenpkg.DefaultPlanID)
	require.NoError(t, err)
	require.EqualValues(t, 30, plan.RPMLimit)
	require.EqualValues(t, 2, plan.ConcurrencyLimit)
	require.Equal(t, "m-a,m-b", plan.AllowedModels)

	for _, body := range []string{`{"allowed_models":[]}`, `{"allowed_models":["  "]}`, `{"rpm_limit":-1}`, `{"concurrency_limit":-1}`} {
		rec = adminTokenCall(s, http.MethodPut, "/token-packages/settings", body, nil, s.adminPutTokenSettings)
		require.Equal(t, http.StatusBadRequest, rec.Code, body)
	}
	plan, err = s.db.Plans().Get(t.Context(), tokenpkg.DefaultPlanID)
	require.NoError(t, err)
	require.EqualValues(t, 30, plan.RPMLimit, "rejected request leaves the plan alone")
}

func TestAdminTokenSubscriptionsList(t *testing.T) {
	s, paygKey := newTokenTestServer(t)
	require.True(t, payTokenOrder(t, s, paygKey, "ts1", "tp-100m"))
	sub, err := s.db.TokenSubscriptions().GetBySub(t.Context(), tokenTestSub)
	require.NoError(t, err)
	recordTokens(t, s, "u1", sub.KeyID, 0, 1234)

	rec := adminTokenCall(s, http.MethodGet, "/token-packages/subscriptions", "", nil, s.adminListTokenSubscriptions)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var list []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	require.Len(t, list, 1)
	row := list[0]
	require.Equal(t, "u@example.com", row["email"])
	require.Equal(t, tokenTestSub, row["google_sub"])
	require.Equal(t, sub.KeyID, row["key_id"])
	require.EqualValues(t, 1234, row["used_tokens"])
	require.EqualValues(t, 100_000_000, row["limit_tokens"])
	require.Equal(t, true, row["active"])
	require.Equal(t, false, row["disabled"])
}

func TestAdminTokenOverview(t *testing.T) {
	s, paygKey := newTokenTestServer(t)
	require.True(t, payTokenOrder(t, s, paygKey, "ov1", "tp-100m"))
	sub, err := s.db.TokenSubscriptions().GetBySub(t.Context(), tokenTestSub)
	require.NoError(t, err)
	recordTokens(t, s, "ov-u1", sub.KeyID, 0, 1234)
	recordTokens(t, s, "ov-u2", sub.KeyID, 0, 766)
	recordTokens(t, s, "ov-payg", paygKey, 0, 99999) // a PAYG key is not counted

	rec := adminTokenCall(s, http.MethodGet, "/token-packages/overview", "", nil, s.adminTokenOverview)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var body struct {
		SubscribersTotal  int64 `json:"subscribers_total"`
		SubscribersActive int64 `json:"subscribers_active"`
		UnusedTokens      int64 `json:"unused_tokens"`
		UsedTokensTotal   int64 `json:"used_tokens_total"`
		UsedTokensRange   int64 `json:"used_tokens_range"`
		RangeDays         int   `json:"range_days"`
		Daily             []struct {
			Day    string `json:"day"`
			Tokens int64  `json:"tokens"`
		} `json:"daily"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.EqualValues(t, 1, body.SubscribersTotal)
	require.EqualValues(t, 1, body.SubscribersActive)
	require.EqualValues(t, 100_000_000-2000, body.UnusedTokens)
	require.EqualValues(t, 2000, body.UsedTokensTotal)
	require.EqualValues(t, 2000, body.UsedTokensRange)
	require.Len(t, body.Daily, body.RangeDays)
	last := body.Daily[len(body.Daily)-1]
	require.Equal(t, time.Now().UTC().Format("2006-01-02"), last.Day)
	require.EqualValues(t, 2000, last.Tokens)
}

func TestAdminTokenKeyToggle(t *testing.T) {
	s, paygKey := newTokenTestServer(t)
	plain := tokenKeyPlaintext(t, s, paygKey)
	rec, called := callAuth(s, plain)
	require.Equal(t, http.StatusOK, rec.Code)
	require.True(t, called)

	rec = adminTokenCall(s, http.MethodPost, "/x", "", map[string]string{"sub": tokenTestSub}, s.adminToggleTokenKey)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.JSONEq(t, `{"disabled":true}`, rec.Body.String())
	rec, called = callAuth(s, plain)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.False(t, called)

	rec = adminTokenCall(s, http.MethodPost, "/x", "", map[string]string{"sub": tokenTestSub}, s.adminToggleTokenKey)
	require.JSONEq(t, `{"disabled":false}`, rec.Body.String())
	rec, _ = callAuth(s, plain)
	require.Equal(t, http.StatusOK, rec.Code)

	rec = adminTokenCall(s, http.MethodPost, "/x", "", map[string]string{"sub": "nobody"}, s.adminToggleTokenKey)
	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestAdminDeleteTokenPlanRefused(t *testing.T) {
	s, _ := newTokenTestServer(t)
	rec := adminTokenCall(s, http.MethodDelete, "/plans/x", "", map[string]string{"id": tokenpkg.DefaultPlanID}, s.adminDeletePlan)
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	_, err := s.db.Plans().Get(t.Context(), tokenpkg.DefaultPlanID)
	require.NoError(t, err)
}

func TestAdminPortalPlanRejectsTokenPlan(t *testing.T) {
	s, _ := newTokenTestServer(t)
	s.settings = s.db.Settings()
	rec := adminTokenCall(s, http.MethodPatch, "/portal-users/x/plan", `{"plan_id":"`+tokenpkg.DefaultPlanID+`"}`,
		map[string]string{"sub": tokenTestSub}, s.adminSetPortalUserPlan)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	u, err := s.db.PortalUsers().GetBySub(t.Context(), tokenTestSub)
	require.NoError(t, err)
	require.Empty(t, u.PlanID)

	rec = adminTokenCall(s, http.MethodPost, "/portal-settings", `{"default_plan_id":"`+tokenpkg.DefaultPlanID+`"}`,
		nil, s.adminUpdatePortalSettings)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}

func TestAdminKeyCreateRejectsTokenPlan(t *testing.T) {
	s, _ := newTokenTestServer(t)
	rec := adminTokenCall(s, http.MethodPost, "/keys", `{"name":"k","plan_id":"`+tokenpkg.DefaultPlanID+`"}`,
		nil, s.adminCreateKey)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}
