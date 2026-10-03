package market

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const StreamURL = "https://inferhub.dev/api/market/stream"

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

func ReadStream(r io.Reader, onSnapshot func([]Model) error) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" {
			continue
		}
		models, err := ParseSnapshot(strings.NewReader(payload))
		if err != nil {
			continue
		}
		if onSnapshot != nil {
			if err := onSnapshot(models); err != nil {
				return err
			}
		}
	}
	return sc.Err()
}