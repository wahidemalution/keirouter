package imagegen

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/mydisha/keirouter/backend/internal/core"
	"github.com/mydisha/keirouter/backend/internal/store"
)

const (
	maxDownloadBytes = 50 << 20
	downloadTimeout  = 60 * time.Second
	maxRedirects     = 3
)

// imageExt lists the stored image types; anything else is a lost image.
var imageExt = map[string]string{"image/png": "png", "image/jpeg": "jpg", "image/webp": "webp"}

func newDownloadClient() *http.Client {
	return &http.Client{
		Timeout: downloadTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > maxRedirects {
				return errors.New("too many redirects")
			}
			if req.URL.Scheme != "https" {
				return errors.New("redirect to a non-https url")
			}
			return nil
		},
	}
}

// ownerHash names an owner's image directory without exposing the subject.
func ownerHash(owner string) string {
	h := sha256.Sum256([]byte(owner))
	return hex.EncodeToString(h[:])[:32]
}

func (s *Service) imagesRoot() string { return filepath.Join(s.d.DataDir, "images") }

// saveAll stores each item under the owner's directory and counts the items
// that could not be stored.
func (s *Service) saveAll(ctx context.Context, owner string, items []core.ImageData) ([]store.ImageFile, int) {
	dir := filepath.Join(s.imagesRoot(), ownerHash(owner))
	var files []store.ImageFile
	lost := 0
	for i, it := range items {
		f, err := s.saveOne(ctx, dir, it)
		if err != nil {
			s.log.Warn("image item not stored", "item", i, "err", err)
			lost++
			continue
		}
		files = append(files, f)
	}
	return files, lost
}

func (s *Service) saveOne(ctx context.Context, dir string, it core.ImageData) (store.ImageFile, error) {
	data, err := s.itemBytes(ctx, it)
	if err != nil {
		return store.ImageFile{}, err
	}
	mime := http.DetectContentType(data)
	ext, ok := imageExt[mime]
	if !ok {
		return store.ImageFile{}, fmt.Errorf("unsupported content type %q", mime)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return store.ImageFile{}, err
	}
	id := uuid.NewString()
	path := filepath.Join(dir, id+"."+ext)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return store.ImageFile{}, err
	}
	_, werr := f.Write(data)
	cerr := f.Close()
	if err := errors.Join(werr, cerr); err != nil {
		_ = os.Remove(path)
		return store.ImageFile{}, err
	}
	return store.ImageFile{ID: id, Path: path, Mime: mime, Bytes: int64(len(data))}, nil
}

func (s *Service) itemBytes(ctx context.Context, it core.ImageData) ([]byte, error) {
	switch {
	case it.B64JSON != "":
		return decodeBase64(it.B64JSON)
	case strings.HasPrefix(it.URL, "data:"):
		meta, data, ok := strings.Cut(it.URL, ",")
		if !ok || !strings.HasSuffix(meta, ";base64") {
			return nil, errors.New("unsupported data url")
		}
		return decodeBase64(data)
	case it.URL != "":
		return s.download(ctx, it.URL)
	}
	return nil, errors.New("empty image item")
}

func decodeBase64(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if data, err := base64.StdEncoding.DecodeString(s); err == nil {
		return data, nil
	}
	return base64.RawStdEncoding.DecodeString(strings.TrimRight(s, "="))
}

// download fetches an https image of at most maxDownloadBytes.
func (s *Service) download(ctx context.Context, raw string) ([]byte, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" {
		return nil, errors.New("image url is not https")
	}
	ctx, cancel := context.WithTimeout(ctx, downloadTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("image download status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxDownloadBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxDownloadBytes {
		return nil, errors.New("image larger than 50 MB")
	}
	return data, nil
}

// removeFiles deletes stored images. Only paths inside the images directory
// are touched, whatever a row says.
func (s *Service) removeFiles(files []store.ImageFile) {
	root := s.imagesRoot() + string(filepath.Separator)
	for _, f := range files {
		p := filepath.Clean(f.Path)
		if !strings.HasPrefix(p, root) {
			s.log.Error("refusing to remove image outside the images directory", "file", f.ID)
			continue
		}
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			s.log.Warn("remove image file", "file", f.ID, "err", err)
		}
	}
}
