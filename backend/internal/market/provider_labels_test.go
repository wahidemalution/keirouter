package market

import (
	"strings"
	"testing"
)

func TestParseSurplusProviderLabels(t *testing.T) {
	book := `{"offers":[
		{"provider_id":"dashscope-cn","provider":"Alibaba DashScope — China","trusted":true},
		{"provider_id":"OpenRouter","provider":"  OpenRouter ","trusted":false},
		{"provider_id":"broken","provider":{"x":1}},
		{"provider_id":"","provider":"Nobody"}]}`
	got, err := ParseSurplusProviderLabels(strings.NewReader(book))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"dashscope-cn": "Alibaba DashScope - China", "openrouter": "OpenRouter"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for id, label := range want {
		if got[id] != label {
			t.Errorf("%s: got %q, want %q", id, got[id], label)
		}
	}
}

func TestProviderLabelsSurviveReplace(t *testing.T) {
	c := NewSnapshotCache()
	c.MergeProviderLabels(map[string]string{"venice": "Venice AI"})
	c.Replace(nil)
	if got := c.ProviderLabel("Venice"); got != "Venice AI" {
		t.Fatalf("got %q", got)
	}
	var nilCache *SnapshotCache
	if nilCache.ProviderLabel("venice") != "" {
		t.Fatal("nil cache must return empty")
	}
}
