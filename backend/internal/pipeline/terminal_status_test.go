package pipeline

import (
	"context"
	"testing"

	"github.com/mydisha/keirouter/backend/internal/core"
	"github.com/stretchr/testify/require"
)

// A key without enough credit is the client's own limit, not a router or
// provider fault, so it must stay out of the failure count behind success rate.
func TestTerminalStatusAndKind(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantStatus string
		wantKind   string
	}{
		{"budget blocked", &core.ProviderError{Kind: core.ErrBudgetBlocked}, "blocked", "budget_blocked"},
		{"policy blocked", &core.ProviderError{Kind: core.ErrPolicyBlocked}, "blocked", "policy_blocked"},
		{"client canceled", &core.ProviderError{Kind: core.ErrClientCanceled}, "cancelled", "client_canceled"},
		{"context canceled", context.Canceled, "cancelled", "cancelled"},
		{"upstream", &core.ProviderError{Kind: core.ErrUpstream}, "failed", "upstream"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, kind := terminalStatusAndKind(tc.err)
			require.Equal(t, tc.wantStatus, status)
			require.Equal(t, tc.wantKind, kind)
		})
	}
}
