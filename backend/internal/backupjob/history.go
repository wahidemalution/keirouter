package backupjob

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/mydisha/keirouter/backend/internal/store"
)

// historyLimit is how many runs the dashboard keeps.
const historyLimit = 20

// RunRecord is one finished run.
type RunRecord struct {
	StartedAt  time.Time `json:"started_at"`
	Result     string    `json:"result"` // success, failed or cancelled
	Size       int64     `json:"size"`
	DurationMs int64     `json:"duration_ms"`
	Note       string    `json:"note"`
	File       string    `json:"file"`
}

// Run results.
const (
	ResultSuccess   = "success"
	ResultFailed    = "failed"
	ResultCancelled = "cancelled"
)

func loadJSON(ctx context.Context, settings *store.SettingsRepo, key string, v any) error {
	raw, err := settings.Get(ctx, key)
	if errors.Is(err, store.ErrNotFound) || (err == nil && raw == "") {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(raw), v)
}

func saveJSON(ctx context.Context, settings *store.SettingsRepo, key string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return settings.Set(ctx, key, string(raw))
}

func loadState(ctx context.Context, settings *store.SettingsRepo) (State, error) {
	var st State
	return st, loadJSON(ctx, settings, StateKey, &st)
}

func loadHistory(ctx context.Context, settings *store.SettingsRepo) ([]RunRecord, error) {
	var h []RunRecord
	return h, loadJSON(ctx, settings, HistoryKey, &h)
}

// prependHistory stores rec as the newest entry, keeping historyLimit.
func prependHistory(ctx context.Context, settings *store.SettingsRepo, rec RunRecord) error {
	h, err := loadHistory(ctx, settings)
	if err != nil {
		h = nil
	}
	h = append([]RunRecord{rec}, h...)
	if len(h) > historyLimit {
		h = h[:historyLimit]
	}
	return saveJSON(ctx, settings, HistoryKey, h)
}
