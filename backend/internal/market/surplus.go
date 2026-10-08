package market

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"time"
)

const (
	SurplusAPIURL = "https://api.surplusintelligence.ai/v1/prices"
	SurplusPrefix = "surplus:"
)

type surplusEnvelope struct {
	Models []struct {
		Model     string `json:"model"`
		Providers []struct {
			Pricing struct {
				Input      float64 `json:"input"`
				Output     float64 `json:"output"`
				CacheRead  float64 `json:"cacheRead"`
				CacheWrite float64 `json:"cacheWrite"`
			} `json:"pricing"`
		} `json:"providers"`
	} `json:"models"`
}

// ParseSurplus maps the /v1/prices matrix into Market models keyed
// "surplus:<model>", one row per model (the cheapest provider by output).
func ParseSurplus(r io.Reader) ([]Model, error) {
	var body surplusEnvelope
	if err := json.NewDecoder(r).Decode(&body); err != nil {
		return nil, err
	}
	out := make([]Model, 0, len(body.Models))
	for _, m := range body.Models {
		best := -1
		for i, p := range m.Providers {
			if p.Pricing.Output <= 0 {
				continue
			}
			if !validRate(p.Pricing.Input) || !validRate(p.Pricing.Output) {
				continue
			}
			if best == -1 ||
				p.Pricing.Output < m.Providers[best].Pricing.Output ||
				(p.Pricing.Output == m.Providers[best].Pricing.Output && p.Pricing.Input < m.Providers[best].Pricing.Input) {
				best = i
			}
		}
		if best == -1 {
			continue
		}
		pr := m.Providers[best].Pricing
		out = append(out, Model{
			Slug:       SurplusPrefix + m.Model,
			MinAskIn:   pr.Input,
			MinAskOut:  pr.Output,
			CacheRead:  math.Max(0, pr.CacheRead),
			CacheWrite: math.Max(0, pr.CacheWrite),
		})
	}
	return out, nil
}

// FetchSurplus downloads and parses the Surplus price matrix.
func FetchSurplus(ctx context.Context, url string) ([]Model, error) {
	if url == "" {
		url = SurplusAPIURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("surplus API status %d", resp.StatusCode)
	}
	return ParseSurplus(resp.Body)
}
