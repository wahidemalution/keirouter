package gateway

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/mydisha/keirouter/backend/internal/backupjob"
)

// autoBackupInput is the body of a save or test request. Secret fields left
// empty keep the stored value.
type autoBackupInput struct {
	Enabled         bool   `json:"enabled"`
	Destination     string `json:"destination"`
	IntervalHours   int    `json:"interval_hours"`
	Keep            int    `json:"keep"`
	UploadLimitKBps int    `json:"upload_limit_kbps"`
	Passphrase      string `json:"passphrase"`
	S3              struct {
		Endpoint        string `json:"endpoint"`
		Region          string `json:"region"`
		Bucket          string `json:"bucket"`
		Prefix          string `json:"prefix"`
		AccessKeyID     string `json:"access_key_id"`
		SecretAccessKey string `json:"secret_access_key"`
		PathStyle       bool   `json:"path_style"`
	} `json:"s3"`
	FTP struct {
		Host     string `json:"host"`
		Port     int    `json:"port"`
		TLS      string `json:"tls"`
		Username string `json:"username"`
		Password string `json:"password"`
		Folder   string `json:"folder"`
	} `json:"ftp"`
}

func (in autoBackupInput) settings() backupjob.Settings {
	return backupjob.Settings{
		Enabled: in.Enabled, Destination: in.Destination, IntervalHours: in.IntervalHours,
		Keep: in.Keep, UploadLimitKBps: in.UploadLimitKBps, Passphrase: in.Passphrase,
		S3: backupjob.S3Settings{
			Endpoint: in.S3.Endpoint, Region: in.S3.Region, Bucket: in.S3.Bucket, Prefix: in.S3.Prefix,
			AccessKeyID: in.S3.AccessKeyID, SecretAccessKey: in.S3.SecretAccessKey, PathStyle: in.S3.PathStyle,
		},
		FTP: backupjob.FTPSettings{
			Host: in.FTP.Host, Port: in.FTP.Port, TLS: in.FTP.TLS, Username: in.FTP.Username,
			Password: in.FTP.Password, Folder: in.FTP.Folder,
		},
	}
}

func writeAutoBackupError(w http.ResponseWriter, err error) {
	var fe *backupjob.FieldError
	if errors.As(err, &fe) {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": map[string]any{"message": fe.Error(), "type": errorType(http.StatusBadRequest), "field": fe.Field},
		})
		return
	}
	writeError(w, http.StatusBadRequest, err.Error())
}

func (s *Server) autoBackupReady(w http.ResponseWriter) bool {
	if s.db == nil || s.vault == nil {
		writeError(w, http.StatusServiceUnavailable, "auto backup not available: store or vault not configured")
		return false
	}
	return true
}

func (s *Server) loadAutoBackup(ctx context.Context) (backupjob.Settings, error) {
	return backupjob.Load(ctx, s.settings.Get, s.vault.Sealer())
}

// autoBackupView is the GET/POST response.
func (s *Server) autoBackupView(ctx context.Context) (map[string]any, error) {
	set, err := s.loadAutoBackup(ctx)
	if err != nil {
		return nil, err
	}
	status := backupjob.Status{History: []backupjob.RunRecord{}}
	if s.backupJob != nil {
		if status, err = s.backupJob.Status(ctx); err != nil {
			return nil, err
		}
	}
	return map[string]any{"settings": set.Public(), "status": status}, nil
}

// adminGetAutoBackup serves GET /api/settings/auto-backup.
func (s *Server) adminGetAutoBackup(w http.ResponseWriter, r *http.Request) {
	if !s.autoBackupReady(w) {
		return
	}
	view, err := s.autoBackupView(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, sanitizeError(s.log, err, "load auto backup settings failed"))
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// adminSaveAutoBackup serves POST /api/settings/auto-backup.
func (s *Server) adminSaveAutoBackup(w http.ResponseWriter, r *http.Request) {
	if !s.autoBackupReady(w) {
		return
	}
	var in autoBackupInput
	if !decodeJSON(w, r, &in) {
		return
	}
	stored, err := s.loadAutoBackup(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, sanitizeError(s.log, err, "load auto backup settings failed"))
		return
	}
	next := backupjob.Merge(stored, in.settings())
	if err := next.Validate(); err != nil {
		writeAutoBackupError(w, err)
		return
	}
	if err := backupjob.Save(r.Context(), s.settings.Set, s.vault.Sealer(), next); err != nil {
		writeError(w, http.StatusInternalServerError, sanitizeError(s.log, err, "save auto backup settings failed"))
		return
	}
	view, err := s.autoBackupView(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, sanitizeError(s.log, err, "load auto backup settings failed"))
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// adminTestAutoBackup serves POST /api/settings/auto-backup/test with the
// submitted (unsaved) values over the stored secrets.
func (s *Server) adminTestAutoBackup(w http.ResponseWriter, r *http.Request) {
	if !s.autoBackupReady(w) {
		return
	}
	var in autoBackupInput
	if !decodeJSON(w, r, &in) {
		return
	}
	stored, err := s.loadAutoBackup(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, sanitizeError(s.log, err, "load auto backup settings failed"))
		return
	}
	next := backupjob.Merge(stored, in.settings())
	next.Enabled = true // a test needs a complete destination
	if err := next.Validate(); err != nil {
		writeAutoBackupError(w, err)
		return
	}
	newTarget := s.newBackupTarget
	if newTarget == nil {
		newTarget = backupjob.NewTarget
	}
	target, err := newTarget(next)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	if err := target.Test(ctx); err != nil {
		writeError(w, http.StatusBadRequest, "connection test failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// adminRunAutoBackup serves POST /api/settings/auto-backup/run.
func (s *Server) adminRunAutoBackup(w http.ResponseWriter, r *http.Request) {
	if !s.autoBackupReady(w) {
		return
	}
	if s.backupJob == nil {
		writeError(w, http.StatusServiceUnavailable, "auto backup runner is not running")
		return
	}
	set, err := s.loadAutoBackup(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, sanitizeError(s.log, err, "load auto backup settings failed"))
		return
	}
	set.Enabled = true // a manual run needs a complete destination
	if err := set.Validate(); err != nil {
		writeAutoBackupError(w, err)
		return
	}
	if err := s.backupJob.RunNow(r.Context()); err != nil {
		if errors.Is(err, backupjob.ErrBusy) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, sanitizeError(s.log, err, "start backup failed"))
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"started": true})
}
