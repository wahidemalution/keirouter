package pipeline

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/core"
	"github.com/mydisha/keirouter/backend/internal/limits"
)

// A key's own RPM, TPM or concurrency limit reaches the client as a KeiRouter
// rejection with the limit value, never as an upstream rate limit.
func TestRateLimitErrorNamesTheKeyLimit(t *testing.T) {
	cases := []struct {
		kind  string
		limit int64
		code  string
		want  string
	}{
		{"concurrency", 3, CodeConcurrencyLimitExceeded, "maksimal 3 permintaan berjalan bersamaan"},
		{"rpm", 60, CodeRPMLimitExceeded, "maksimal 60 permintaan per menit"},
		{"tpm", 100000, CodeTPMLimitExceeded, "maksimal 100000 token per menit"},
	}
	for _, c := range cases {
		t.Run(c.kind, func(t *testing.T) {
			pe := rateLimitError(limits.Decision{
				Reason:     "rate limit exceeded: key " + c.kind + " limit",
				RetryAfter: 2 * time.Second,
				LimitKind:  c.kind,
				Scope:      "key",
				Limit:      c.limit,
			})
			require.Equal(t, core.ErrRateLimit, pe.Kind)
			require.Equal(t, c.code, pe.Code)
			require.Contains(t, pe.Message, c.want)
			require.Contains(t, pe.Message, "bukan dari provider")
			require.Equal(t, 2*time.Second, pe.RetryAfter)
			require.True(t, IsKeyLimitCode(pe.Code))
		})
	}
}

// An unknown limit kind still carries a code so the client never sees the
// upstream wording.
func TestRateLimitErrorUnknownKind(t *testing.T) {
	pe := rateLimitError(limits.Decision{LimitKind: "other", Scope: "key", Limit: 5})
	require.Equal(t, CodeKeyRateLimitExceeded, pe.Code)
	require.NotEmpty(t, pe.Message)
	require.False(t, IsKeyLimitCode(CodeNNotSupported))
}
