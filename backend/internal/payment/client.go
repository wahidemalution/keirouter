// Package payment integrates the SumoPod payment gateway for portal credit
// top-ups and verifies its webhook authenticity (Svix HMAC signature and/or a
// shared webhook token).
package payment

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// maxResponseBytes bounds upstream response bodies.
const maxResponseBytes = 1 << 20

// Client calls the SumoPod payment API.
type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

// NewClient builds a SumoPod client. baseURL is host-root (no trailing slash).
func NewClient(baseURL, apiKey string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		http:    &http.Client{Timeout: 20 * time.Second},
	}
}

// CreateRequest is the SumoPod create-payment payload.
type CreateRequest struct {
	OrderID               string `json:"order_id"`
	AmountIDR             int64  `json:"amount"`
	Currency              string `json:"currency"`
	ExpiresInHours        int    `json:"expires_in_hours"`
	SuccessReturnURL      string `json:"success_return_url"`
	CancelReturnURL       string `json:"cancel_return_url"`
	PaymentMethodTypeCode string `json:"payment_method_type_code"`
}

// CreateResponse is the parsed SumoPod create-payment response.
type CreateResponse struct {
	PaymentID      string `json:"payment_id"`
	OrderID        string `json:"order_id"`
	Amount         int64  `json:"amount"`
	Fee            int64  `json:"fee"`
	NetAmount      int64  `json:"net_amount"`
	PaymentLinkURL string `json:"payment_link_url"`
	Status         string `json:"status"`
	ExpiresAt      string `json:"expires_at"`
}

// CreatePayment creates a payment and returns its link. Non-2xx is an error.
func (c *Client) CreatePayment(ctx context.Context, req CreateRequest) (CreateResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return CreateResponse{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/v1/payments", bytes.NewReader(body))
	if err != nil {
		return CreateResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-Api-Key", c.apiKey)
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return CreateResponse{}, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return CreateResponse{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return CreateResponse{}, fmt.Errorf("payment: create status %d: %s", resp.StatusCode, string(data))
	}
	var out CreateResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return CreateResponse{}, fmt.Errorf("payment: decode create response: %w", err)
	}
	if out.PaymentID == "" || out.PaymentLinkURL == "" {
		return CreateResponse{}, fmt.Errorf("payment: create response missing payment_id or link")
	}
	return out, nil
}

// VerifySignature checks a Svix-style HMAC-SHA256 signature. svixSignature may
// contain multiple space-separated "v1,<base64>" values (rotation window).
func VerifySignature(secret, svixID, svixTimestamp, svixSignature string, rawBody []byte) bool {
	if secret == "" || svixID == "" || svixTimestamp == "" || svixSignature == "" {
		return false
	}
	secretBytes, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(secret, "whsec_"))
	if err != nil {
		return false
	}
	signed := svixID + "." + svixTimestamp + "." + string(rawBody)
	mac := hmac.New(sha256.New, secretBytes)
	mac.Write([]byte(signed))
	expected := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	for _, part := range strings.Fields(svixSignature) {
		sig := strings.TrimPrefix(part, "v1,")
		if hmac.Equal([]byte(sig), []byte(expected)) {
			return true
		}
	}
	return false
}

// VerifyToken compares the shared webhook token in constant time.
func VerifyToken(expected, received string) bool {
	if expected == "" || received == "" {
		return false
	}
	return hmac.Equal([]byte(expected), []byte(received))
}