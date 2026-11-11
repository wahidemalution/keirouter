package gateway

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/market"
	"github.com/mydisha/keirouter/backend/internal/store"
)

func TestChainStepDisplayNameValidation(t *testing.T) {
	st, msg := chainStepInput{Provider: "p", Model: "m", DisplayName: "  Fast Lane  "}.toStore("c", 0, time.Now())
	require.Empty(t, msg)
	require.Equal(t, "Fast Lane", st.DisplayName)

	_, msg = chainStepInput{Provider: "p", Model: "m", DisplayName: strings.Repeat("a", maxStepDisplayName+1)}.toStore("c", 0, time.Now())
	require.Contains(t, msg, "display_name")
}

func TestRequestLogProviderNamePrefersStepDisplayName(t *testing.T) {
	cache := market.NewSnapshotCache()
	cache.MergeProviderLabels(map[string]string{"deepseek": "DeepSeek"})
	s := &Server{marketCache: cache}
	info := requestLogInfo{s: s, names: map[string]string{}, official: map[string]*officialPrice{},
		chains: map[string]store.Chain{"c1": {ID: "c1", Steps: []store.ChainStep{
			{Provider: "custom-openai-surplus-deepseek", MarketSlug: "surplus-trusted@deepseek:m", DisplayName: "Jalur Cepat"},
		}}}}
	require.Equal(t, "Jalur Cepat", info.providerName(store.RecentRecord{ChainID: "c1", Provider: "custom-openai-surplus-deepseek"}))
}

func TestBuildModelSettingsStepDisplayName(t *testing.T) {
	f := newModelSettingsFixture(t, store.Chain{
		ID: "dn1", Name: "named", Strategy: "priority",
		Steps: []store.ChainStep{
			{ID: "dn1a", Position: 0, Provider: "custom-openai-sharellm", Model: "m", StaticInputPerM: 0.04, StaticOutputPerM: 0.2, DisplayName: "Jalur Hemat"},
			{ID: "dn1b", Position: 1, Provider: "custom-openai-other", Model: "m", StaticInputPerM: 0.04, StaticOutputPerM: 0.2},
		},
	})
	ctx := context.Background()
	chains, err := f.srv.chains.ListByTenant(ctx, store.DefaultTenantID)
	require.NoError(t, err)
	require.Equal(t, "Jalur Hemat", chains[0].Steps[0].DisplayName, "stored and read back")
	key, err := f.srv.identity.Get(ctx, f.keyID)
	require.NoError(t, err)
	resp, err := f.srv.buildModelSettings(ctx, key, chains)
	require.NoError(t, err)
	require.Equal(t, "Jalur Hemat", resp.Models[0].Providers[0].Name)
	require.NotEqual(t, "Jalur Hemat", resp.Models[0].Providers[1].Name, "a step without a name keeps the default")
}
