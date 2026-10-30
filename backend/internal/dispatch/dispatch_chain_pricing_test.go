package dispatch

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/store"
)

func TestTargetsFromChainSpreadsChainRateToEveryStep(t *testing.T) {
	chain := store.Chain{
		InputPerM: 2.5, OutputPerM: 10, CacheWritePerM: 3.125, CacheReadPerM: 0.25,
		Steps: []store.ChainStep{
			{Provider: "openai", Model: "gpt-4o"},
			{Provider: "anthropic", Model: "claude-3-5-sonnet"},
		},
	}
	targets := TargetsFromChain(chain)
	require.Len(t, targets, 2)
	for i, tgt := range targets {
		require.Equal(t, 2.5, tgt.InputPerM, "target %d input", i)
		require.Equal(t, 10.0, tgt.OutputPerM, "target %d output", i)
		require.Equal(t, 3.125, tgt.CacheWritePerM, "target %d cache write", i)
		require.Equal(t, 0.25, tgt.CacheReadPerM, "target %d cache read", i)
	}
	require.Equal(t, "openai", targets[0].Provider)
	require.Equal(t, "anthropic", targets[1].Provider)
}

func TestTargetsFromChainCopiesStaticStepPrice(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	chain := store.Chain{Steps: []store.ChainStep{
		{Provider: "openai", Model: "gpt-4o", MarketSlug: "surplus-fast:gpt-4o"},
		{Provider: "openai", Model: "gpt-4o", StaticInputPerM: 0.5, StaticOutputPerM: 2,
			StaticCacheReadPerM: f(0.04), MarkupPercent: f(10)},
	}}
	targets := TargetsFromChain(chain)
	require.Len(t, targets, 2)
	st := targets[1]
	require.Equal(t, 0.5, st.StaticInputPerM)
	require.Equal(t, 2.0, st.StaticOutputPerM)
	require.True(t, st.StaticCacheReadSet)
	require.Equal(t, 0.04, st.StaticCacheReadPerM)
	require.False(t, st.StaticCacheWriteSet)
	require.True(t, st.StepMarkupSet)
	require.Equal(t, 10.0, st.StepMarkupPercent)
	require.False(t, st.StaticPrice, "StaticPrice is set by the gateway once rates are stamped")
	require.False(t, targets[0].StepMarkupSet)
	require.Zero(t, targets[0].StaticInputPerM)
}
