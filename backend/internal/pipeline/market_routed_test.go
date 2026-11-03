package pipeline

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/core"
	"github.com/mydisha/keirouter/backend/internal/dispatch"
	"github.com/mydisha/keirouter/backend/internal/market"
	"github.com/mydisha/keirouter/backend/internal/store"
)

// Only surplus-trusted and surplus-fast steps are marketplace-routed; the flag must not leak
// onto direct provider steps, and the scope is left alone so same-attempt
// retries of transient faults still apply.
func TestProviderErrorForAttemptMarksMarketRoutes(t *testing.T) {
	upstream := &core.ProviderError{Kind: core.ErrUpstream, StatusCode: 503}

	trusted := dispatch.Attempt{Target: dispatch.Target{Provider: "custom-openai-surplus", Model: "glm-5.3-flash", MarketSlug: "surplus-trusted:glm-5.3-flash"}}
	pe := providerErrorForAttempt(upstream, trusted)
	require.True(t, pe.MarketRouted)
	require.Equal(t, core.FailureScopeProvider, pe.EffectiveScope())
	require.False(t, upstream.MarketRouted, "the shared upstream error must not be mutated")

	fast := dispatch.Attempt{Target: dispatch.Target{Provider: "custom-openai-surplus", Model: "glm-5.3-flash", MarketSlug: "surplus-fast:glm-5.3-flash"}}
	require.True(t, providerErrorForAttempt(upstream, fast).MarketRouted)

	direct := dispatch.Attempt{Target: dispatch.Target{Provider: "custom-openai-inferhub", Model: "ali/deepseek-v4.1-flash", MarketSlug: "ali/deepseek-v4.1-flash"}}
	require.False(t, providerErrorForAttempt(upstream, direct).MarketRouted)
	require.False(t, providerErrorForAttempt(errors.New("boom"), dispatch.Attempt{}).MarketRouted)
}

// slugSkipConnector fails every call the way a Surplus answer of the given
// kind would, on both the chat and stream paths.
type slugSkipConnector struct {
	admissionFallbackConnector
	err *core.ProviderError
}

func (c slugSkipConnector) Chat(context.Context, *core.ChatRequest, core.Credentials) (*core.ChatResponse, error) {
	return nil, c.err
}

func (c slugSkipConnector) Stream(context.Context, *core.ChatRequest, core.Credentials, core.StreamConfig) (<-chan core.StreamChunk, error) {
	return nil, c.err
}

func TestOutOfStockAddsSlugSkip(t *testing.T) {
	const slug = "surplus-trusted@openrouter:m"
	outOfStock := &core.ProviderError{Kind: core.ErrModelOutOfStock, Scope: core.FailureScopeRequest, StatusCode: 404, Message: "no_sellers_for_model"}
	upstream500 := &core.ProviderError{Kind: core.ErrUpstream, StatusCode: 500, Message: "boom"}
	cases := []struct {
		name   string
		err    *core.ProviderError
		slug   string
		stream bool
		want   bool
	}{
		{"chat out of stock", outOfStock, slug, false, true},
		{"stream out of stock", outOfStock, slug, true, true},
		{"chat 500", upstream500, slug, false, false},
		{"stream 500", upstream500, slug, true, false},
		{"out of stock on a direct slug", outOfStock, "ali/deepseek-v4.1-flash", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newPlannerDispatcherWithConnector(t, slugSkipConnector{err: tc.err}, "acc-1")
			skips := &market.SlugSkips{}
			var logs bytes.Buffer
			p := New(Deps{Dispatcher: d, SlugSkips: skips, Logger: slog.New(slog.NewTextHandler(&logs, nil))})
			req := &core.ChatRequest{
				Model: "gpt-4o",
				Messages: []core.Message{{
					Role: core.RoleUser, Content: []core.ContentPart{{Type: core.PartText, Text: "hello"}},
				}},
				Metadata: core.RequestMetadata{TenantID: store.DefaultTenantID, SourceDialect: core.DialectOpenAI},
			}
			opts := Options{Targets: []dispatch.Target{{Provider: "openai", Model: "gpt-4o", MarketSlug: tc.slug}}}
			var err error
			if tc.stream {
				_, err = p.Stream(context.Background(), req, opts)
			} else {
				_, err = p.Chat(context.Background(), req, opts)
			}
			require.Error(t, err)
			require.Equal(t, tc.want, skips.Has(tc.slug))
			line := "level=INFO msg=\"market slug set aside until the next refresh: out of stock\" slug=" + tc.slug
			require.Equal(t, tc.want, strings.Count(logs.String(), line) == 1, logs.String())
		})
	}
}
