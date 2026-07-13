// Package policy implements the synchronous whitelist check against the
// Policy / Risk Engine.
package policy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// CheckRequest is sent to the Policy/Risk Engine.
type CheckRequest struct {
	WalletID   string `json:"wallet_id"`
	ToAddress  string `json:"to_address"`
	Asset      string `json:"asset"`
	Amount     string `json:"amount"`
}

// CheckResponse is the Policy/Risk Engine verdict.
type CheckResponse struct {
	Approved         bool   `json:"approved"`
	DecisionID       string `json:"decision_id"`
	Reason           string `json:"reason"`
}

// Client is the Policy/Risk Engine REST client.
type Client interface {
	CheckWhitelist(ctx context.Context, req *CheckRequest) (*CheckResponse, error)
}

// HTTPClient posts JSON to POLICY_RISK_ENGINE_URL.
type HTTPClient struct {
	URL    string
	Client *http.Client
}

// NewHTTPClient constructs an HTTP policy client.
func NewHTTPClient(url string) *HTTPClient {
	return &HTTPClient{URL: url, Client: &http.Client{Timeout: 5 * time.Second}}
}

// CheckWhitelist calls POST /v1/whitelist/check.
func (c *HTTPClient) CheckWhitelist(ctx context.Context, req *CheckRequest) (*CheckResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL+"/v1/whitelist/check", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := c.Client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("policy engine rejected: %d %s", resp.StatusCode, string(b))
	}
	var out CheckResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

// MockClient is an in-memory policy client for tests.
type MockClient struct {
	CheckFn func(ctx context.Context, req *CheckRequest) (*CheckResponse, error)
}

// CheckWhitelist delegates to CheckFn.
func (m *MockClient) CheckWhitelist(ctx context.Context, req *CheckRequest) (*CheckResponse, error) {
	if m.CheckFn != nil {
		return m.CheckFn(ctx, req)
	}
	return &CheckResponse{Approved: true, DecisionID: "mock-decision"}, nil
}