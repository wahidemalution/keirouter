package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/backupjob"
)

// blockingTarget holds every Put until release is closed.
type blockingTarget struct {
	entered chan struct{}
	release chan struct{}
	got     []backupjob.Settings
}

func (b *blockingTarget) Put(ctx context.Context, _ string, r io.Reader, _ int64) error {
	if b.entered != nil {
		close(b.entered)
		b.entered = nil
	}
	select {
	case <-b.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	_, err := io.Copy(io.Discard, r)
	return err
}
func (b *blockingTarget) Size(context.Context, string) (int64, error) { return 0, nil }
func (b *blockingTarget) List(context.Context) ([]string, error)      { return nil, nil }
func (b *blockingTarget) Delete(context.Context, string) error        { return nil }
func (b *blockingTarget) Test(context.Context) error                  { return nil }

func newAutoBackupServer(t *testing.T) (*Server, *blockingTarget) {
	t.Helper()
	s := newPortalTestServer(t)
	tg := &blockingTarget{release: make(chan struct{})}
	s.newBackupTarget = func(set backupjob.Settings) (backupjob.Target, error) {
		tg.got = append(tg.got, set)
		return tg, nil
	}
	s.backupJob = backupjob.New(backupjob.Deps{
		DB: s.db, Box: s.vault.Sealer(), DataDir: t.TempDir(), Log: slog.Default(),
		NewTarget: s.newBackupTarget, Owner: "test",
	})
	return s, tg
}

func callAutoBackup(t *testing.T, h http.HandlerFunc, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		require.NoError(t, err)
		rdr = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

func validAutoBackupBody() map[string]any {
	return map[string]any{
		"enabled": true, "destination": "s3", "interval_hours": 24, "keep": 7, "upload_limit_kbps": 0,
		"passphrase": "auto-backup-pass",
		"s3": map[string]any{"region": "us-east-1", "bucket": "kr", "prefix": "backups",
			"access_key_id": "AK", "secret_access_key": "s3-secret-value"},
		"ftp": map[string]any{"port": 21, "tls": "explicit"},
	}
}

func TestAutoBackupGetHidesSecrets(t *testing.T) {
	s, _ := newAutoBackupServer(t)
	rec := callAutoBackup(t, s.adminSaveAutoBackup, http.MethodPost, "/api/settings/auto-backup", validAutoBackupBody())
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	rec = callAutoBackup(t, s.adminGetAutoBackup, http.MethodGet, "/api/settings/auto-backup", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotContains(t, rec.Body.String(), "s3-secret-value")
	require.NotContains(t, rec.Body.String(), "auto-backup-pass")
	var out struct {
		Settings struct {
			Passphrase struct{ Set bool } `json:"passphrase"`
			S3         struct {
				Bucket          string             `json:"bucket"`
				SecretAccessKey struct{ Set bool } `json:"secret_access_key"`
			} `json:"s3"`
		} `json:"settings"`
		Status struct {
			History []any `json:"history"`
		} `json:"status"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.True(t, out.Settings.Passphrase.Set)
	require.True(t, out.Settings.S3.SecretAccessKey.Set)
	require.Equal(t, "kr", out.Settings.S3.Bucket)
	require.NotNil(t, out.Status.History)
}

func TestAutoBackupSaveKeepsSecretWhenBlank(t *testing.T) {
	s, _ := newAutoBackupServer(t)
	require.Equal(t, http.StatusOK, callAutoBackup(t, s.adminSaveAutoBackup, http.MethodPost, "/api/settings/auto-backup", validAutoBackupBody()).Code)
	body := validAutoBackupBody()
	body["passphrase"] = ""
	body["s3"].(map[string]any)["secret_access_key"] = ""
	body["keep"] = 3
	rec := callAutoBackup(t, s.adminSaveAutoBackup, http.MethodPost, "/api/settings/auto-backup", body)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	got, err := backupjob.Load(context.Background(), s.settings.Get, s.vault.Sealer())
	require.NoError(t, err)
	require.Equal(t, "s3-secret-value", got.S3.SecretAccessKey)
	require.Equal(t, "auto-backup-pass", got.Passphrase)
	require.Equal(t, 3, got.Keep)
}

func TestAutoBackupSaveValidation(t *testing.T) {
	s, _ := newAutoBackupServer(t)
	body := validAutoBackupBody()
	body["interval_hours"] = 0
	rec := callAutoBackup(t, s.adminSaveAutoBackup, http.MethodPost, "/api/settings/auto-backup", body)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	var out struct {
		Error struct {
			Message string `json:"message"`
			Field   string `json:"field"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.Equal(t, "interval_hours", out.Error.Field)
	require.NotEmpty(t, out.Error.Message)
}

func TestAutoBackupTestUsesUnsavedValues(t *testing.T) {
	s, tg := newAutoBackupServer(t)
	body := validAutoBackupBody()
	body["s3"].(map[string]any)["bucket"] = "unsaved-bucket"
	rec := callAutoBackup(t, s.adminTestAutoBackup, http.MethodPost, "/api/settings/auto-backup/test", body)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Len(t, tg.got, 1)
	require.Equal(t, "unsaved-bucket", tg.got[0].S3.Bucket)
	require.Equal(t, "s3-secret-value", tg.got[0].S3.SecretAccessKey)

	_, err := s.settings.Get(context.Background(), backupjob.SettingsKey)
	require.Error(t, err, "testing must not save")
}

func TestAutoBackupRunConflict(t *testing.T) {
	s, tg := newAutoBackupServer(t)
	require.Equal(t, http.StatusOK, callAutoBackup(t, s.adminSaveAutoBackup, http.MethodPost, "/api/settings/auto-backup", validAutoBackupBody()).Code)
	tg.entered = make(chan struct{})
	entered := tg.entered
	rec := callAutoBackup(t, s.adminRunAutoBackup, http.MethodPost, "/api/settings/auto-backup/run", nil)
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	<-entered
	rec = callAutoBackup(t, s.adminRunAutoBackup, http.MethodPost, "/api/settings/auto-backup/run", nil)
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	close(tg.release)
}

func TestAutoBackupRunRequiresCompleteSettings(t *testing.T) {
	s, _ := newAutoBackupServer(t)
	rec := callAutoBackup(t, s.adminRunAutoBackup, http.MethodPost, "/api/settings/auto-backup/run", nil)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}

func TestAutoBackupRoutesRequireAdmin(t *testing.T) {
	h := newE2E(t, func(w http.ResponseWriter, _ *http.Request) {})
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/api/settings/auto-backup"},
		{http.MethodPost, "/api/settings/auto-backup"},
		{http.MethodPost, "/api/settings/auto-backup/test"},
		{http.MethodPost, "/api/settings/auto-backup/run"},
	} {
		req := httptest.NewRequest(c.method, c.path, bytes.NewReader([]byte("{}")))
		req.RemoteAddr = "127.0.0.1:1234"
		rec := httptest.NewRecorder()
		h.gateway.Handler().ServeHTTP(rec, req)
		require.Equal(t, http.StatusUnauthorized, rec.Code, "%s %s", c.method, c.path)
	}
}
