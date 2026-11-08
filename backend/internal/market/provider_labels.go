package market

import (
	"encoding/json"
	"io"
	"strings"
)

// ParseSurplusProviderLabels reads an order book (GET /api/markets/:model) and
// returns the display label Surplus publishes for each provider id, whatever
// the offer's state. Ids are lowercased; labels are cleaned for display.
func ParseSurplusProviderLabels(r io.Reader) (map[string]string, error) {
	var body trustedBookEnvelope
	if err := json.NewDecoder(r).Decode(&body); err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, o := range body.Offers {
		id := strings.ToLower(strings.TrimSpace(o.ProviderID))
		if id == "" {
			continue
		}
		var label string
		if json.Unmarshal(o.Provider, &label) != nil {
			continue
		}
		if label = cleanProviderLabel(label); label != "" {
			out[id] = label
		}
	}
	return out, nil
}

// cleanProviderLabel trims a label and swaps dashes for a plain hyphen, so
// "Alibaba DashScope — China" reads "Alibaba DashScope - China".
func cleanProviderLabel(s string) string {
	s = strings.NewReplacer("—", "-", "–", "-").Replace(s)
	return strings.Join(strings.Fields(s), " ")
}

// MergeProviderLabels adds or replaces provider labels. Labels outlive a
// snapshot Replace, so a provider with no offer right now keeps its name.
func (c *SnapshotCache) MergeProviderLabels(labels map[string]string) {
	if c == nil || len(labels) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.labels == nil {
		c.labels = map[string]string{}
	}
	for id, label := range labels {
		c.labels[strings.ToLower(id)] = label
	}
}

// ProviderLabel returns the Surplus display label for a provider id, or "".
func (c *SnapshotCache) ProviderLabel(id string) string {
	if c == nil {
		return ""
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.labels[strings.ToLower(strings.TrimSpace(id))]
}

// ProviderLabels returns a copy of every Surplus provider label seen so far,
// keyed by lowercased provider id.
func (c *SnapshotCache) ProviderLabels() map[string]string {
	if c == nil {
		return map[string]string{}
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make(map[string]string, len(c.labels))
	for id, label := range c.labels {
		out[id] = label
	}
	return out
}
