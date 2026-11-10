package pipeline

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/core"
	"github.com/mydisha/keirouter/backend/internal/store"
)

func imageRequest(keyID string) *core.ChatRequest {
	return &core.ChatRequest{
		Model: "gpt-4o",
		Messages: []core.Message{{Role: core.RoleUser, Content: []core.ContentPart{
			{Type: core.PartText, Text: "describe"},
			{Type: core.PartImage, Media: &core.MediaPayload{URL: "https://example.com/a.png"}},
		}}},
		PreviousResponseID: "resp_123",
		Metadata:           core.RequestMetadata{TenantID: store.DefaultTenantID, APIKeyID: keyID},
	}
}

// A token key's hold, and the prompt it is billed on a cut, use the token
// package estimate.
func TestTokenHoldUsesTokenPackageEstimate(t *testing.T) {
	p := New(Deps{Budget: tokenBudgetEngine(t, "key-tok", 1_000_000)})
	req := imageRequest("key-tok")
	require.NoError(t, p.reserveBudget(context.Background(), req, Options{TokenMetered: true}))

	want := core.EstimateTokenPackagePromptTokens(req)
	require.Greater(t, want, core.EstimatePromptTokens(req))
	require.EqualValues(t, want, req.Metadata.BudgetPromptTokens)

	unheld := imageRequest("key-tok")
	unheld.Metadata.TokenMetered = true
	require.Equal(t, want, tokenPromptEstimate(unheld))
}

// previous_response_id pulls server-side context the estimate cannot see.
func TestTokenRequestDropsPreviousResponseID(t *testing.T) {
	req := imageRequest("key-tok")
	require.NoError(t, limitTokenMeteredParams(req))
	require.Empty(t, req.PreviousResponseID)
}
