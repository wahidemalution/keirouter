package connectors

import (
	"testing"

	"github.com/mydisha/keirouter/backend/internal/core"
)

func TestSetCostLookupUpdatesRegisteredProviderOnly(t *testing.T) {
	RegisterDynamicProvider(DynamicProvider{ID: "custom-openai-cl", DisplayName: "cl", Alias: "cl", Dialect: core.DialectOpenAI, BaseURL: "https://x.test/v1"})
	t.Cleanup(func() { UnregisterDynamicProvider("custom-openai-cl") })

	SetCostLookup("custom-openai-cl", true)
	if !CostLookupEnabled("custom-openai-cl") {
		t.Fatal("flag not set")
	}
	SetCostLookup("custom-openai-cl", false)
	if CostLookupEnabled("custom-openai-cl") {
		t.Fatal("flag not cleared")
	}
	SetCostLookup("custom-openai-unknown", true)
	if _, ok := SpecByID("custom-openai-unknown"); ok {
		t.Fatal("an unknown id must not be registered")
	}
}
