package market

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const APIURL = "https://inferhub.dev/api/market"

type Model struct {
	Slug      string  `json:"slug"`
	Family    string  `json:"family"`
	MinAskIn  float64 `json:"minAskIn"`
	MinAskOut float64 `json:"minAskOut"`
	MaxAskIn  float64 `json:"maxAskIn"`
	MaxAskOut float64 `json:"maxAskOut"`
	LastRate  float64 `json:"lastRate"`
}

func ParseSnapshot(r io.Reader) ([]Model, error) {
	var body struct {
		Models []Model `json:"models"`
	}
	if err := json.NewDecoder(r).Decode(&body); err != nil {
		return nil, err
	}
	return body.Models, nil
}

// Fetch downloads the market snapshot in one request.
func Fetch(ctx context.Context, url string) ([]Model, error) {
	if url == "" {
		url = APIURL
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
		return nil, fmt.Errorf("market API status %d", resp.StatusCode)
	}
	return ParseSnapshot(resp.Body)
}
