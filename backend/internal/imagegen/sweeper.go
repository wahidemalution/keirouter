package imagegen

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/mydisha/keirouter/backend/internal/store"
)

const (
	defaultSweepEvery = 10 * time.Minute
	// stuckAfter is how long a job may stay queued or running. A worker
	// gives up well before: 45 minutes in the queue plus the 5-minute job
	// timeout. Billing commits only with the succeeded state, so a job
	// failed here is unbilled even if a worker still holds it.
	stuckAfter = 55 * time.Minute
	// orphanAfter is the age at which a stored file no job serves any more
	// is removed. Retention is 24h after a job finishes.
	orphanAfter = 26 * time.Hour
)

// Sweep deletes jobs whose retention ended before now, files first: a failed
// row delete is retried on the next sweep, and an expired row is never served.
// It also fails stuck jobs and removes orphaned files.
func (s *Service) Sweep(ctx context.Context, now time.Time) error {
	var errs []error
	if n, err := s.d.Jobs.FailStuck(ctx, now.Add(-stuckAfter), msgGenericFail); err != nil {
		errs = append(errs, err)
	} else if n > 0 {
		s.log.Warn("failed stuck image jobs", "count", n)
	}
	jobs, err := s.d.Jobs.ListExpired(ctx, now)
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	for _, j := range jobs {
		s.removeFiles(j.Files)
		if _, err := s.d.Jobs.Delete(ctx, j.OwnerSub, j.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
			errs = append(errs, err)
		}
	}
	s.sweepOrphans(now)
	return errors.Join(errs...)
}

// sweepOrphans removes regular files older than orphanAfter from the owner
// directories under the images root. Every file a live job serves is younger
// than that, so an older one is an orphan: written for a job whose billing
// never committed, or left by a crash. Symlinks are never followed.
func (s *Service) sweepOrphans(now time.Time) {
	root := s.imagesRoot()
	owners, err := os.ReadDir(root)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			s.log.Warn("list image owner directories", "err", err)
		}
		return
	}
	cutoff := now.Add(-orphanAfter)
	for _, o := range owners {
		if !o.IsDir() || o.Type()&fs.ModeSymlink != 0 {
			continue
		}
		dir := filepath.Join(root, o.Name())
		files, err := os.ReadDir(dir)
		if err != nil {
			s.log.Warn("list image files", "err", err)
			continue
		}
		for _, f := range files {
			if !f.Type().IsRegular() {
				continue
			}
			info, err := f.Info()
			if err != nil || !info.ModTime().Before(cutoff) {
				continue
			}
			if err := os.Remove(filepath.Join(dir, f.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
				s.log.Warn("remove orphan image file", "err", err)
			}
		}
	}
}

// Delete removes one of the owner's finished jobs, then its files. Another
// owner's job is store.ErrNotFound; a queued or running job is
// store.ErrImageJobActive and stays untouched.
func (s *Service) Delete(ctx context.Context, ownerSub, id string) error {
	files, err := s.d.Jobs.Delete(ctx, ownerSub, id)
	if err != nil {
		return err
	}
	s.removeFiles(files)
	return nil
}

// RunSweeper sweeps now and then every interval until ctx is done.
func (s *Service) RunSweeper(ctx context.Context, every time.Duration) {
	if every <= 0 {
		every = defaultSweepEvery
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if err := s.Sweep(ctx, s.now()); err != nil {
			s.log.Warn("sweep expired image jobs", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
