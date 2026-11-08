package gateway

import (
	"bytes"
	"context"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/mydisha/keirouter/backend/internal/market"
)

const (
	surplusLabelsTTL        = 10 * time.Minute
	surplusLabelsRetryAfter = time.Minute
	// surplusLabelsMaxModels bounds the cache: the model comes from the query.
	surplusLabelsMaxModels = 256
)

var surplusModelName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

type surplusProviderLabel struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type cachedLabels struct {
	labels map[string]string
	at     time.Time
}

// surplusLabelCache holds the provider ids and names each model's Surplus
// order book lists, so the chain editor can name steps without refetching
// (Surplus rate-limits /api/markets per IP).
type surplusLabelCache struct {
	mu      sync.Mutex
	entries map[string]cachedLabels
	failed  map[string]time.Time
	sf      singleflight.Group
	now     func() time.Time // tests override it
}

func (c *surplusLabelCache) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// get returns the labels for model, fetching when missing or stale. A failed
// fetch is not retried for surplusLabelsRetryAfter; a stale entry is served
// meanwhile.
func (c *surplusLabelCache) get(model string, fetch func() (map[string]string, error)) (map[string]string, bool) {
	c.mu.Lock()
	e, ok := c.entries[model]
	failedAt, failed := c.failed[model]
	cooling := failed && c.clock().Sub(failedAt) < surplusLabelsRetryAfter
	c.mu.Unlock()
	if ok && (cooling || c.clock().Sub(e.at) < surplusLabelsTTL) {
		return e.labels, true
	}
	if cooling {
		return nil, false
	}
	v, _, _ := c.sf.Do(model, func() (any, error) {
		labels, err := fetch()
		c.mu.Lock()
		defer c.mu.Unlock()
		if err != nil {
			if c.failed == nil {
				c.failed = map[string]time.Time{}
			}
			c.failed[model] = c.clock()
			if e, ok := c.entries[model]; ok {
				return e.labels, nil
			}
			return nil, err
		}
		delete(c.failed, model)
		if c.entries == nil {
			c.entries = map[string]cachedLabels{}
		}
		if _, ok := c.entries[model]; !ok && len(c.entries) >= surplusLabelsMaxModels {
			c.evictOldest()
		}
		c.entries[model] = cachedLabels{labels: labels, at: c.clock()}
		return labels, nil
	})
	labels, ok := v.(map[string]string)
	return labels, ok
}

// evictOldest drops the least recently fetched entry. Callers hold c.mu.
func (c *surplusLabelCache) evictOldest() {
	var oldest string
	var at time.Time
	for model, e := range c.entries {
		if oldest == "" || e.at.Before(at) {
			oldest, at = model, e.at
		}
	}
	delete(c.entries, oldest)
}

// adminSurplusProviders lists the Surplus provider ids and names for a model,
// read from its order book. When the book cannot be read it falls back to
// every label seen by the market sync and reports complete=false.
func (s *Server) adminSurplusProviders(w http.ResponseWriter, r *http.Request) {
	model := strings.TrimSpace(r.URL.Query().Get("model"))
	if !surplusModelName.MatchString(model) {
		writeError(w, http.StatusBadRequest, "model must be a Surplus model name")
		return
	}
	// The shared fetch outlives a cancelled request so the cache still fills.
	fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 10*time.Second)
	defer cancel()
	labels, ok := s.surplusLabels.get(model, func() (map[string]string, error) {
		body, err := market.FetchSurplusBookRaw(fetchCtx, s.surplusURL, model)
		if err != nil {
			return nil, err
		}
		return market.ParseSurplusProviderLabels(bytes.NewReader(body))
	})
	if ok {
		s.marketCache.MergeProviderLabels(labels)
	} else {
		labels = s.marketCache.ProviderLabels()
	}
	out := make([]surplusProviderLabel, 0, len(labels))
	for id, name := range labels {
		out = append(out, surplusProviderLabel{ID: id, Name: name})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	writeJSON(w, http.StatusOK, map[string]any{"providers": out, "complete": ok})
}
