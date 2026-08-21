package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/tyler-smith/go-bip39"

	"wallet-scan/internal/balanceclient"
	"wallet-scan/internal/wallet"
)

func TestGenerateDoesNotRequireDatabase(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://invalid-host/never-connect")
	var stdout bytes.Buffer
	if err := runWithIO([]string{"generate", "--json"}, &stdout, io.Discard); err != nil {
		t.Fatal(err)
	}
	var result wallet.Result
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !bip39.IsMnemonicValid(result.Mnemonic) {
		t.Fatal("invalid generated mnemonic")
	}
	if result.Addresses.BTC.Address == "" || result.Addresses.ETH.Address == "" ||
		result.Addresses.SOL.Address == "" || result.Addresses.TRX.Address == "" {
		t.Fatal("missing generated address")
	}
}

func TestGenerateSupports24Words(t *testing.T) {
	var stdout bytes.Buffer
	if err := runWithIO([]string{"generate", "--words", "24"}, &stdout, io.Discard); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(stdout.String(), "\n")
	if len(lines) == 0 || !strings.HasPrefix(lines[0], "Mnemonic: ") {
		t.Fatalf("unexpected output: %q", stdout.String())
	}
	if got := len(strings.Fields(strings.TrimPrefix(lines[0], "Mnemonic: "))); got != 24 {
		t.Fatalf("got %d mnemonic words, want 24", got)
	}
}

func TestGenerateRejectsInvalidWordsWithoutOutput(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := runWithIO([]string{"generate", "--words", "15"}, &stdout, &stderr); err == nil {
		t.Fatal("expected invalid word count error")
	}
	if stdout.Len() != 0 {
		t.Fatalf("unexpected secret output: %q", stdout.String())
	}
}

func TestGenerateRejectsPositionalArguments(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := runWithIO([]string{"generate", "extra"}, &stdout, &stderr); err == nil {
		t.Fatal("expected positional argument error")
	}
	if stdout.Len() != 0 {
		t.Fatalf("unexpected output: %q", stdout.String())
	}
}

func TestGenerateScanQueriesAllSupportedChains(t *testing.T) {
	const apiKey = "scan-key"
	var mu sync.Mutex
	queries := make([]balanceclient.Query, 0, 6)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Internal-API-Key"); got != apiKey {
			t.Errorf("api key header: got %q, want %q", got, apiKey)
		}
		var query balanceclient.Query
		if err := json.NewDecoder(r.Body).Decode(&query); err != nil {
			t.Errorf("decode query: %v", err)
		}
		mu.Lock()
		queries = append(queries, query)
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(balanceclient.Result{
			State:         "checked",
			AddressType:   query.AddressType,
			Address:       query.Address,
			Chain:         query.Chain,
			BalanceAtomic: "0",
			AssetSymbol:   "NATIVE",
		})
	}))
	defer server.Close()

	var stdout bytes.Buffer
	if err := runWithIO([]string{"generate", "--scan", "--api-url", server.URL, "--api-key", apiKey, "--json"}, &stdout, io.Discard); err != nil {
		t.Fatal(err)
	}
	var output generatedOutput
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatal(err)
	}
	if len(output.Scan) != 6 {
		t.Fatalf("got %d scan results, want 6", len(output.Scan))
	}
	want := map[string]bool{"btc/btc": true, "evm/ethereum": true, "evm/arbitrum": true, "evm/bsc": true, "sol/solana": true, "trx/tron": true}
	for _, query := range queries {
		delete(want, query.AddressType+"/"+query.Chain)
	}
	if len(want) != 0 {
		t.Fatalf("missing query mappings: %#v", want)
	}
	if strings.Contains(stdout.String(), apiKey) {
		t.Fatal("api key leaked into command output")
	}
}

func TestGenerateScanReturnsErrorForRetryResult(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var query balanceclient.Query
		if err := json.NewDecoder(r.Body).Decode(&query); err != nil {
			t.Errorf("decode query: %v", err)
		}
		if query.Chain == "bsc" {
			w.WriteHeader(http.StatusTooManyRequests)
			_ = json.NewEncoder(w).Encode(balanceclient.Result{State: "retry", ErrorCode: "rate_limited"})
			return
		}
		_ = json.NewEncoder(w).Encode(balanceclient.Result{State: "checked", AddressType: query.AddressType, Address: query.Address, Chain: query.Chain})
	}))
	defer server.Close()

	var stdout bytes.Buffer
	if err := runWithIO([]string{"generate", "--scan", "--api-url", server.URL, "--json"}, &stdout, io.Discard); err == nil {
		t.Fatal("expected scan error")
	}
	var output generatedOutput
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatal(err)
	}
	for _, result := range output.Scan {
		if result.Chain == "bsc" && result.State != "retry" {
			t.Fatalf("unexpected BSC result: %#v", result)
		}
	}
}
