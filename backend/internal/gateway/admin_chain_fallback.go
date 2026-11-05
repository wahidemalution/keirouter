package gateway

import (
	"github.com/mydisha/keirouter/backend/internal/store"
)

// chainFallbackPriceInput is the admin body for a chain's final fallback
// price. Input and output are set together, or both left at 0 to use the
// models.dev price of the fallback model.
type chainFallbackPriceInput struct {
	InputPerM      float64  `json:"input_per_m"`
	OutputPerM     float64  `json:"output_per_m"`
	CacheReadPerM  *float64 `json:"cache_read_per_m"`
	CacheWritePerM *float64 `json:"cache_write_per_m"`
	// MarkupPercent null means no markup, never the global one.
	MarkupPercent *float64 `json:"markup_percent"`
}

// applyTo validates the input and writes it onto c. It returns the 400
// message, "" when valid.
func (in chainFallbackPriceInput) applyTo(c *store.Chain) string {
	for _, r := range []float64{in.InputPerM, in.OutputPerM} {
		if !validRate(r) {
			return "fallback price must be finite and non-negative"
		}
	}
	for _, r := range []*float64{in.CacheReadPerM, in.CacheWritePerM} {
		if r != nil && !validRate(*r) {
			return "fallback cache price must be null or finite and non-negative"
		}
	}
	if (in.InputPerM > 0) != (in.OutputPerM > 0) {
		return "fallback input and output price must be set together"
	}
	if in.MarkupPercent != nil && !validMarkup(*in.MarkupPercent) {
		return "fallback markup_percent must be null or between 0 and 1000"
	}
	c.FallbackInputPerM, c.FallbackOutputPerM = in.InputPerM, in.OutputPerM
	c.FallbackCacheReadPerM, c.FallbackCacheWritePerM = in.CacheReadPerM, in.CacheWritePerM
	c.FallbackMarkupPercent = in.MarkupPercent
	return ""
}

// chainFallbackPriceJSON is the stored fallback price plus the models.dev
// price used when none is stored.
func (s *Server) chainFallbackPriceJSON(c store.Chain) map[string]any {
	out := map[string]any{
		"input_per_m":       c.FallbackInputPerM,
		"output_per_m":      c.FallbackOutputPerM,
		"cache_read_per_m":  c.FallbackCacheReadPerM,
		"cache_write_per_m": c.FallbackCacheWritePerM,
		"markup_percent":    c.FallbackMarkupPercent,
		"auto":              nil,
	}
	if ref, ok := s.fallbackModelsDevPrice(c.FallbackModel); ok {
		out["auto"] = map[string]any{"input_per_m": ref.InputPerM, "output_per_m": ref.OutputPerM, "source": "models.dev"}
	}
	return out
}
