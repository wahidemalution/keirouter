package market

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const modelsDevFixture = `{
  "deepseek": {"models": {
    "deepseek-v4-flash": {"id":"deepseek-v4-flash","canonical_model_id":"deepseek/deepseek-v4.1-flash",
      "cost":{"input":0.15,"output":0.6},"limit":{"context":1000000,"output":393216}}
  }},
  "reseller-a": {"models": {
    "deepseek/deepseek-v4.1-flash": {"id":"deepseek/deepseek-v4.1-flash","canonical_model_id":"deepseek/deepseek-v4.1-flash",
      "cost":{"input":0.3,"output":1.2},"limit":{"context":1048576}},
    "glm-x": {"id":"glm-x","cost":{"input":0.4,"output":2},"limit":{"context":131072}},
    "glm-x:free": {"id":"glm-x:free","cost":{"input":0,"output":0},"limit":{"context":131072}}
  }},
  "reseller-b": {"models": {
    "glm-x": {"id":"glm-x","cost":{"input":0.2,"output":1},"limit":{"context":200000}}
  }},
  "reseller-c": {"models": {
    "zai/glm-x": {"id":"zai/glm-x","cost":{"input":0.2,"output":1},"limit":{"context":200000}}
  }},
  "zhipuai": {"models": {
    "glm-5.3-flash": {"id":"glm-5.3-flash","canonical_model_id":"zhipuai/glm-5.3-flash","cost":{"input":0.15,"output":0.5},"limit":{"context":1000000}},
    "glm-5.3-flashx": {"id":"glm-5.3-flashx","canonical_model_id":"zhipuai/glm-5.3-flash","cost":{"input":0.37,"output":1.25},"limit":{"context":1000000}}
  }}
}`

func parseFixture(t *testing.T) *ReferenceCache {
	t.Helper()
	refs, err := ParseModelsDev(strings.NewReader(modelsDevFixture))
	require.NoError(t, err)
	c := NewReferenceCache()
	c.Replace(refs)
	return c
}

func TestModelsDevPrefersVendorListing(t *testing.T) {
	c := parseFixture(t)
	for _, name := range []string{"deepseek-v4.1-flash", "deepseek/deepseek-v4.1-flash", "DeepSeek-V4_1-Flash"} {
		ref, ok := c.Lookup(name)
		require.True(t, ok, name)
		require.Equal(t, Reference{InputPerM: 0.15, OutputPerM: 0.6, ContextTokens: 1000000, Provider: "deepseek"}, ref,
			"the vendor's own listing (matched by canonical id) wins over resellers")
	}
}

func TestModelsDevPrefersExactVendorID(t *testing.T) {
	ref, ok := parseFixture(t).Lookup("glm-5.3-flash")
	require.True(t, ok)
	require.Equal(t, 0.15, ref.InputPerM, "glm-5.3-flashx shares the canonical id but is a different SKU")
	require.Equal(t, "zhipuai", ref.Provider)
}

func TestModelsDevFallsBackToMostCommonPrice(t *testing.T) {
	ref, ok := parseFixture(t).Lookup("glm-x")
	require.True(t, ok)
	require.Equal(t, Reference{InputPerM: 0.2, OutputPerM: 1, ContextTokens: 200000}, ref,
		"no vendor listing: the price two providers share beats the outlier; the free variant is ignored")
}

func TestModelsDevCachePrices(t *testing.T) {
	refs, err := ParseModelsDev(strings.NewReader(`{
  "deepseek": {"models": {
    "deepseek-v4-flash": {"id":"deepseek-v4-flash","canonical_model_id":"deepseek/deepseek-v4-flash",
      "cost":{"input":0.28,"output":0.42,"cache_read":0.028,"cache_write":0.35}}
  }},
  "r1": {"models": {"m": {"id":"m","cost":{"input":1,"output":2,"cache_read":0.1}}}},
  "r2": {"models": {"m": {"id":"m","cost":{"input":1,"output":2,"cache_read":0.1}}}},
  "r3": {"models": {"m": {"id":"m","cost":{"input":1,"output":2,"cache_read":0.5,"cache_write":1.25}}}},
  "r4": {"models": {"m": {"id":"m","cost":{"input":3,"output":4,"cache_read":0.01}}}}
}`))
	require.NoError(t, err)
	c := NewReferenceCache()
	c.Replace(refs)

	ref, ok := c.Lookup("deepseek-v4-flash")
	require.True(t, ok)
	require.Equal(t, 0.028, ref.CacheReadPerM)
	require.Equal(t, 0.35, ref.CacheWritePerM)

	ref, ok = c.Lookup("m")
	require.True(t, ok)
	require.Equal(t, 1.0, ref.InputPerM)
	require.Equal(t, 0.1, ref.CacheReadPerM, "cache comes from the most common listing of the chosen price")
	require.Equal(t, 0.0, ref.CacheWritePerM, "unlisted cache write stays 0 (unknown)")
}
