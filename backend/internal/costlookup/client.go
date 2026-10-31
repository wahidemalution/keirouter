// Package costlookup reconciles provisional bills with the cost a provider
// reports in its own usage log (Subrouter, a new-api marketplace).
package costlookup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// DefaultBaseURL is the Subrouter origin serving the usage log API.
const DefaultBaseURL = "https://subrouter.ai"

// QuotaNanos converts Subrouter quota to USD nanos: USD = quota / 500000.
const QuotaNanos = 2000

var (
	// ErrUnauthorized means Subrouter rejected the access token (401/403).
	ErrUnauthorized = errors.New("costlookup: access token rejected")
	// ErrTransient covers failures worth retrying later: rate limits,
	// server errors, timeouts, malformed or unsuccessful responses.
	ErrTransient = errors.New("costlookup: temporary failure")
)

// Client reads one Subrouter account's usage log with a system access token.
type Client struct {
	BaseURL   string
	Token     string
	UserID    string
	TokenName string
	HTTP      *http.Client
}

// LogQuery selects one page of consume entries.
type LogQuery struct {
	Cursor     string
	Start, End int64
	PageSize   int
}

// LogItem is one consume entry of the usage log.
type LogItem struct {
	CreatedAt        int64  `json:"created_at"`
	IsStream         bool   `json:"is_stream"`
	Quota            int64  `json:"quota"`
	PromptTokens     int    `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
	ModelName        string `json:"model_name"`
	RequestID        string `json:"request_id"`
}

// Page is one page of the usage log, newest entries first.
type Page struct {
	Items      []LogItem
	HasMore    bool
	NextCursor string
}

func (c *Client) base() string {
	b := strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	if b == "" {
		return DefaultBaseURL
	}
	return b
}

func (c *Client) logsURL() string { return c.base() + "/api/log/self" }

// Logs fetches one page of consume (type=2) entries.
func (c *Client) Logs(ctx context.Context, q LogQuery) (Page, error) {
	v := url.Values{}
	v.Set("type", "2")
	v.Set("cursor", q.Cursor)
	v.Set("page_size", strconv.Itoa(q.PageSize))
	if q.Start > 0 {
		v.Set("start_timestamp", strconv.FormatInt(q.Start, 10))
	}
	if q.End > 0 {
		v.Set("end_timestamp", strconv.FormatInt(q.End, 10))
	}
	if c.TokenName != "" {
		v.Set("token_name", c.TokenName)
	}
	var body struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
		Data    struct {
			Items      []LogItem `json:"items"`
			HasMore    bool      `json:"has_more"`
			NextCursor string    `json:"next_cursor"`
		} `json:"data"`
	}
	if err := c.get(ctx, c.logsURL()+"?"+v.Encode(), &body); err != nil {
		return Page{}, err
	}
	if !body.Success {
		return Page{}, fmt.Errorf("%w: %s", ErrTransient, body.Message)
	}
	return Page{Items: body.Data.Items, HasMore: body.Data.HasMore, NextCursor: body.Data.NextCursor}, nil
}

func (c *Client) get(ctx context.Context, u string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrTransient, err)
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("New-Api-User", c.UserID)
	req.Header.Set("Accept", "application/json")
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrTransient, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("%w: read body: %v", ErrTransient, err)
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return ErrUnauthorized
	case resp.StatusCode == http.StatusTooManyRequests:
		return fmt.Errorf("%w: rate limited", ErrTransient)
	case resp.StatusCode >= 300:
		return fmt.Errorf("%w: http %d", ErrTransient, resp.StatusCode)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%w: invalid response: %v", ErrTransient, err)
	}
	return nil
}
