package imagegen

import (
	"context"
	"errors"
	"time"

	"github.com/mydisha/keirouter/backend/internal/store"
)

const defaultSweepEvery = 10 * time.Minute

// Sweep deletes jobs whose retention ended before now, files first: a failed
// row delete is retried on the next sweep, and an expired row is never served.
func (s *Service) Sweep(ctx context.Context, now time.Time) error {
	jobs, err := s.d.Jobs.ListExpired(ctx, now)
	if err != nil {
		return err
	}
	var errs []error
	for _, j := range jobs {
		s.removeFiles(j.Files)
		if _, err := s.d.Jobs.Delete(ctx, j.OwnerSub, j.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
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
