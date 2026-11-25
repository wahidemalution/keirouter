package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Image job statuses.
const (
	ImageJobQueued    = "queued"
	ImageJobRunning   = "running"
	ImageJobSucceeded = "succeeded"
	ImageJobFailed    = "failed"
)

// ErrImageJobNotActive means the job is not queued or running, so it cannot be finished.
var ErrImageJobNotActive = errors.New("store: image job is not active")

// ErrImageJobActive means the job is still queued or running, so it cannot be deleted.
var ErrImageJobActive = errors.New("store: image job is still active")

// imageRetention is how long a finished job's files stay available.
const imageRetention = 24 * time.Hour

// ImageJob is one portal image generation request, owned by a portal user.
type ImageJob struct {
	ID, OwnerSub, KeyID, TenantID, ProjectID, ChainID, Prompt, Params, Status, Error string
	ImageCount                                                                       int
	CostMicro, UpstreamCostMicro                                                     int64
	CreatedAt                                                                        time.Time
	FinishedAt, ExpiresAt                                                            *time.Time
	Files                                                                            []ImageFile
}

// ImageFile is one generated image stored on disk under Path.
type ImageFile struct {
	ID, JobID, Path, Mime string
	Bytes                 int64
}

// ImageJobsRepo persists image_jobs and image_files. Every owner-scoped method
// filters by owner_sub in SQL and reports another owner's id as ErrNotFound.
type ImageJobsRepo struct {
	db *DB
	// Now supplies the current time for expiry checks; nil means time.Now.
	Now func() time.Time
}

// ImageJobs returns the image job repository.
func (db *DB) ImageJobs() *ImageJobsRepo { return &ImageJobsRepo{db: db} }

func (r *ImageJobsRepo) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

const imageJobCols = `id, owner_sub, key_id, tenant_id, project_id, chain_id, prompt, params, status, error, image_count, cost_micro, upstream_cost_micro, created_at, finished_at, expires_at`

const imageFileCols = `id, job_id, path, mime, bytes`

// Create inserts a job (the ID is chosen by the caller).
func (r *ImageJobsRepo) Create(ctx context.Context, j ImageJob) error {
	q := r.db.rebind(`INSERT INTO image_jobs (` + imageJobCols + `) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	_, err := r.db.sql.ExecContext(ctx, q, j.ID, j.OwnerSub, j.KeyID, j.TenantID, j.ProjectID, j.ChainID,
		j.Prompt, j.Params, j.Status, j.Error, j.ImageCount, j.CostMicro, j.UpstreamCostMicro,
		formatTime(j.CreatedAt), nullTime(j.FinishedAt), nullTime(j.ExpiresAt))
	if err != nil {
		if isUniqueViolation(err) {
			return ErrAlreadyExists
		}
		return fmt.Errorf("store: create image job: %w", err)
	}
	return nil
}

type rowScanner interface{ Scan(dest ...any) error }

func scanImageJob(s rowScanner) (ImageJob, error) {
	var j ImageJob
	var created string
	var fin, exp sql.NullString
	if err := s.Scan(&j.ID, &j.OwnerSub, &j.KeyID, &j.TenantID, &j.ProjectID, &j.ChainID, &j.Prompt, &j.Params,
		&j.Status, &j.Error, &j.ImageCount, &j.CostMicro, &j.UpstreamCostMicro, &created, &fin, &exp); err != nil {
		return ImageJob{}, err
	}
	j.CreatedAt = parseTime(created)
	if fin.Valid && fin.String != "" {
		t := parseTime(fin.String)
		j.FinishedAt = &t
	}
	if exp.Valid && exp.String != "" {
		t := parseTime(exp.String)
		j.ExpiresAt = &t
	}
	return j, nil
}

// Get returns one of the owner's jobs with its files. Another owner's id is ErrNotFound.
func (r *ImageJobsRepo) Get(ctx context.Context, ownerSub, id string) (ImageJob, error) {
	q := r.db.rebind(`SELECT ` + imageJobCols + ` FROM image_jobs WHERE owner_sub = ? AND id = ?`)
	j, err := scanImageJob(r.db.sql.QueryRowContext(ctx, q, ownerSub, id))
	if errors.Is(err, sql.ErrNoRows) {
		return ImageJob{}, ErrNotFound
	}
	if err != nil {
		return ImageJob{}, fmt.Errorf("store: get image job: %w", err)
	}
	files, err := r.filesFor(ctx, []string{j.ID})
	if err != nil {
		return ImageJob{}, err
	}
	j.Files = files[j.ID]
	return j, nil
}

// ListByOwner returns the owner's newest jobs first, with files filled.
func (r *ImageJobsRepo) ListByOwner(ctx context.Context, ownerSub string, limit int) ([]ImageJob, error) {
	if limit <= 0 {
		limit = 50
	}
	q := r.db.rebind(`SELECT ` + imageJobCols + ` FROM image_jobs WHERE owner_sub = ? ORDER BY created_at DESC, id DESC LIMIT ?`)
	return r.listJobs(ctx, q, ownerSub, limit)
}

// ListExpired returns jobs whose expires_at is before now.
func (r *ImageJobsRepo) ListExpired(ctx context.Context, now time.Time) ([]ImageJob, error) {
	q := r.db.rebind(`SELECT ` + imageJobCols + ` FROM image_jobs WHERE expires_at IS NOT NULL AND expires_at < ? ORDER BY expires_at`)
	return r.listJobs(ctx, q, formatTime(now))
}

func (r *ImageJobsRepo) listJobs(ctx context.Context, q string, args ...any) ([]ImageJob, error) {
	rows, err := r.db.sql.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list image jobs: %w", err)
	}
	var out []ImageJob
	for rows.Next() {
		j, err := scanImageJob(rows)
		if err != nil {
			rows.Close()
			return nil, fmt.Errorf("store: scan image job: %w", err)
		}
		out = append(out, j)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(out))
	for i := range out {
		ids[i] = out[i].ID
	}
	files, err := r.filesFor(ctx, ids)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Files = files[out[i].ID]
	}
	return out, nil
}

func (r *ImageJobsRepo) filesFor(ctx context.Context, jobIDs []string) (map[string][]ImageFile, error) {
	out := map[string][]ImageFile{}
	const chunk = 500
	for start := 0; start < len(jobIDs); start += chunk {
		end := start + chunk
		if end > len(jobIDs) {
			end = len(jobIDs)
		}
		ids := jobIDs[start:end]
		args := make([]any, len(ids))
		marks := make([]byte, 0, len(ids)*2)
		for i, id := range ids {
			args[i] = id
			if i > 0 {
				marks = append(marks, ',')
			}
			marks = append(marks, '?')
		}
		q := r.db.rebind(`SELECT ` + imageFileCols + ` FROM image_files WHERE job_id IN (` + string(marks) + `) ORDER BY id`)
		rows, err := r.db.sql.QueryContext(ctx, q, args...)
		if err != nil {
			return nil, fmt.Errorf("store: list image files: %w", err)
		}
		for rows.Next() {
			var f ImageFile
			if err := rows.Scan(&f.ID, &f.JobID, &f.Path, &f.Mime, &f.Bytes); err != nil {
				rows.Close()
				return nil, fmt.Errorf("store: scan image file: %w", err)
			}
			out[f.JobID] = append(out[f.JobID], f)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// CountActive counts the owner's queued and running jobs.
func (r *ImageJobsRepo) CountActive(ctx context.Context, ownerSub string) (int, error) {
	q := r.db.rebind(`SELECT COUNT(*) FROM image_jobs WHERE owner_sub = ? AND status IN (?, ?)`)
	var n int
	if err := r.db.sql.QueryRowContext(ctx, q, ownerSub, ImageJobQueued, ImageJobRunning).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: count active image jobs: %w", err)
	}
	return n, nil
}

// SetRunning marks a queued job as running. ErrNotFound when the job is gone or not queued.
func (r *ImageJobsRepo) SetRunning(ctx context.Context, id string) error {
	q := r.db.rebind(`UPDATE image_jobs SET status = ? WHERE id = ? AND status = ?`)
	res, err := r.db.sql.ExecContext(ctx, q, ImageJobRunning, id, ImageJobQueued)
	if err != nil {
		return fmt.Errorf("store: set image job running: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Finish records the outcome and its files; the job expires 24h after finishedAt.
// Only a queued or running job can finish (status succeeded or failed); any
// other job yields ErrImageJobNotActive and no rows change.
func (r *ImageJobsRepo) Finish(ctx context.Context, id, status, errMsg string, count int, cost, upstream int64, files []ImageFile, finishedAt time.Time) error {
	if status != ImageJobSucceeded && status != ImageJobFailed {
		return fmt.Errorf("store: finish image job: invalid status %q", status)
	}
	tx, err := r.db.sql.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: finish image job: %w", err)
	}
	defer tx.Rollback()
	q := r.db.rebind(`UPDATE image_jobs SET status = ?, error = ?, image_count = ?, cost_micro = ?, upstream_cost_micro = ?, finished_at = ?, expires_at = ? WHERE id = ? AND status IN (?, ?)`)
	res, err := tx.ExecContext(ctx, q, status, errMsg, count, cost, upstream, formatTime(finishedAt), formatTime(finishedAt.Add(imageRetention)), id, ImageJobQueued, ImageJobRunning)
	if err != nil {
		return fmt.Errorf("store: finish image job: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrImageJobNotActive
	}
	fq := r.db.rebind(`INSERT INTO image_files (` + imageFileCols + `) VALUES (?, ?, ?, ?, ?)`)
	for _, f := range files {
		if _, err := tx.ExecContext(ctx, fq, f.ID, id, f.Path, f.Mime, f.Bytes); err != nil {
			return fmt.Errorf("store: insert image file: %w", err)
		}
	}
	return tx.Commit()
}

// FinishSucceededBilled marks a running job succeeded with its files and
// inserts its usage row in one transaction, so a job is billed exactly when
// it is shown as succeeded. The count and costs come from usage. A job that
// is not running yields ErrImageJobNotActive and no rows change.
func (r *ImageJobsRepo) FinishSucceededBilled(ctx context.Context, id string, files []ImageFile, usage UsageRecord, finishedAt time.Time) error {
	tx, err := r.db.sql.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: finish billed image job: %w", err)
	}
	defer tx.Rollback()
	cost := usage.CostMicros
	if cost == 0 && usage.CostNanos != 0 {
		cost = (usage.CostNanos + 500) / 1000
	}
	upstream := usage.UpstreamCostMicros
	if upstream == 0 && usage.UpstreamCostNanos != 0 {
		upstream = (usage.UpstreamCostNanos + 500) / 1000
	}
	q := r.db.rebind(`UPDATE image_jobs SET status = ?, error = '', image_count = ?, cost_micro = ?, upstream_cost_micro = ?, finished_at = ?, expires_at = ? WHERE id = ? AND status = ?`)
	res, err := tx.ExecContext(ctx, q, ImageJobSucceeded, usage.ImageCount, cost, upstream,
		formatTime(finishedAt), formatTime(finishedAt.Add(imageRetention)), id, ImageJobRunning)
	if err != nil {
		return fmt.Errorf("store: finish billed image job: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrImageJobNotActive
	}
	fq := r.db.rebind(`INSERT INTO image_files (` + imageFileCols + `) VALUES (?, ?, ?, ?, ?)`)
	for _, f := range files {
		if _, err := tx.ExecContext(ctx, fq, f.ID, id, f.Path, f.Mime, f.Bytes); err != nil {
			return fmt.Errorf("store: insert image file: %w", err)
		}
	}
	if err := insertUsage(ctx, tx, r.db.rebind, usage); err != nil {
		return fmt.Errorf("store: record image usage: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: finish billed image job: %w", err)
	}
	return nil
}

// GetFile returns a file only when its job belongs to the owner and has not expired.
func (r *ImageJobsRepo) GetFile(ctx context.Context, ownerSub, fileID string) (ImageFile, error) {
	q := r.db.rebind(`SELECT f.id, f.job_id, f.path, f.mime, f.bytes FROM image_files f
		JOIN image_jobs j ON j.id = f.job_id
		WHERE f.id = ? AND j.owner_sub = ? AND j.expires_at IS NOT NULL AND j.expires_at > ?`)
	var f ImageFile
	err := r.db.sql.QueryRowContext(ctx, q, fileID, ownerSub, formatTime(r.now())).Scan(&f.ID, &f.JobID, &f.Path, &f.Mime, &f.Bytes)
	if errors.Is(err, sql.ErrNoRows) {
		return ImageFile{}, ErrNotFound
	}
	if err != nil {
		return ImageFile{}, fmt.Errorf("store: get image file: %w", err)
	}
	return f, nil
}

// Delete removes one of the owner's jobs and returns its files so the caller
// can delete them from disk. Another owner's id is ErrNotFound; a queued or
// running job is ErrImageJobActive and stays untouched.
func (r *ImageJobsRepo) Delete(ctx context.Context, ownerSub, id string) ([]ImageFile, error) {
	tx, err := r.db.sql.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("store: delete image job: %w", err)
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, r.db.rebind(`DELETE FROM image_jobs WHERE owner_sub = ? AND id = ? AND status NOT IN (?, ?)`),
		ownerSub, id, ImageJobQueued, ImageJobRunning)
	if err != nil {
		return nil, fmt.Errorf("store: delete image job: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		var one int
		err := tx.QueryRowContext(ctx, r.db.rebind(`SELECT 1 FROM image_jobs WHERE owner_sub = ? AND id = ?`), ownerSub, id).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		if err != nil {
			return nil, fmt.Errorf("store: delete image job: %w", err)
		}
		return nil, ErrImageJobActive
	}
	rows, err := tx.QueryContext(ctx, r.db.rebind(`SELECT `+imageFileCols+` FROM image_files WHERE job_id = ? ORDER BY id`), id)
	if err != nil {
		return nil, fmt.Errorf("store: delete image job: %w", err)
	}
	var files []ImageFile
	for rows.Next() {
		var f ImageFile
		if err := rows.Scan(&f.ID, &f.JobID, &f.Path, &f.Mime, &f.Bytes); err != nil {
			rows.Close()
			return nil, err
		}
		files = append(files, f)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, r.db.rebind(`DELETE FROM image_files WHERE job_id = ?`), id); err != nil {
		return nil, fmt.Errorf("store: delete image files: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return files, nil
}

// FailStuck marks failed the queued or running jobs created before
// createdBefore: jobs no worker will finish.
func (r *ImageJobsRepo) FailStuck(ctx context.Context, createdBefore time.Time, msg string) (int, error) {
	q := r.db.rebind(`UPDATE image_jobs SET status = ?, error = ?, finished_at = ?, expires_at = ? WHERE status IN (?, ?) AND created_at < ?`)
	now := r.now()
	res, err := r.db.sql.ExecContext(ctx, q, ImageJobFailed, msg, formatTime(now), formatTime(now.Add(imageRetention)),
		ImageJobQueued, ImageJobRunning, formatTime(createdBefore))
	if err != nil {
		return 0, fmt.Errorf("store: fail stuck image jobs: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// FailStale marks every queued or running job failed (jobs orphaned by a restart).
func (r *ImageJobsRepo) FailStale(ctx context.Context, msg string) (int, error) {
	q := r.db.rebind(`UPDATE image_jobs SET status = ?, error = ?, finished_at = ?, expires_at = ? WHERE status IN (?, ?)`)
	now := r.now()
	res, err := r.db.sql.ExecContext(ctx, q, ImageJobFailed, msg, formatTime(now), formatTime(now.Add(imageRetention)), ImageJobQueued, ImageJobRunning)
	if err != nil {
		return 0, fmt.Errorf("store: fail stale image jobs: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}
