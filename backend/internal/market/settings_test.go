package market

import (
	"context"
	"testing"
)

func TestSettingsDefaultsAndClamp(t *testing.T) {
	get := func(context.Context, string) (string, error) { return "", nil }
	s := LoadSettings(context.Background(), get)
	if s.AutoRefresh || s.RefreshIntervalMinutes != 2 || s.MarkupPercent != 10 {
		t.Fatalf("unexpected defaults %+v", s)
	}
	getBad := func(context.Context, string) (string, error) {
		return `{"auto_refresh":true,"refresh_interval_minutes":0,"markup_percent":-5}`, nil
	}
	s2 := LoadSettings(context.Background(), getBad)
	if s2.RefreshIntervalMinutes != 2 || s2.MarkupPercent != 10 {
		t.Fatalf("expected clamped defaults %+v", s2)
	}
}