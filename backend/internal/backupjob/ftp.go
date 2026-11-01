package backupjob

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/jlaffaye/ftp"

	"github.com/mydisha/keirouter/backend/internal/httputil"
)

type ftpTarget struct {
	conf   FTPSettings
	folder string // "", "a/b" (relative to the login dir) or "/a/b"
}

// NewFTP returns an FTP target, with explicit TLS (FTPS) unless conf.TLS is
// "none". Every call opens its own connection.
func NewFTP(conf FTPSettings) Target {
	folder := strings.TrimSuffix(NormalizePrefix(conf.Folder), "/")
	if folder != "" && strings.HasPrefix(strings.TrimSpace(conf.Folder), "/") {
		folder = "/" + folder
	}
	return &ftpTarget{conf: conf, folder: folder}
}

func (t *ftpTarget) connect(ctx context.Context) (*ftp.ServerConn, func() bool, error) {
	if err := httputil.ValidateHost(t.conf.Host); err != nil {
		return nil, nil, fmt.Errorf("ftp: %w", err)
	}
	port := t.conf.Port
	if port == 0 {
		port = 21
	}
	opts := []ftp.DialOption{
		ftp.DialWithContext(ctx),
		ftp.DialWithDialer(*httputil.SafeDialer(30 * time.Second)),
		ftp.DialWithTimeout(30 * time.Second),
		ftp.DialWithShutTimeout(30 * time.Second),
	}
	if t.conf.TLS != TLSNone {
		opts = append(opts, ftp.DialWithExplicitTLS(&tls.Config{ServerName: t.conf.Host, MinVersion: tls.VersionTLS12}))
	}
	c, err := ftp.Dial(net.JoinHostPort(t.conf.Host, strconv.Itoa(port)), opts...)
	if err != nil {
		return nil, nil, fmt.Errorf("ftp: connect: %w", err)
	}
	if err := c.Login(t.conf.Username, t.conf.Password); err != nil {
		_ = c.Quit()
		return nil, nil, fmt.Errorf("ftp: login: %w", err)
	}
	// Abort a blocked call when the run is cancelled.
	stop := context.AfterFunc(ctx, func() { _ = c.Quit() })
	return c, stop, nil
}

func (t *ftpTarget) path(name string) string {
	if t.folder == "" {
		return name
	}
	return t.folder + "/" + name
}

// with runs fn on a fresh connection and always closes it.
func (t *ftpTarget) with(ctx context.Context, fn func(*ftp.ServerConn) error) error {
	c, stop, err := t.connect(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if stop() {
			_ = c.Quit()
		}
	}()
	if err := fn(c); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	return nil
}

func (t *ftpTarget) Put(ctx context.Context, name string, r io.Reader, size int64) error {
	part := t.path(name + ".part")
	err := t.with(ctx, func(c *ftp.ServerConn) error {
		if err := c.Stor(part, r); err != nil {
			return fmt.Errorf("ftp: upload: %w", err)
		}
		if err := c.Rename(part, t.path(name)); err != nil {
			return fmt.Errorf("ftp: rename: %w", err)
		}
		return nil
	})
	if err != nil {
		// Rotation ignores .part files, so a partial upload would stay
		// forever. Remove it on a fresh connection, even after a cancel.
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		_ = t.with(cctx, func(c *ftp.ServerConn) error { return c.Delete(part) })
	}
	return err
}

func (t *ftpTarget) Size(ctx context.Context, name string) (int64, error) {
	var n int64
	err := t.with(ctx, func(c *ftp.ServerConn) error {
		var err error
		n, err = c.FileSize(t.path(name))
		if err != nil {
			return fmt.Errorf("ftp: size: %w", err)
		}
		return nil
	})
	return n, err
}

func (t *ftpTarget) List(ctx context.Context) ([]string, error) {
	var names []string
	err := t.with(ctx, func(c *ftp.ServerConn) error {
		dir := t.folder
		if dir == "" {
			dir = "."
		}
		entries, err := c.NameList(dir)
		if err != nil {
			return fmt.Errorf("ftp: list: %w", err)
		}
		for _, e := range entries {
			if b := path.Base(e); b != "." && b != ".." && b != "/" {
				names = append(names, b)
			}
		}
		return nil
	})
	return names, err
}

func (t *ftpTarget) Delete(ctx context.Context, name string) error {
	return t.with(ctx, func(c *ftp.ServerConn) error {
		if err := c.Delete(t.path(name)); err != nil {
			return fmt.Errorf("ftp: delete: %w", err)
		}
		return nil
	})
}

// Test creates the folder if missing, then writes and deletes a probe.
func (t *ftpTarget) Test(ctx context.Context) error {
	return t.with(ctx, func(c *ftp.ServerConn) error {
		if t.folder != "" {
			cur := ""
			if strings.HasPrefix(t.folder, "/") {
				cur = "/"
			}
			for _, seg := range strings.Split(strings.TrimPrefix(t.folder, "/"), "/") {
				cur = path.Join(cur, seg)
				_ = c.MakeDir(cur) // already exists is fine; Stor below proves access
			}
		}
		name := t.path(probeName())
		if err := c.Stor(name, strings.NewReader(probeBody)); err != nil {
			return fmt.Errorf("ftp: write test file: %w", err)
		}
		if err := c.Delete(name); err != nil {
			return fmt.Errorf("ftp: delete test file: %w", err)
		}
		return nil
	})
}
