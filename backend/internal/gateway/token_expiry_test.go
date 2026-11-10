package gateway

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// tokenKeyPlaintext pays a token order and returns the issued key's plaintext.
func tokenKeyPlaintext(t *testing.T, s *Server, paygKey string) string {
	t.Helper()
	require.True(t, payTokenOrder(t, s, paygKey, "tx1", "tp-100m"))
	sub, err := s.db.TokenSubscriptions().GetBySub(t.Context(), tokenTestSub)
	require.NoError(t, err)
	plain, err := s.vault.Sealer().OpenString(sub.SealedKey)
	require.NoError(t, err)
	return plain
}

func callAuth(s *Server, token string) (*httptest.ResponseRecorder, bool) {
	called := false
	h := s.authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec, called
}

func TestExpiredTokenKeyRejected(t *testing.T) {
	s, paygKey := newTokenTestServer(t)
	plain := tokenKeyPlaintext(t, s, paygKey)
	setTokenExpiry(t, s, time.Now().Add(-time.Minute))

	rec, called := callAuth(s, plain)
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Contains(t, rec.Body.String(), "token_package_expired")
	require.Contains(t, rec.Body.String(), "Paket token kadaluarsa")
	require.False(t, called)
}

func TestActiveTokenKeyPasses(t *testing.T) {
	s, paygKey := newTokenTestServer(t)
	plain := tokenKeyPlaintext(t, s, paygKey)

	rec, called := callAuth(s, plain)
	require.Equal(t, http.StatusOK, rec.Code)
	require.True(t, called)
}

func TestPAYGKeyUnaffected(t *testing.T) {
	s, _ := newTokenTestServer(t)
	issued, err := s.identity.Create(t.Context(), "default", "", "plain")
	require.NoError(t, err)

	rec, called := callAuth(s, issued.Plaintext)
	require.Equal(t, http.StatusOK, rec.Code)
	require.True(t, called)
}

func TestExpiryAppliesDespiteAuthCache(t *testing.T) {
	s, paygKey := newTokenTestServer(t)
	plain := tokenKeyPlaintext(t, s, paygKey)

	rec, _ := callAuth(s, plain)
	require.Equal(t, http.StatusOK, rec.Code)

	setTokenExpiry(t, s, time.Now().Add(-time.Minute))
	sub, err := s.db.TokenSubscriptions().GetBySub(t.Context(), tokenTestSub)
	require.NoError(t, err)
	s.invalidateTokenSub(sub.KeyID)

	rec, called := callAuth(s, plain)
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.False(t, called)
}
