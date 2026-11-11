package backupjob

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/config"
	"github.com/mydisha/keirouter/backend/internal/crypto"
	"github.com/mydisha/keirouter/backend/internal/store"
)

func newDB(t *testing.T) *store.DB {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, config.DatabaseConfig{Driver: "sqlite", DSN: ":memory:"}, t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.Migrate(ctx))
	require.NoError(t, db.Tenants().EnsureDefault(ctx))
	return db
}

func newSealer(t *testing.T) *crypto.Sealer {
	t.Helper()
	key, err := crypto.GenerateMasterKey()
	require.NoError(t, err)
	s, err := crypto.NewSealer(key)
	require.NoError(t, err)
	return s
}

func TestBuildEncryptsSecretsAndRestoresUnderAnotherKey(t *testing.T) {
	ctx := context.Background()
	db, sealer := newDB(t), newSealer(t)
	sec, err := sealer.SealString("plain-secret-42")
	require.NoError(t, err)
	raw, _ := json.Marshal(map[string]any{"token": sec})
	require.NoError(t, db.Settings().Set(ctx, "test.sealed", string(raw)))

	var buf bytes.Buffer
	n, err := Build(ctx, db, sealer, "long-enough-pass", &buf)
	require.NoError(t, err)
	require.Positive(t, n)
	require.NotContains(t, buf.String(), "plain-secret-42")

	b, err := store.DecodeBackup(&buf)
	require.NoError(t, err)
	other := newSealer(t)
	require.ErrorIs(t, PrepareRestore(&b, other, "wrong-passphrase"), ErrWrongPassphrase)
	require.NoError(t, PrepareRestore(&b, other, "long-enough-pass"))

	dst := newDB(t)
	_, err = dst.RestoreBackup(ctx, b)
	require.NoError(t, err)
	got, err := dst.Settings().Get(ctx, "test.sealed")
	require.NoError(t, err)
	var doc struct {
		Token crypto.Sealed `json:"token"`
	}
	require.NoError(t, json.Unmarshal([]byte(got), &doc))
	plain, err := other.OpenString(doc.Token)
	require.NoError(t, err)
	require.Equal(t, "plain-secret-42", plain)
}

func TestBuildRejectsShortPassphrase(t *testing.T) {
	_, err := Build(context.Background(), newDB(t), newSealer(t), "short", &bytes.Buffer{})
	require.Error(t, err)
	require.False(t, errors.Is(err, ErrWrongPassphrase))
}
