package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"wallet-scan/internal/adaptive"
	"wallet-scan/internal/providers"
)

type fakeProvider struct {
	balance providers.Balance
	err     error
}

func (p fakeProvider) Check(context.Context, string) (providers.Balance, error) {
	return p.balance, p.err
}

func testBalanceService(provider providers.Provider) *BalanceService {
	return &BalanceService{
		Providers: map[string]providers.Provider{"ethereum": provider},
		Limiter: adaptive.New(adaptive.Config{
			InitialConcurrency: 2,
			MaxConcurrency:     2,
			QueueWait:          5 * time.Millisecond,
		}),
		APIKey:         "test-key",
		RequestTimeout: time.Second,
	}
}

func TestHandleBalanceReturnsPositiveAndNormalizesEVM(t *testing.T) {
	service := testBalanceService(fakeProvider{balance: providers.Balance{
		Atomic: big.NewInt(1000000000000000000), Symbol: "ETH", Chain: "ethereum", Provider: "ethereum",
	}})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/balance", strings.NewReader(`{"address_type":"evm","chain":"ethereum","address":"0x0000000000000000000000000000000000000001"}`))
	request.Header.Set("X-Internal-API-Key", "test-key")
	service.HandleBalance(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	var response balanceResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if !response.HasBalance || response.BalanceAtomic != "1000000000000000000" || response.Address != "0x0000000000000000000000000000000000000001" {
		t.Fatalf("unexpected response: %+v", response)
	}
}

func TestHandleBalanceRejectsInvalidChainAndAPIKey(t *testing.T) {
	service := testBalanceService(fakeProvider{balance: providers.Balance{Atomic: big.NewInt(0), Symbol: "ETH", Chain: "ethereum"}})
	request := httptest.NewRequest(http.MethodPost, "/v1/balance", strings.NewReader(`{"address_type":"evm","chain":"solana","address":"0x0000000000000000000000000000000000000001"}`))
	request.Header.Set("X-Internal-API-Key", "test-key")
	recorder := httptest.NewRecorder()
	service.HandleBalance(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid chain status = %d", recorder.Code)
	}

	request = httptest.NewRequest(http.MethodPost, "/v1/balance", strings.NewReader(`{"address_type":"evm","chain":"ethereum","address":"0x0000000000000000000000000000000000000001"}`))
	recorder = httptest.NewRecorder()
	service.HandleBalance(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", recorder.Code)
	}
}

func TestHandleBalanceReturnsRetryForProviderFailure(t *testing.T) {
	providerErr := &providers.ProviderError{Code: "rate_limited", Provider: "ethereum", Temporary: true, RetryAfter: 2 * time.Second, Err: errors.New("limit")}
	service := testBalanceService(fakeProvider{err: providerErr})
	request := httptest.NewRequest(http.MethodPost, "/v1/balance", strings.NewReader(`{"address_type":"evm","chain":"ethereum","address":"0x0000000000000000000000000000000000000001"}`))
	request.Header.Set("X-Internal-API-Key", "test-key")
	recorder := httptest.NewRecorder()
	service.HandleBalance(recorder, request)
	if recorder.Code != http.StatusTooManyRequests || recorder.Header().Get("Retry-After") != "2" {
		t.Fatalf("unexpected retry response: status=%d headers=%v body=%s", recorder.Code, recorder.Header(), recorder.Body.String())
	}
}

func TestHandleCapacityReturnsRecommendation(t *testing.T) {
	service := testBalanceService(fakeProvider{balance: providers.Balance{Atomic: big.NewInt(0), Symbol: "ETH", Chain: "ethereum"}})
	request := httptest.NewRequest(http.MethodGet, "/v1/capacity", nil)
	request.Header.Set("X-Internal-API-Key", "test-key")
	recorder := httptest.NewRecorder()
	service.HandleCapacity(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"max_in_flight":2`) {
		t.Fatalf("unexpected capacity response: %s", recorder.Body.String())
	}
}
