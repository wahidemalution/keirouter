// Package backupjob builds full router backups and ships them on a schedule
// to an offsite target (S3 or FTP).
//
// A backup is a logical dump of every table. Secrets sealed under this
// server's master key are re-encrypted under a passphrase on export and
// re-sealed under the target's master key on restore, so the target may use
// a different KEIROUTER_MASTER_KEY.
package backupjob

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/mydisha/keirouter/backend/internal/crypto"
	"github.com/mydisha/keirouter/backend/internal/store"
)

// MinPassphrase is the shortest backup passphrase accepted.
const MinPassphrase = 8

// MaxRestoreBytes is the largest uncompressed backup Restore reads.
const MaxRestoreBytes = 4 << 30

const (
	encScheme    = "passphrase-box-v1"
	checkText    = "keirouter-backup-check"
	sealedMarker = "portable-box"
)

// Opener seals and opens secrets with the master key (the vault sealer).
type Opener interface {
	OpenString(crypto.Sealed) (string, error)
	SealString(string) (crypto.Sealed, error)
}

type encryption struct {
	Scheme string `json:"scheme"`
	Salt   string `json:"salt"`
	Check  string `json:"check"`
}

// ErrWrongPassphrase is returned by PrepareRestore for a passphrase that does
// not open the backup.
var ErrWrongPassphrase = errors.New("wrong passphrase for this backup")

// sealedColumnPairs lists every (wrapped DEK, ciphertext) column pair holding
// a master-key-sealed secret.
var sealedColumnPairs = []struct{ table, dek, ct string }{
	{"accounts", "secret_wrapped_dek", "secret_ciphertext"},
	{"accounts", "token_wrapped_dek", "token_ciphertext"},
	{"accounts", "refresh_wrapped_dek", "refresh_ciphertext"},
	{"portal_users", "sealed_wrapped_dek", "sealed_ciphertext"},
	{"hosting_instances", "password_wrapped_dek", "password_ciphertext"},
}

// Export dumps the database with every master-key secret re-encrypted under
// passphrase.
func Export(ctx context.Context, db *store.DB, box Opener, passphrase string) (store.Backup, error) {
	if len(strings.TrimSpace(passphrase)) < MinPassphrase {
		return store.Backup{}, fmt.Errorf("passphrase must be at least %d characters", MinPassphrase)
	}
	backup, err := db.ExportBackup(ctx)
	if err != nil {
		return store.Backup{}, err
	}
	pbox, err := crypto.NewPortableBox(strings.TrimSpace(passphrase), nil)
	if err != nil {
		return store.Backup{}, err
	}
	check, err := pbox.SealString(checkText)
	if err != nil {
		return store.Backup{}, err
	}
	backup.Encryption, _ = json.Marshal(encryption{Scheme: encScheme, Salt: pbox.Salt(), Check: check})
	if err := RekeySecrets(&backup, func(sealed crypto.Sealed) (crypto.Sealed, error) {
		plain, err := box.OpenString(sealed)
		if err != nil {
			return crypto.Sealed{}, fmt.Errorf("cannot open a sealed secret (master key mismatch?): %w", err)
		}
		ct, err := pbox.SealString(plain)
		return crypto.Sealed{WrappedDEK: sealedMarker, Ciphertext: ct}, err
	}); err != nil {
		return store.Backup{}, err
	}
	return backup, nil
}

// Build writes the encrypted backup JSON to w and returns the table count.
func Build(ctx context.Context, db *store.DB, box Opener, passphrase string, w io.Writer) (int, error) {
	backup, err := Export(ctx, db, box, passphrase)
	if err != nil {
		return 0, err
	}
	if err := json.NewEncoder(w).Encode(backup); err != nil {
		return 0, err
	}
	return len(backup.Tables), nil
}

// PrepareRestore checks passphrase against the backup and re-seals every
// secret in it under box, ready for store.RestoreBackup.
func PrepareRestore(b *store.Backup, box Opener, passphrase string) error {
	var enc encryption
	if err := json.Unmarshal(b.Encryption, &enc); err != nil || enc.Scheme != encScheme {
		return errors.New("unsupported or missing backup encryption")
	}
	salt, err := crypto.DecodePortableSalt(enc.Salt)
	if err != nil {
		return errors.New("corrupt backup encryption header")
	}
	pbox, err := crypto.NewPortableBox(strings.TrimSpace(passphrase), salt)
	if err != nil {
		return err
	}
	if check, err := pbox.OpenString(enc.Check); err != nil || check != checkText {
		return ErrWrongPassphrase
	}
	if err := RekeySecrets(b, func(sealed crypto.Sealed) (crypto.Sealed, error) {
		if sealed.WrappedDEK != sealedMarker {
			return crypto.Sealed{}, errors.New("backup contains a secret that is not passphrase-encrypted")
		}
		plain, err := pbox.OpenString(sealed.Ciphertext)
		if err != nil {
			return crypto.Sealed{}, err
		}
		return box.SealString(plain)
	}); err != nil {
		return fmt.Errorf("restore failed: %w", err)
	}
	return detachSchedule(b)
}

// detachSchedule turns the scheduled backup off in a backup being restored
// and drops its run state and lease. Otherwise a restored copy (a staging
// drill, a second server) would start uploading into the source server's
// destination within a minute and rotate away the source's own files. The
// destination and credentials stay, so the admin only has to switch it on.
func detachSchedule(b *store.Backup) error {
	for ti := range b.Tables {
		t := &b.Tables[ti]
		if t.Name != "settings" {
			continue
		}
		ki, vi := -1, -1
		for i, c := range t.Columns {
			switch c {
			case "key":
				ki = i
			case "value":
				vi = i
			}
		}
		if ki < 0 || vi < 0 {
			return nil
		}
		kept := t.Rows[:0]
		for _, row := range t.Rows {
			key, _ := row[ki].(string)
			switch key {
			case StateKey, LeaseKey:
				continue
			case SettingsKey:
				raw, _ := row[vi].(string)
				var doc map[string]any
				if err := json.Unmarshal([]byte(raw), &doc); err != nil {
					return fmt.Errorf("restore failed: decode %s: %w", SettingsKey, err)
				}
				doc["enabled"] = false
				enc, err := json.Marshal(doc)
				if err != nil {
					return err
				}
				row[vi] = string(enc)
			}
			kept = append(kept, row)
		}
		t.Rows = kept
	}
	return nil
}

// RekeySecrets applies fn to every sealed secret in the backup: the
// account/portal-user column pairs and any {wrapped_dek, ciphertext} object
// nested in a settings JSON value (e.g. the bansos key).
func RekeySecrets(b *store.Backup, fn func(crypto.Sealed) (crypto.Sealed, error)) error {
	for ti := range b.Tables {
		t := &b.Tables[ti]
		idx := map[string]int{}
		for i, c := range t.Columns {
			idx[c] = i
		}
		for _, p := range sealedColumnPairs {
			if p.table != t.Name {
				continue
			}
			di, okD := idx[p.dek]
			ci, okC := idx[p.ct]
			if !okD || !okC {
				continue
			}
			for _, row := range t.Rows {
				dek, _ := row[di].(string)
				ct, _ := row[ci].(string)
				if dek == "" || ct == "" {
					continue
				}
				out, err := fn(crypto.Sealed{WrappedDEK: dek, Ciphertext: ct})
				if err != nil {
					return fmt.Errorf("%s.%s: %w", t.Name, p.ct, err)
				}
				row[di], row[ci] = out.WrappedDEK, out.Ciphertext
			}
		}
		if t.Name != "settings" {
			continue
		}
		ki, okK := idx["key"]
		vi, okV := idx["value"]
		if !okK || !okV {
			continue
		}
		for _, row := range t.Rows {
			raw, _ := row[vi].(string)
			if !strings.Contains(raw, "wrapped_dek") {
				continue
			}
			var doc any
			if err := json.Unmarshal([]byte(raw), &doc); err != nil {
				continue
			}
			changed, err := rekeyJSONSealed(doc, fn)
			if err != nil {
				return fmt.Errorf("settings %v: %w", row[ki], err)
			}
			if changed {
				enc, err := json.Marshal(doc)
				if err != nil {
					return err
				}
				row[vi] = string(enc)
			}
		}
	}
	return nil
}

// rekeyJSONSealed walks a decoded JSON document and re-keys every object that
// carries a non-empty wrapped_dek + ciphertext pair.
func rekeyJSONSealed(doc any, fn func(crypto.Sealed) (crypto.Sealed, error)) (bool, error) {
	changed := false
	switch v := doc.(type) {
	case map[string]any:
		dek, okD := v["wrapped_dek"].(string)
		ct, okC := v["ciphertext"].(string)
		if okD && okC && dek != "" && ct != "" {
			out, err := fn(crypto.Sealed{WrappedDEK: dek, Ciphertext: ct})
			if err != nil {
				return false, err
			}
			v["wrapped_dek"], v["ciphertext"] = out.WrappedDEK, out.Ciphertext
			return true, nil
		}
		for _, child := range v {
			c, err := rekeyJSONSealed(child, fn)
			if err != nil {
				return false, err
			}
			changed = changed || c
		}
	case []any:
		for _, child := range v {
			c, err := rekeyJSONSealed(child, fn)
			if err != nil {
				return false, err
			}
			changed = changed || c
		}
	}
	return changed, nil
}
