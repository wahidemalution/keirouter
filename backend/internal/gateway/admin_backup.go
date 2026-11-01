package gateway

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/mydisha/keirouter/backend/internal/backupjob"
	"github.com/mydisha/keirouter/backend/internal/store"
)

// Full backup / restore: a logical dump of every table (chains, providers,
// accounts, API keys, portal users, plans, budgets, top-ups, usage history,
// settings...) for moving a router to another server. Spent and remaining
// balances are derived from budgets, top-ups, adjustments and usage rows, all
// of which are copied verbatim, so they are identical after a restore.
//
// Secrets sealed under this server's master key are re-encrypted under a
// passphrase on export and re-sealed under the target's master key on
// restore, so the target may use a different KEIROUTER_MASTER_KEY.

const maxRestoreBytes = 4 << 30

// adminFullBackup serves POST /api/settings/backup {"passphrase": "..."} as a
// downloadable JSON file. The passphrase travels in the body, never the URL.
func (s *Server) adminFullBackup(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Passphrase string `json:"passphrase"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	pass := strings.TrimSpace(body.Passphrase)
	if len(pass) < backupjob.MinPassphrase {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("passphrase must be at least %d characters", backupjob.MinPassphrase))
		return
	}
	if s.db == nil || s.vault == nil {
		writeError(w, http.StatusServiceUnavailable, "backup not available: store or vault not configured")
		return
	}
	backup, err := backupjob.Export(r.Context(), s.db, s.vault.Sealer(), pass)
	if err != nil {
		writeError(w, http.StatusInternalServerError, sanitizeError(s.log, err, "backup failed"))
		return
	}

	name := "keirouter-backup-" + time.Now().UTC().Format("20060102-150405") + ".json"
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(backup); err != nil {
		s.log.Warn("backup stream failed", "err", err)
	}
	if s.consoleLog != nil {
		s.consoleLog.Log("INFO", "Full backup exported", fmt.Sprintf("%d tables", len(backup.Tables)))
	}
}

// adminFullRestore serves POST /api/settings/restore. The body is the backup
// file; the passphrase comes in the X-Backup-Passphrase header. The whole
// restore is one transaction: a wrong passphrase or any bad row changes nothing.
func (s *Server) adminFullRestore(w http.ResponseWriter, r *http.Request) {
	pass := strings.TrimSpace(r.Header.Get("X-Backup-Passphrase"))
	if pass == "" {
		writeError(w, http.StatusBadRequest, "X-Backup-Passphrase header is required")
		return
	}
	if s.db == nil || s.vault == nil {
		writeError(w, http.StatusServiceUnavailable, "restore not available: store or vault not configured")
		return
	}
	backup, err := decodeBackupBody(w, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := backupjob.PrepareRestore(&backup, s.vault.Sealer(), pass); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	stats, err := s.db.RestoreBackup(r.Context(), backup)
	if err != nil {
		writeError(w, http.StatusBadRequest, "restore failed (nothing was changed): "+err.Error())
		return
	}
	if s.consoleLog != nil {
		s.consoleLog.Log("WARN", "Full backup restored", fmt.Sprintf("%d tables, %d rows from %s", stats.Tables, stats.Rows, backup.CreatedAt))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "tables": stats.Tables, "rows": stats.Rows,
		"backup_created_at": backup.CreatedAt, "source_dialect": backup.SourceDialect,
		// In-memory caches (API-key auth, budgets, market snapshot) still
		// reflect the old data until the process restarts.
		"restart_required": true,
	})
}

// decodeBackupBody reads a backup upload, plain JSON or gzip (a scheduled
// backup file), capped at maxRestoreBytes before and after decompression.
func decodeBackupBody(w http.ResponseWriter, r *http.Request) (store.Backup, error) {
	br := bufio.NewReader(http.MaxBytesReader(w, r.Body, maxRestoreBytes))
	head, _ := br.Peek(2)
	if len(head) == 2 && head[0] == 0x1f && head[1] == 0x8b {
		zr, err := gzip.NewReader(br)
		if err != nil {
			return store.Backup{}, fmt.Errorf("corrupt gzip backup: %w", err)
		}
		defer zr.Close()
		return store.DecodeBackup(io.LimitReader(zr, maxRestoreBytes))
	}
	return store.DecodeBackup(br)
}
