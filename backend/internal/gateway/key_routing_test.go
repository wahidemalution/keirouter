package gateway

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/dispatch"
	"github.com/mydisha/keirouter/backend/internal/market"
	"github.com/mydisha/keirouter/backend/internal/store"
)

const (
	routingSlugA = "surplus-trusted@openrouter:m"
	routingSlugB = "surplus-trusted@alibaba-cn:m"
)

// routingChain is chain m: A and B are Surplus provider steps, C has a static
// price, and fb/model is the chain fallback.
func routingChain() store.Chain {
	return store.Chain{
		ID: "c-m", Name: "m", Strategy: "priority",
		InputPerM: 5, OutputPerM: 10,
		Steps: []store.ChainStep{
			{Position: 0, Provider: "pA", Model: "m", MarketSlug: routingSlugA},
			{Position: 1, Provider: "pB", Model: "m", MarketSlug: routingSlugB},
			{Position: 2, Provider: "pC", Model: "m", StaticInputPerM: 1, StaticOutputPerM: 2},
		},
		FallbackProvider: "fb", FallbackModel: "model",
	}
}

func routingCache() *market.SnapshotCache {
	cache := market.NewSnapshotCache()
	q := market.TrustedQuote{Found: true, BestIn: 1, BestOut: 2, CapIn: 3, CapOut: 4, MinDiscountPct: 10, Providers: []string{"x"}, BestDiscountPct: 35}
	cache.Replace([]market.Model{q.ProviderModel("openrouter", "m"), q.ProviderModel("alibaba-cn", "m")})
	return cache
}

func withKey(key store.APIKey) context.Context {
	return context.WithValue(context.Background(), apiKeyCtxKey, key)
}

func resolveRouting(ctx context.Context, c store.Chain, cache *market.SnapshotCache) (resolveResult, error) {
	return resolveTargetsWith(ctx, &fakeChains{chains: []store.Chain{c}}, &fakeAliases{}, nil, cache, 0, "t1", "chain:"+c.Name)
}

func providersOf(ts []dispatch.Target) []string {
	out := make([]string, 0, len(ts))
	for _, t := range ts {
		out = append(out, t.Provider)
	}
	return out
}

func requireBadModel(t *testing.T, err error, status int, msg string) {
	t.Helper()
	var bad badModelError
	require.ErrorAs(t, err, &bad)
	require.Equal(t, status, bad.HTTPStatus())
	require.Equal(t, msg, bad.Error())
}

func TestKeyRoutingNoKeyKeepsToday(t *testing.T) {
	res, err := resolveRouting(context.Background(), routingChain(), routingCache())
	require.NoError(t, err)
	require.Equal(t, []string{"pA", "pB", "pC", "fb"}, providersOf(res.Targets))
	require.True(t, res.Targets[3].ChainFallback)
	require.Equal(t, 1, res.PlanOpts.PinnedTail)
}

func TestKeyRoutingNoRowsNoFallbackChain(t *testing.T) {
	c := routingChain()
	c.FallbackProvider, c.FallbackModel = "", ""
	res, err := resolveRouting(withKey(store.APIKey{ID: "k"}), c, routingCache())
	require.NoError(t, err)
	require.Equal(t, []string{"pA", "pB", "pC"}, providersOf(res.Targets))
	require.Equal(t, 0, res.PlanOpts.PinnedTail)
}

func TestKeyRoutingDisabledProvider(t *testing.T) {
	key := store.APIKey{ID: "k", ModelDisabledProviders: map[string][]string{"m": {"pB"}}}
	res, err := resolveRouting(withKey(key), routingChain(), routingCache())
	require.NoError(t, err)
	require.Equal(t, []string{"pA", "pC"}, providersOf(res.Targets), "fallback off: not appended")

	key.ModelFinalFallback = map[string]int{"m": store.FinalFallbackChosen}
	res, err = resolveRouting(withKey(key), routingChain(), routingCache())
	require.NoError(t, err)
	require.Equal(t, []string{"pA", "pC", "fb"}, providersOf(res.Targets))
}

func TestKeyRoutingAdminCeilingEmptyIntersection(t *testing.T) {
	key := store.APIKey{ID: "k", AllowedProviders: []string{"pZ"}}
	_, err := resolveRouting(withKey(key), routingChain(), routingCache())
	requireBadModel(t, err, http.StatusUnprocessableEntity,
		"model m has no provider this key is allowed to use. Contact support to change the providers on your key")
}

func TestKeyRoutingCustomerDisabledAll(t *testing.T) {
	key := store.APIKey{ID: "k", ModelDisabledProviders: map[string][]string{"m": {"pA", "pB", "pC"}}}
	_, err := resolveRouting(withKey(key), routingChain(), routingCache())
	requireBadModel(t, err, http.StatusUnprocessableEntity,
		"model m has no provider turned on for this key. Turn one on in Model Settings, or turn on the final fallback")

	// A bare model name reports the same error instead of "unknown model".
	_, err = resolveTargetsWith(withKey(key), &fakeChains{chains: []store.Chain{routingChain()}}, &fakeAliases{}, nil, routingCache(), 0, "t1", "m")
	requireBadModel(t, err, http.StatusUnprocessableEntity,
		"model m has no provider turned on for this key. Turn one on in Model Settings, or turn on the final fallback")
}

// Mixed causes (one step outside the ceiling, the rest turned off by the
// customer) get the customer copy: the customer can fix part of it.
func TestKeyRoutingMixedCausesUseCustomerCopy(t *testing.T) {
	key := store.APIKey{ID: "k", AllowedProviders: []string{"pA", "pB"}, ModelDisabledProviders: map[string][]string{"m": {"pA", "pB"}}}
	_, err := resolveRouting(withKey(key), routingChain(), routingCache())
	requireBadModel(t, err, http.StatusUnprocessableEntity,
		"model m has no provider turned on for this key. Turn one on in Model Settings, or turn on the final fallback")
}

// With every step turned off and the final fallback on, the fallback alone
// serves: the error copy names the fallback as a way out, and the admin
// ceiling covers steps only.
func TestKeyRoutingAllStepsOffFallbackOnServesFallback(t *testing.T) {
	key := store.APIKey{ID: "k", AllowedProviders: []string{"pZ"}, ModelFinalFallback: map[string]int{"m": store.FinalFallbackChosen}}
	res, err := resolveRouting(withKey(key), routingChain(), routingCache())
	require.NoError(t, err)
	require.Equal(t, []string{"fb"}, providersOf(res.Targets))
	require.True(t, res.Targets[0].ChainFallback)
	require.Equal(t, 1, res.PlanOpts.PinnedTail)
}

func TestKeyRoutingFallbackToggle(t *testing.T) {
	key := store.APIKey{ID: "k", ModelFinalFallback: map[string]int{"m": store.FinalFallbackOff}}
	res, err := resolveRouting(withKey(key), routingChain(), routingCache())
	require.NoError(t, err)
	require.Equal(t, []string{"pA", "pB", "pC"}, providersOf(res.Targets))
	require.Equal(t, 0, res.PlanOpts.PinnedTail)
	for _, tg := range res.Targets {
		require.False(t, tg.ChainFallback)
	}

	// The toggle is keyed by the normalised model name.
	key.ModelFinalFallback = map[string]int{"m": store.FinalFallbackChosen}
	res, err = resolveTargetsWith(withKey(key), &fakeChains{chains: []store.Chain{routingChain()}}, &fakeAliases{}, nil, routingCache(), 0, "t1", "chain:m")
	require.NoError(t, err)
	last := res.Targets[len(res.Targets)-1]
	require.True(t, last.ChainFallback)
	require.Equal(t, "fb", last.Provider)
	require.Zero(t, last.InputPerM, "the chain price never bills the fallback")
	require.Equal(t, 1, res.PlanOpts.PinnedTail)
}

func TestKeyRoutingRoundRobinWithoutFallback(t *testing.T) {
	c := routingChain()
	c.Strategy = "round_robin"
	res, err := resolveRouting(withKey(store.APIKey{ID: "k"}), c, routingCache())
	require.NoError(t, err)
	require.Equal(t, dispatch.StrategyRoundRobin, res.PlanOpts.Strategy)
	require.Equal(t, []string{"pA", "pB", "pC"}, providersOf(res.Targets))
	require.Equal(t, 0, res.PlanOpts.PinnedTail, "no fallback: every step rotates")
}

func TestKeyRoutingSkipList(t *testing.T) {
	cache := routingCache()
	cache.Skips().Add(routingSlugA)
	res, err := resolveRouting(withKey(store.APIKey{ID: "k"}), routingChain(), cache)
	require.NoError(t, err)
	require.Equal(t, []string{"pB", "pC"}, providersOf(res.Targets))

	// The skip list is about stock, not the key: unauthenticated routing skips too.
	res, err = resolveRouting(context.Background(), routingChain(), cache)
	require.NoError(t, err)
	require.Equal(t, []string{"pB", "pC", "fb"}, providersOf(res.Targets))
}

// A step stored with a mixed-case provider is matched against the skip list
// in canonical form, the form the pipeline adds after a failed attempt.
func TestKeyRoutingSkipListMatchesCanonicalSlug(t *testing.T) {
	c := routingChain()
	c.Steps[0].MarketSlug = "surplus-trusted@OpenRouter:m"
	cache := routingCache()
	cache.Skips().Add(routingSlugA)
	res, err := resolveRouting(withKey(store.APIKey{ID: "k"}), c, cache)
	require.NoError(t, err)
	require.Equal(t, []string{"pB", "pC"}, providersOf(res.Targets))
}

// The fallback target records whether the key turned it on explicitly, which
// decides whether a minimum discount may remove it.
func TestKeyRoutingMarksFallbackExemption(t *testing.T) {
	for _, tc := range []struct {
		name   string
		ctx    context.Context
		exempt bool
	}{
		{"no key", context.Background(), false},
		{"kept from before", withKey(store.APIKey{ID: "k", ModelFinalFallback: map[string]int{"m": store.FinalFallbackKept}}), false},
		{"chosen", withKey(store.APIKey{ID: "k", ModelFinalFallback: map[string]int{"m": store.FinalFallbackChosen}}), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, err := resolveRouting(tc.ctx, routingChain(), routingCache())
			require.NoError(t, err)
			last := res.Targets[len(res.Targets)-1]
			require.True(t, last.ChainFallback)
			require.Equal(t, tc.exempt, last.FallbackFloorExempt)
			require.Equal(t, 1, res.PlanOpts.PinnedTail)
		})
	}
}

// The guardrails judge resolves its own chain: the triggering key's ceiling
// and missing fallback row must not strip it.
func TestJudgeIgnoresKeyRouting(t *testing.T) {
	db, gw := newPublicTestGatewayWithDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	require.NoError(t, db.Chains().Create(ctx, store.Chain{
		ID: "judge", TenantID: store.DefaultTenantID, Name: "judge", Strategy: "priority",
		FallbackProvider: "openai", FallbackModel: "gpt-4o-mini",
		Steps: []store.ChainStep{
			{ID: "j1", Provider: "openai", Model: "gpt-4o", Position: 0},
			{ID: "j2", Provider: "anthropic", Model: "claude-3.5", Position: 1},
		},
		CreatedAt: now, UpdatedAt: now,
	}))
	keyed := context.WithValue(ctx, apiKeyCtxKey, store.APIKey{ID: "k", AllowedProviders: []string{"pZ"}})

	_, err := gw.resolveTargets(keyed, store.DefaultTenantID, "judge")
	require.Error(t, err, "the key itself may not use this chain")

	res, err := gw.resolveJudgeTargets(keyed, store.DefaultTenantID, "judge")
	require.NoError(t, err)
	require.Equal(t, []string{"openai", "anthropic", "openai"}, providersOf(res.Targets))
	require.True(t, res.Targets[2].ChainFallback)
	require.Equal(t, 1, res.PlanOpts.PinnedTail)
}

func TestKeyRoutingSkipListCoversPlainAndFastSlugs(t *testing.T) {
	cache := market.NewSnapshotCache()
	q := market.TrustedQuote{Found: true, BestIn: 1, BestOut: 2, CapIn: 3, CapOut: 4, Providers: []string{"x"}, ProviderOrder: []string{"x"}}
	cache.Replace([]market.Model{q.Model("m"), q.FastModel("m")})
	cache.Skips().Add("surplus-trusted:m")
	cache.Skips().Add("surplus-fast:m")
	c := store.Chain{ID: "c", Name: "m", Steps: []store.ChainStep{
		{Position: 0, Provider: "pA", Model: "m", MarketSlug: "surplus-trusted:m"},
		{Position: 1, Provider: "pB", Model: "m", MarketSlug: "surplus-fast:m"},
		{Position: 2, Provider: "pC", Model: "m", StaticInputPerM: 1, StaticOutputPerM: 2},
	}}
	res, err := resolveRouting(context.Background(), c, cache)
	require.NoError(t, err)
	require.Equal(t, []string{"pC"}, providersOf(res.Targets))
}

func TestKeyRoutingSoldOutFallbackOff(t *testing.T) {
	c := routingChain()
	c.Steps = c.Steps[:2] // market steps only
	empty := market.NewSnapshotCache()

	_, err := resolveRouting(withKey(store.APIKey{ID: "k"}), c, empty)
	requireBadModel(t, err, http.StatusServiceUnavailable,
		"model m is sold out: no offer currently meets its price floor, try again later. Turn on the final fallback in Model Settings to avoid this error")

	// Bare name: still the sold-out answer, not "unknown model".
	_, err = resolveTargetsWith(withKey(store.APIKey{ID: "k"}), &fakeChains{chains: []store.Chain{c}}, &fakeAliases{}, nil, empty, 0, "t1", "m")
	requireBadModel(t, err, http.StatusServiceUnavailable,
		"model m is sold out: no offer currently meets its price floor, try again later. Turn on the final fallback in Model Settings to avoid this error")

	// Fallback on: the fallback serves.
	res, err := resolveRouting(withKey(store.APIKey{ID: "k", ModelFinalFallback: map[string]int{"m": store.FinalFallbackChosen}}), c, empty)
	require.NoError(t, err)
	require.Equal(t, []string{"fb"}, providersOf(res.Targets))

	// A chain without a fallback keeps the plain sold-out copy.
	c.FallbackProvider, c.FallbackModel = "", ""
	_, err = resolveRouting(withKey(store.APIKey{ID: "k"}), c, empty)
	requireBadModel(t, err, http.StatusServiceUnavailable,
		"model m is sold out: no offer currently meets its price floor, try again later")
}

func TestKeyRoutingStampsBestDiscountPct(t *testing.T) {
	res, err := resolveRouting(context.Background(), routingChain(), routingCache())
	require.NoError(t, err)
	require.Equal(t, 35, res.Targets[0].BestDiscountPct)
	require.Equal(t, 35, res.Targets[1].BestDiscountPct)
	require.Equal(t, 0, res.Targets[2].BestDiscountPct, "static step has no market quote")
}

// A stored provider slug with a mixed-case provider still finds its quote,
// and the target carries the canonical slug.
func TestKeyRoutingCanonicalisesProviderSlug(t *testing.T) {
	c := routingChain()
	c.Steps[0].MarketSlug = "surplus-trusted@OpenRouter:m"
	res, err := resolveRouting(context.Background(), c, routingCache())
	require.NoError(t, err)
	require.Equal(t, []string{"pA", "pB", "pC", "fb"}, providersOf(res.Targets))
	require.Equal(t, routingSlugA, res.Targets[0].MarketSlug)
	require.True(t, res.Targets[0].BillUpstreamCost)

	c.ReorderByMarket = true
	res, err = resolveRouting(context.Background(), c, routingCache())
	require.NoError(t, err)
	require.Equal(t, routingSlugA, res.Targets[0].MarketSlug)
	require.True(t, res.Targets[0].BillUpstreamCost)
}

func TestFilterStepsForKey(t *testing.T) {
	steps := []dispatch.Target{{Provider: "pA"}, {Provider: "pB"}}

	kept, byAdmin := filterStepsForKey(steps, store.APIKey{}, "m")
	require.Equal(t, steps, kept)
	require.False(t, byAdmin, "nothing dropped")

	kept, byAdmin = filterStepsForKey(steps, store.APIKey{AllowedProviders: []string{"pA"}}, "chain:M")
	require.Equal(t, []string{"pA"}, providersOf(kept))
	require.True(t, byAdmin)

	kept, byAdmin = filterStepsForKey(steps, store.APIKey{ModelDisabledProviders: map[string][]string{"m": {"pA"}}}, "chain:M")
	require.Equal(t, []string{"pB"}, providersOf(kept))
	require.False(t, byAdmin)
}

// The console names a no-provider 422 for what it is, not a discount floor.
func TestResolveRejectCopyNoProvider(t *testing.T) {
	var bad badModelError
	require.ErrorAs(t, errNoEnabledProvider("m", false), &bad)
	title, hint := resolveRejectCopy("m", bad)
	require.Equal(t, `Rejected · model "m" has no provider this key can use`, title)
	require.Contains(t, hint, "Routing tab")

	require.ErrorAs(t, errDiscountFloor("m", 30), &bad)
	title, _ = resolveRejectCopy("m", bad)
	require.Equal(t, `Rejected · model "m" cannot meet the key's discount floor`, title)
}
