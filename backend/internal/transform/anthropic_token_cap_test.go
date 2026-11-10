package transform

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/core"
)

type renderedThinking struct {
	MaxTokens int `json:"max_tokens"`
	Thinking  *struct {
		Type         string `json:"type"`
		BudgetTokens int    `json:"budget_tokens"`
	} `json:"thinking"`
}

func renderAnt(t *testing.T, maxTokens int, reasoning *core.ReasoningConfig, tokenMetered bool) renderedThinking {
	t.Helper()
	req := &core.ChatRequest{
		Model:     "claude-3-7-sonnet-20250219",
		MaxTokens: &maxTokens,
		Reasoning: reasoning,
		Messages:  []core.Message{{Role: core.RoleUser, Content: []core.ContentPart{{Type: core.PartText, Text: "hi"}}}},
		Metadata:  core.RequestMetadata{TokenMetered: tokenMetered},
	}
	raw, err := AnthropicCodec{}.RenderRequest(req)
	require.NoError(t, err)
	var out renderedThinking
	require.NoError(t, json.Unmarshal(raw, &out))
	return out
}

// A token package's output cap is a hard limit: the thinking budget is fitted
// under it, or dropped when it cannot fit, never max_tokens raised above it.
func TestAnthropicTokenCapNeverRaised(t *testing.T) {
	cases := []struct {
		name       string
		max        int
		reasoning  *core.ReasoningConfig
		paygMax    int
		tokenThink int // 0: thinking dropped
	}{
		{"effort_budget_above_cap", 500, &core.ReasoningConfig{Effort: "high"}, 4096 + 1024, 0},
		{"budget_equal_to_cap", 8000, &core.ReasoningConfig{MaxTokens: 8000}, 9024, 8000 - 1024},
		{"min_budget_above_cap", 500, &core.ReasoningConfig{Effort: "enabled"}, 1024 + antThinkingAnswerReserve, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			payg := renderAnt(t, c.max, c.reasoning, false)
			require.Equal(t, c.paygMax, payg.MaxTokens, "PAYG unchanged")

			tok := renderAnt(t, c.max, c.reasoning, true)
			require.Equal(t, c.max, tok.MaxTokens)
			if c.tokenThink == 0 {
				require.Nil(t, tok.Thinking)
				return
			}
			require.NotNil(t, tok.Thinking)
			require.Equal(t, c.tokenThink, tok.Thinking.BudgetTokens)
			require.Less(t, tok.Thinking.BudgetTokens, tok.MaxTokens)
		})
	}
}
