package providers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestEVMProviderParsesZeroAndPositive(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","result":"0xde0b6b3a7640000","id":1}`))
	}))
	defer server.Close()
	provider := NewEVMProvider(NewHTTPClient(time.Second), server.URL, "ethereum", "ETH")
	balance, err := provider.Check(context.Background(), "0x0000000000000000000000000000000000000001")
	if err != nil {
		t.Fatal(err)
	}
	if balance.Atomic.String() != "1000000000000000000" {
		t.Fatalf("got %s", balance.Atomic)
	}
}

func TestHTTPClientClassifiesRateLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "2")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()
	var output map[string]any
	err := NewHTTPClient(time.Second).request(context.Background(), http.MethodGet, server.URL, nil, &output, "test")
	providerErr, ok := err.(*ProviderError)
	if !ok || providerErr.Code != "rate_limited" || providerErr.RetryAfter != 2*time.Second {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBTCProviderSubtractsSpentOutputs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"chain_stats":{"funded_txo_sum":150,"spent_txo_sum":40}}`)
	}))
	defer server.Close()
	balance, err := NewBTCProvider(NewHTTPClient(time.Second), server.URL).Check(context.Background(), "bc1qtest")
	if err != nil {
		t.Fatal(err)
	}
	if balance.Atomic.String() != "110" {
		t.Fatalf("got %s", balance.Atomic)
	}
}
