package market

import "testing"

func TestParseProviderSlug(t *testing.T) {
	p, m, ok := ParseProviderSlug("surplus-trusted@OpenRouter:deepseek-v4.1-flash")
	if !ok || p != "openrouter" || m != "deepseek-v4.1-flash" {
		t.Fatalf("got %q, %q, %v", p, m, ok)
	}
	for _, bad := range []string{
		"surplus-trusted@:m",
		"surplus-trusted@p:",
		"surplus-trusted@-p:m",
		"surplus-trusted@p q:m",
		"surplus-trusted@p",
		"surplus-trusted:m",
		"surplus-fast@p:m",
		"",
	} {
		if _, _, ok := ParseProviderSlug(bad); ok {
			t.Errorf("ParseProviderSlug(%q) accepted", bad)
		}
	}
}

func TestProviderSlug(t *testing.T) {
	slug := ProviderSlug("alibaba-cn", "glm-5.3")
	if slug != "surplus-trusted@alibaba-cn:glm-5.3" {
		t.Fatalf("got %q", slug)
	}
	if p, m, ok := ParseProviderSlug(slug); !ok || p != "alibaba-cn" || m != "glm-5.3" {
		t.Fatalf("round trip: %q, %q, %v", p, m, ok)
	}
}

func TestCutUpstreamCostSlugProviderForm(t *testing.T) {
	name, ok := CutUpstreamCostSlug("surplus-trusted@alibaba-cn:glm-5.3")
	if !ok || name != "glm-5.3" {
		t.Fatalf("got %q, %v", name, ok)
	}
	if !IsUpstreamCostSlug("surplus-trusted@alibaba-cn:glm-5.3") {
		t.Error("IsUpstreamCostSlug rejected the provider form")
	}
	if _, ok := CutUpstreamCostSlug("surplus-trusted@bad id:glm-5.3"); ok {
		t.Error("an invalid provider id was accepted")
	}
	if got := SlugProvider("surplus-trusted@alibaba-cn:glm-5.3"); got != "alibaba-cn" {
		t.Errorf("SlugProvider = %q", got)
	}
	if got := SlugProvider("surplus-trusted:glm-5.3"); got != "" {
		t.Errorf("SlugProvider of the plain form = %q", got)
	}
}

func TestValidateStepSlug(t *testing.T) {
	cases := []struct {
		slug       string
		chainLevel bool
		wantErr    bool
	}{
		{"surplus-trusted@openrouter:m", false, false},
		{"surplus-trusted@openrouter:m", true, true},
		{"surplus-fast@openrouter:m", false, true},
		{"surplus-fast@openrouter:m", true, true},
		{"surplus-trusted@Bad Id:m", false, true},
		{"surplus-trusted@p:", false, true},
		{"surplus-trusted:m", true, false},
		{"surplus-trusted:m", false, false},
		{"surplus-fast:m", false, false},
		{"surplus:m", false, false},
		{"ali/deepseek-v4.1-flash", true, false},
		{"", true, false},
		{"", false, false},
	}
	for _, c := range cases {
		err := ValidateStepSlug(c.slug, c.chainLevel)
		if (err != nil) != c.wantErr {
			t.Errorf("ValidateStepSlug(%q, %v) = %v, wantErr %v", c.slug, c.chainLevel, err, c.wantErr)
		}
	}
}
