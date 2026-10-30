package gateway

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/mydisha/keirouter/backend/internal/store"
)

// chainStepInput is one route step in a chain create or update body. A step
// is priced by its market slug or by its own static price, never both.
type chainStepInput struct {
	Provider             string   `json:"provider"`
	Model                string   `json:"model"`
	MarketSlug           string   `json:"market_slug"`
	StaticInputPerM      float64  `json:"static_input_per_m"`
	StaticOutputPerM     float64  `json:"static_output_per_m"`
	StaticCacheReadPerM  *float64 `json:"static_cache_read_per_m"`
	StaticCacheWritePerM *float64 `json:"static_cache_write_per_m"`
	MarkupPercent        *float64 `json:"markup_percent"`
}

// toStore validates the step and converts it for storage. pos is the 0-based
// step index; the returned string is the 400 message, "" when valid.
func (in chainStepInput) toStore(chainID string, pos int, now time.Time) (store.ChainStep, string) {
	prefix := fmt.Sprintf("step %d: ", pos+1)
	st := store.ChainStep{
		ID: uuid.NewString(), ChainID: chainID, Position: pos,
		Provider: in.Provider, Model: in.Model, MarketSlug: strings.TrimSpace(in.MarketSlug),
		StaticInputPerM: in.StaticInputPerM, StaticOutputPerM: in.StaticOutputPerM,
		StaticCacheReadPerM: in.StaticCacheReadPerM, StaticCacheWritePerM: in.StaticCacheWritePerM,
		MarkupPercent: in.MarkupPercent, CreatedAt: now,
	}
	for _, r := range []*float64{&in.StaticInputPerM, &in.StaticOutputPerM, in.StaticCacheReadPerM, in.StaticCacheWritePerM} {
		if r != nil && !validRate(*r) {
			return st, prefix + "invalid static price: rates must be finite and non-negative"
		}
	}
	if !st.HasStaticPrice() {
		if in.StaticCacheReadPerM != nil || in.StaticCacheWritePerM != nil || in.MarkupPercent != nil {
			return st, prefix + "cache overrides and markup only apply to a static-price step"
		}
		return st, ""
	}
	if st.MarketSlug != "" {
		return st, prefix + "set a market slug or a static price, not both"
	}
	if in.StaticInputPerM <= 0 || in.StaticOutputPerM <= 0 {
		return st, prefix + "static input and output price must be greater than 0"
	}
	// The meter reads a zero cache rate as unset and bills the input rate, so
	// a zero override would bill more than the editor shows. Empty = derived.
	for _, r := range []*float64{in.StaticCacheReadPerM, in.StaticCacheWritePerM} {
		if r != nil && *r <= 0 {
			return st, prefix + "cache overrides must be greater than 0; leave them empty to derive them from the input price"
		}
	}
	if in.MarkupPercent != nil && !validMarkup(*in.MarkupPercent) {
		return st, prefix + "markup_percent must be between 0 and 1000"
	}
	return st, ""
}

// chainStepJSON is the admin list shape of one step. Nil pointers encode as
// null: derived cache rate, or markup inherited from the chain.
func chainStepJSON(st store.ChainStep) map[string]any {
	return map[string]any{
		"provider": st.Provider, "model": st.Model, "position": st.Position,
		"market_slug":              st.MarketSlug,
		"static_input_per_m":       st.StaticInputPerM,
		"static_output_per_m":      st.StaticOutputPerM,
		"static_cache_read_per_m":  st.StaticCacheReadPerM,
		"static_cache_write_per_m": st.StaticCacheWritePerM,
		"markup_percent":           st.MarkupPercent,
	}
}
