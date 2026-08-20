package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Balance is an exact native balance in the chain's smallest unit.
type Balance struct {
	Atomic   *big.Int
	Symbol   string
	Chain    string
	Provider string
}

// Provider is the read-only balance operation shared by all chains.
type Provider interface {
	Check(context.Context, string) (Balance, error)
}

// ProviderError classifies a provider failure for retry and health decisions.
type ProviderError struct {
	Code       string
	Provider   string
	StatusCode int
	RetryAfter time.Duration
	Temporary  bool
	Err        error
}

func (e *ProviderError) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("%s: %s", e.Provider, e.Code)
	}
	return fmt.Sprintf("%s: %s: %v", e.Provider, e.Code, e.Err)
}

func (e *ProviderError) Unwrap() error { return e.Err }

func providerError(code, name string, temporary bool, err error) *ProviderError {
	return &ProviderError{Code: code, Provider: name, Temporary: temporary, Err: err}
}

// HTTPClient provides bounded HTTP requests for provider implementations.
type HTTPClient struct {
	Client *http.Client
}

// NewHTTPClient creates a client with a request timeout.
func NewHTTPClient(timeout time.Duration) *HTTPClient {
	return &HTTPClient{Client: &http.Client{Timeout: timeout}}
}

func (c *HTTPClient) request(ctx context.Context, method, rawURL string, payload any, output any, provider string) error {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return providerError("request_encode_error", provider, false, err)
		}
		body = strings.NewReader(string(encoded))
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return providerError("request_error", provider, false, err)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := c.Client.Do(req)
	if err != nil {
		code := "connection_error"
		temporary := true
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			code = "timeout"
		}
		return providerError(code, provider, temporary, err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusTooManyRequests {
		retryAfter := time.Duration(0)
		if value, parseErr := time.ParseDuration(response.Header.Get("Retry-After") + "s"); parseErr == nil {
			retryAfter = value
		}
		return &ProviderError{Code: "rate_limited", Provider: provider, StatusCode: response.StatusCode, RetryAfter: retryAfter, Temporary: true}
	}
	if response.StatusCode >= 500 {
		return &ProviderError{Code: "provider_unhealthy", Provider: provider, StatusCode: response.StatusCode, Temporary: true}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &ProviderError{Code: "rpc_error", Provider: provider, StatusCode: response.StatusCode, Temporary: false}
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 1<<20))
	if err := decoder.Decode(output); err != nil {
		return providerError("response_decode_error", provider, false, err)
	}
	return nil
}

func rpcURL(base string) string { return strings.TrimRight(base, "/") }

func addressPath(base, address string) string {
	return rpcURL(base) + "/address/" + url.PathEscape(address)
}
