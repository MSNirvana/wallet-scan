package balanceclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCheckSendsBalanceProtocol(t *testing.T) {
	const apiKey = "test-key"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/balance" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("X-Internal-API-Key"); got != apiKey {
			t.Fatalf("api key header: got %q, want %q", got, apiKey)
		}
		var query Query
		if err := json.NewDecoder(r.Body).Decode(&query); err != nil {
			t.Fatal(err)
		}
		if query != (Query{AddressType: "btc", Chain: "btc", Address: "bc1qexample"}) {
			t.Fatalf("query: %#v", query)
		}
		_ = json.NewEncoder(w).Encode(Result{
			State:         "checked",
			AddressType:   query.AddressType,
			Address:       query.Address,
			Chain:         query.Chain,
			BalanceAtomic: "123",
			AssetSymbol:   "BTC",
			HasBalance:    true,
		})
	}))
	defer server.Close()

	client, err := New(server.URL, apiKey, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Check(context.Background(), Query{AddressType: "btc", Chain: "btc", Address: "bc1qexample"})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != "checked" || result.BalanceAtomic != "123" || !result.HasBalance {
		t.Fatalf("result: %#v", result)
	}
}

func TestCheckDecodesRetryResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_ = json.NewEncoder(w).Encode(Result{State: "retry", ErrorCode: "rate_limited", RetryAfterMS: 1500})
	}))
	defer server.Close()

	client, err := New(server.URL, "", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Check(context.Background(), Query{AddressType: "sol", Chain: "solana", Address: "sol-example"})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != "retry" || result.ErrorCode != "rate_limited" || result.RetryAfterMS != 1500 {
		t.Fatalf("result: %#v", result)
	}
}

func TestNewRejectsInvalidAPIURL(t *testing.T) {
	for _, rawURL := range []string{"", "localhost:8080", "ftp://localhost:8080", "http://"} {
		if _, err := New(rawURL, "", nil); err == nil {
			t.Fatalf("accepted invalid URL %q", rawURL)
		}
	}
}

func TestCheckMalformedResponseDoesNotExposeAPIKey(t *testing.T) {
	const apiKey = "secret-api-key"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not-json"))
	}))
	defer server.Close()

	client, err := New(server.URL, apiKey, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Check(context.Background(), Query{AddressType: "btc", Chain: "btc", Address: "bc1qexample"})
	if err == nil || result.State != "client_error" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if strings.Contains(err.Error(), apiKey) || strings.Contains(result.Message, apiKey) {
		t.Fatal("api key leaked in error")
	}
}
