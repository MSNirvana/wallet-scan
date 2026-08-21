package balanceclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const balancePath = "/v1/balance"

// Query identifies one public address balance request.
type Query struct {
	AddressType string `json:"address_type"`
	Chain       string `json:"chain"`
	Address     string `json:"address"`
}

// Result contains the API result for one query. It never contains API credentials.
type Result struct {
	State         string `json:"state"`
	AddressType   string `json:"address_type"`
	Address       string `json:"address"`
	Chain         string `json:"chain"`
	BalanceAtomic string `json:"balance_atomic"`
	AssetSymbol   string `json:"asset_symbol"`
	HasBalance    bool   `json:"has_balance"`
	CheckedAt     string `json:"checked_at"`
	ErrorCode     string `json:"error_code,omitempty"`
	Message       string `json:"message,omitempty"`
	Provider      string `json:"provider,omitempty"`
	RetryAfterMS  int64  `json:"retry_after_ms,omitempty"`
}

// Client calls the wallet-scan synchronous balance API.
type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

// New creates a client for an HTTP or HTTPS wallet-scan base URL.
func New(baseURL, apiKey string, httpClient *http.Client) (*Client, error) {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, fmt.Errorf("api-url must be an http or https URL")
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	trimmedPath := strings.TrimRight(parsed.Path, "/")
	parsed.Path = trimmedPath
	parsed.RawPath = ""
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	return &Client{baseURL: strings.TrimRight(parsed.String(), "/"), apiKey: apiKey, http: httpClient}, nil
}

// Check performs one public-address balance request.
func (c *Client) Check(ctx context.Context, query Query) (Result, error) {
	result := Result{AddressType: query.AddressType, Address: query.Address, Chain: query.Chain}
	body, err := json.Marshal(query)
	if err != nil {
		return clientError(result, "request_encode_error", err), err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+balancePath, bytes.NewReader(body))
	if err != nil {
		return clientError(result, "request_error", err), err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("X-Internal-API-Key", c.apiKey)
	}
	response, err := c.http.Do(req)
	if err != nil {
		return clientError(result, "connection_error", err), err
	}
	defer response.Body.Close()
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result); err != nil {
		wrapped := fmt.Errorf("decode balance API response: %w", err)
		return clientError(result, "response_decode_error", wrapped), wrapped
	}
	if result.AddressType == "" {
		result.AddressType = query.AddressType
	}
	if result.Address == "" {
		result.Address = query.Address
	}
	if result.Chain == "" {
		result.Chain = query.Chain
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		if result.State == "" {
			result.State = "client_error"
		}
		if result.ErrorCode == "" {
			result.ErrorCode = fmt.Sprintf("http_%d", response.StatusCode)
		}
		if result.Message == "" {
			result.Message = "balance API request failed"
		}
	}
	return result, nil
}

func clientError(result Result, code string, err error) Result {
	result.State = "client_error"
	result.ErrorCode = code
	result.Message = err.Error()
	return result
}
