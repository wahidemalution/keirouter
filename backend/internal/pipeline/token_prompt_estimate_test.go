package pipeline

import (
	"context"
	"encoding/json"
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
		Metadata: core.RequestMetadata{TenantID: store.DefaultTenantID, APIKeyID: keyID},
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

// previous_response_id pulls server-side context the estimate cannot see:
// refused, not silently dropped.
func TestTokenRequestRefusesPreviousResponseID(t *testing.T) {
	req := imageRequest("key-tok")
	req.PreviousResponseID = "resp_123"
	err := limitTokenMeteredParams(req)
	pe := core.AsProviderError(err)
	require.NotNil(t, pe)
	require.Equal(t, core.ErrBadRequest, pe.Kind)
	require.Equal(t, CodePreviousResponseNotSupported, pe.Code)
}

// service_tier changes the upstream's rates and store keeps server-side
// state; neither is part of a token package.
func TestTokenRequestDropsServiceTierAndStore(t *testing.T) {
	req := imageRequest("key-tok")
	req.Extra = map[string]json.RawMessage{"service_tier": json.RawMessage(`"priority"`), "store": json.RawMessage(`true`), "seed": json.RawMessage(`1`)}
	req.Metadata.ServiceTier = "priority"
	require.NoError(t, limitTokenMeteredParams(req))
	require.NotContains(t, req.Extra, "service_tier")
	require.NotContains(t, req.Extra, "store")
	require.Contains(t, req.Extra, "seed")
	require.Empty(t, req.Metadata.ServiceTier)
}

// An admission timeout or client cancel cut the stream: the cap is charged
// even when a smaller output count was already reported.
func TestTokenAdmissionChargeCutStreamKeepsCap(t *testing.T) {
	for name, c := range map[string]struct {
		pe   *core.ProviderError
		want int
	}{
		"timeout":  {&core.ProviderError{Kind: core.ErrTimeout, Cause: errAdmissionTimeout}, 4000},
		"cancel":   {&core.ProviderError{Kind: core.ErrClientCanceled, Cause: context.Canceled}, 4000},
		"finished": {&core.ProviderError{Kind: core.ErrUpstream}, 100},
	} {
		t.Run(name, func(t *testing.T) {
			c.pe.AttemptUsage = &core.Usage{PromptTokens: 50, CompletionTokens: 100, TotalTokens: 150}
			got, ok := tokenAdmissionCharge(tokenReq(4000), c.pe)
			require.True(t, ok)
			require.Equal(t, c.want, got.CompletionTokens)
			require.Equal(t, 50, got.PromptTokens)
			require.Equal(t, got.PromptTokens+got.CompletionTokens, got.TotalTokens)
		})
	}
	big := &core.ProviderError{Kind: core.ErrTimeout, Cause: errAdmissionTimeout,
		AttemptUsage: &core.Usage{PromptTokens: 50, CompletionTokens: 5000}}
	got, _ := tokenAdmissionCharge(tokenReq(4000), big)
	require.Equal(t, 5000, got.CompletionTokens, "a larger reported count is kept")
}
