package backupjob

import (
	"context"
	"io"

	"golang.org/x/time/rate"
)

const limiterBurst = 32 << 10

type limitedReader struct {
	ctx context.Context
	r   io.Reader
	lim *rate.Limiter
}

// NewLimitedReader caps reads from r at limitKBps (1 KB = 1024 bytes). A
// limit of 0 returns r unchanged.
func NewLimitedReader(ctx context.Context, r io.Reader, limitKBps int) io.Reader {
	if limitKBps <= 0 {
		return r
	}
	return &limitedReader{ctx: ctx, r: r, lim: rate.NewLimiter(rate.Limit(limitKBps*1024), limiterBurst)}
}

func (l *limitedReader) Read(p []byte) (int, error) {
	if len(p) > limiterBurst {
		p = p[:limiterBurst]
	}
	n, err := l.r.Read(p)
	if n > 0 {
		if werr := l.lim.WaitN(l.ctx, n); werr != nil {
			return n, werr
		}
	}
	return n, err
}
