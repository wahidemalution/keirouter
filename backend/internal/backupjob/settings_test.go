package backupjob

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/httputil"
	"github.com/mydisha/keirouter/backend/internal/store"
)

func validS3() Settings {
	s := Defaults()
	s.Enabled = true
	s.Destination = DestS3
	s.Passphrase = "long-enough-pass"
	s.S3 = S3Settings{Region: "us-east-1", Bucket: "kr-backup", AccessKeyID: "AK", SecretAccessKey: "SK"}
	return s
}

func fieldOf(t *testing.T, err error) string {
	t.Helper()
	var fe *FieldError
	require.True(t, errors.As(err, &fe), "want *FieldError, got %v", err)
	return fe.Field
}

func TestValidateBounds(t *testing.T) {
	require.NoError(t, validS3().Validate())
	cases := []struct {
		field string
		mut   func(*Settings)
	}{
		{"interval_hours", func(s *Settings) { s.IntervalHours = 0 }},
		{"interval_hours", func(s *Settings) { s.IntervalHours = 721 }},
		{"keep", func(s *Settings) { s.Keep = 0 }},
		{"keep", func(s *Settings) { s.Keep = 366 }},
		{"upload_limit_kbps", func(s *Settings) { s.UploadLimitKBps = -1 }},
		{"upload_limit_kbps", func(s *Settings) { s.UploadLimitKBps = 1_000_001 }},
		{"passphrase", func(s *Settings) { s.Passphrase = "short" }},
		{"s3.bucket", func(s *Settings) { s.S3.Bucket = "" }},
		{"s3.secret_access_key", func(s *Settings) { s.S3.SecretAccessKey = "" }},
		{"destination", func(s *Settings) { s.Destination = "dropbox" }},
		{"ftp.host", func(s *Settings) { s.Destination = DestFTP; s.FTP.Username, s.FTP.Password = "u", "p" }},
		{"ftp.tls", func(s *Settings) {
			s.Destination = DestFTP
			s.FTP = FTPSettings{Host: "ftp.example.com", Port: 21, TLS: "implicit", Username: "u", Password: "p"}
		}},
	}
	for _, c := range cases {
		s := validS3()
		c.mut(&s)
		require.Equal(t, c.field, fieldOf(t, s.Validate()), "case %s", c.field)
	}
	// Disabled settings may be incomplete, but bounds still apply.
	s := Defaults()
	require.NoError(t, s.Validate())
	s.Keep = 0
	require.Equal(t, "keep", fieldOf(t, s.Validate()))
}

func TestValidateRejectsPrivateHost(t *testing.T) {
	httputil.SetAllowPrivateBaseURL(false)
	s := validS3()
	s.Destination = DestFTP
	s.FTP = FTPSettings{Host: "10.0.0.5", Port: 21, TLS: TLSExplicit, Username: "u", Password: "p"}
	require.Equal(t, "ftp.host", fieldOf(t, s.Validate()))

	s = validS3()
	s.S3.Endpoint = "https://192.168.1.10:9000"
	require.Equal(t, "s3.endpoint", fieldOf(t, s.Validate()))
	s.S3.Endpoint = "http://minio.example.com"
	require.Equal(t, "s3.endpoint", fieldOf(t, s.Validate()), "plain http needs allow_private_base_url")

	httputil.SetAllowPrivateBaseURL(true)
	t.Cleanup(func() { httputil.SetAllowPrivateBaseURL(false) })
	s.S3.Endpoint = "http://192.168.1.10:9000"
	require.NoError(t, s.Validate())
}

func TestMergeKeepsStoredSecrets(t *testing.T) {
	stored := validS3()
	stored.FTP.Password = "ftp-pass"
	in := stored
	in.Passphrase, in.S3.SecretAccessKey, in.FTP.Password = "", "", ""
	in.Keep = 3
	got := Merge(stored, in)
	require.Equal(t, "long-enough-pass", got.Passphrase)
	require.Equal(t, "SK", got.S3.SecretAccessKey)
	require.Equal(t, "ftp-pass", got.FTP.Password)
	require.Equal(t, 3, got.Keep)

	in.S3.SecretAccessKey = "SK2"
	require.Equal(t, "SK2", Merge(stored, in).S3.SecretAccessKey)
}

func TestPublicHidesSecrets(t *testing.T) {
	s := validS3()
	s.S3.SecretAccessKey = "s3-secret-value"
	s.Passphrase = "passphrase-value"
	s.FTP.Password = "ftp-password-value"
	raw, err := json.Marshal(s.Public())
	require.NoError(t, err)
	for _, leak := range []string{"s3-secret-value", "passphrase-value", "ftp-password-value"} {
		require.NotContains(t, string(raw), leak)
	}
	p := s.Public()
	require.True(t, p.Passphrase.Set)
	require.True(t, p.S3.SecretAccessKey.Set)
	require.Equal(t, "AK", p.S3.AccessKeyID)
}

func TestNormalizePrefix(t *testing.T) {
	for in, want := range map[string]string{"/a/b": "a/b/", "a": "a/", "": "", "a//": "a/", "/": "", " a/b/ ": "a/b/"} {
		require.Equal(t, want, NormalizePrefix(in), "input %q", in)
	}
}

func TestLoadDefaultsWhenAbsent(t *testing.T) {
	db := newDB(t)
	got, err := Load(context.Background(), db.Settings().Get, newSealer(t))
	require.NoError(t, err)
	require.Equal(t, Defaults(), got)
	require.Equal(t, 24, got.IntervalHours)
	require.Equal(t, 7, got.Keep)
	require.Equal(t, DestS3, got.Destination)
	require.Equal(t, 21, got.FTP.Port)
	require.Equal(t, TLSExplicit, got.FTP.TLS)
}

func TestSaveSealsSecrets(t *testing.T) {
	ctx := context.Background()
	db, sealer := newDB(t), newSealer(t)
	s := validS3()
	s.S3.SecretAccessKey = "s3-secret-value"
	require.NoError(t, Save(ctx, db.Settings().Set, sealer, s))
	raw, err := db.Settings().Get(ctx, SettingsKey)
	require.NoError(t, err)
	require.NotContains(t, raw, "s3-secret-value")
	require.NotContains(t, raw, "long-enough-pass")
	got, err := Load(ctx, db.Settings().Get, sealer)
	require.NoError(t, err)
	require.Equal(t, s, got)
}

// Review Focus 1: a backup restored under another master key must keep the
// auto-backup secrets usable there.
func TestStoredSecretsSurviveBackupRekey(t *testing.T) {
	ctx := context.Background()
	src, srcBox := newDB(t), newSealer(t)
	s := validS3()
	s.FTP.Password = "ftp-password-value"
	require.NoError(t, Save(ctx, src.Settings().Set, srcBox, s))

	var buf bytes.Buffer
	_, err := Build(ctx, src, srcBox, "file-passphrase", &buf)
	require.NoError(t, err)
	b, err := store.DecodeBackup(&buf)
	require.NoError(t, err)

	dst, dstBox := newDB(t), newSealer(t)
	require.NoError(t, PrepareRestore(&b, dstBox, "file-passphrase"))
	_, err = dst.RestoreBackup(ctx, b)
	require.NoError(t, err)

	got, err := Load(ctx, dst.Settings().Get, dstBox)
	require.NoError(t, err)
	require.Equal(t, "SK", got.S3.SecretAccessKey)
	require.Equal(t, "ftp-password-value", got.FTP.Password)
	require.Equal(t, "long-enough-pass", got.Passphrase)
}

// Review I-3: a restored server must not start shipping into the source
// server's destination (and rotating its files) on its own.
func TestRestoreDisablesScheduleAndDropsState(t *testing.T) {
	ctx := context.Background()
	src, srcBox := newDB(t), newSealer(t)
	require.NoError(t, Save(ctx, src.Settings().Set, srcBox, validS3()))
	require.NoError(t, src.Settings().Set(ctx, StateKey, `{"last_success":"2026-11-01T00:00:00Z"}`))
	ok, err := src.Settings().ClaimLease(ctx, LeaseKey, "src", time.Now(), time.Hour)
	require.NoError(t, err)
	require.True(t, ok)

	var buf bytes.Buffer
	_, err = Build(ctx, src, srcBox, "file-passphrase", &buf)
	require.NoError(t, err)
	b, err := store.DecodeBackup(&buf)
	require.NoError(t, err)
	dst, dstBox := newDB(t), newSealer(t)
	require.NoError(t, PrepareRestore(&b, dstBox, "file-passphrase"))
	_, err = dst.RestoreBackup(ctx, b)
	require.NoError(t, err)

	got, err := Load(ctx, dst.Settings().Get, dstBox)
	require.NoError(t, err)
	require.False(t, got.Enabled, "schedule must be off after a restore")
	require.Equal(t, "SK", got.S3.SecretAccessKey, "the rest of the config survives")
	_, err = dst.Settings().Get(ctx, StateKey)
	require.ErrorIs(t, err, store.ErrNotFound)
	_, err = dst.Settings().Get(ctx, LeaseKey)
	require.ErrorIs(t, err, store.ErrNotFound)
}
