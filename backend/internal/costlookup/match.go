package costlookup

import (
	"github.com/mydisha/keirouter/backend/internal/store"
)

// fingerprintWindow is how far apart (seconds) a row and a log entry may be
// created and still match on model and token counts.
const fingerprintWindow = 10

// Pair is a usage row and the log entry that priced it.
type Pair struct {
	Row  store.ReconcileRow
	Item LogItem
}

// Match pairs rows with log entries; each entry is used at most once. A row
// carrying the provider's request id matches only the entry with that id. A
// row without one matches the nearest unused entry with the same model and
// token counts created within fingerprintWindow seconds, rows taken in the
// order given (oldest first). Entries left over are returned as unmatched.
func Match(rows []store.ReconcileRow, items []LogItem) (pairs []Pair, unmatched []LogItem) {
	used := make([]bool, len(items))
	byID := make(map[string]int, len(items))
	for i, it := range items {
		if it.RequestID != "" {
			byID[it.RequestID] = i
		}
	}
	matched := make([]bool, len(rows))
	for ri, r := range rows {
		if r.UpstreamRequestID == "" {
			continue
		}
		if i, ok := byID[r.UpstreamRequestID]; ok && !used[i] {
			used[i] = true
			matched[ri] = true
			pairs = append(pairs, Pair{Row: r, Item: items[i]})
		}
	}
	for ri, r := range rows {
		if matched[ri] || r.UpstreamRequestID != "" {
			continue
		}
		best, bestDist := -1, int64(fingerprintWindow+1)
		for i, it := range items {
			if used[i] || it.ModelName != r.Model || it.PromptTokens != r.PromptTokens ||
				it.CompletionTokens != r.CompletionTokens {
				continue
			}
			d := it.CreatedAt - r.CreatedAt.Unix()
			if d < 0 {
				d = -d
			}
			if d <= fingerprintWindow && d < bestDist {
				best, bestDist = i, d
			}
		}
		if best >= 0 {
			used[best] = true
			pairs = append(pairs, Pair{Row: r, Item: items[best]})
		}
	}
	for i, it := range items {
		if !used[i] {
			unmatched = append(unmatched, it)
		}
	}
	return pairs, unmatched
}
