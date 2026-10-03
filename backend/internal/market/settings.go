package market

import (
	"context"
	"encoding/json"
)

const SettingsKey = "market_pricing_settings"

type Settings struct {
	AutoRefresh            bool    `json:"auto_refresh"`
	RefreshIntervalMinutes int     `json:"refresh_interval_minutes"`
	MarkupPercent          float64 `json:"markup_percent"`
	LastFetchedAt          string  `json:"last_fetched_at"`
	LastFetchError         string  `json:"last_fetch_error,omitempty"`
	LastSyncedCount        int     `json:"last_synced_count"`
}

func DefaultSettings() Settings {
	return Settings{AutoRefresh: false, RefreshIntervalMinutes: 2, MarkupPercent: 10}
}

func LoadSettings(ctx context.Context, get func(context.Context, string) (string, error)) Settings {
	def := DefaultSettings()
	raw, err := get(ctx, SettingsKey)
	if err != nil || raw == "" {
		return def
	}
	s := def
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return def
	}
	if s.RefreshIntervalMinutes <= 0 {
		s.RefreshIntervalMinutes = def.RefreshIntervalMinutes
	}
	if s.MarkupPercent < 0 {
		s.MarkupPercent = def.MarkupPercent
	}
	return s
}

func SaveSettings(ctx context.Context, set func(context.Context, string, string) error, s Settings) error {
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return set(ctx, SettingsKey, string(raw))
}