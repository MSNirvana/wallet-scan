package httpserver

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"wallet-scan/internal/adaptive"
	"wallet-scan/internal/importer"
	"wallet-scan/internal/providers"
)

// BalanceService performs one read-only native balance query per request.
type BalanceService struct {
	Providers      map[string]providers.Provider
	Limiter        *adaptive.Limiter
	APIKey         string
	RequestTimeout time.Duration
}

type balanceRequest struct {
	AddressType string `json:"address_type"`
	Chain       string `json:"chain"`
	Address     string `json:"address"`
}

type balanceResponse struct {
	State         string `json:"state"`
	AddressType   string `json:"address_type"`
	Address       string `json:"address"`
	Chain         string `json:"chain"`
	BalanceAtomic string `json:"balance_atomic"`
	AssetSymbol   string `json:"asset_symbol"`
	HasBalance    bool   `json:"has_balance"`
	CheckedAt     string `json:"checked_at"`
}

type errorResponse struct {
	State        string `json:"state"`
	ErrorCode    string `json:"error_code"`
	Message      string `json:"message"`
	Provider     string `json:"provider,omitempty"`
	RetryAfterMS int64  `json:"retry_after_ms,omitempty"`
}

// HandleCapacity exposes a current recommendation, not a guarantee.
func (s *BalanceService) HandleCapacity(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, errorResponse{State: "rejected", ErrorCode: "method_not_allowed", Message: "use GET"})
		return
	}
	if !s.authorized(r) {
		writeError(w, http.StatusUnauthorized, errorResponse{State: "rejected", ErrorCode: "unauthorized", Message: "invalid API key"})
		return
	}
	if s.Limiter == nil {
		writeError(w, http.StatusServiceUnavailable, errorResponse{State: "retry", ErrorCode: "capacity_unavailable", Message: "capacity controller unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, s.Limiter.Snapshot())
}

// HandleBalance performs one synchronous native-balance query.
func (s *BalanceService) HandleBalance(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, errorResponse{State: "rejected", ErrorCode: "method_not_allowed", Message: "use POST"})
		return
	}
	if !s.authorized(r) {
		writeError(w, http.StatusUnauthorized, errorResponse{State: "rejected", ErrorCode: "unauthorized", Message: "invalid API key"})
		return
	}
	var input balanceRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, errorResponse{State: "rejected", ErrorCode: "invalid_json", Message: "request must be a valid JSON object"})
		return
	}
	if err := ensureSingleJSONValue(decoder); err != nil {
		writeError(w, http.StatusBadRequest, errorResponse{State: "rejected", ErrorCode: "invalid_json", Message: "request must contain one JSON object"})
		return
	}
	input.AddressType = strings.ToLower(strings.TrimSpace(input.AddressType))
	input.Chain = strings.ToLower(strings.TrimSpace(input.Chain))
	input.Address = strings.TrimSpace(input.Address)
	normalized, err := importer.NormalizeAndValidate(input.AddressType, input.Address)
	if err != nil {
		writeError(w, http.StatusBadRequest, errorResponse{State: "rejected", ErrorCode: "invalid_address", Message: err.Error()})
		return
	}
	if !chainAllowed(input.AddressType, input.Chain) {
		writeError(w, http.StatusBadRequest, errorResponse{State: "rejected", ErrorCode: "invalid_chain", Message: "chain does not match address_type"})
		return
	}
	provider, ok := s.Providers[input.Chain]
	if !ok {
		writeError(w, http.StatusServiceUnavailable, errorResponse{State: "retry", ErrorCode: "provider_unconfigured", Message: "provider is not configured", Provider: input.Chain})
		return
	}
	if s.Limiter == nil {
		writeError(w, http.StatusServiceUnavailable, errorResponse{State: "retry", ErrorCode: "capacity_unavailable", Message: "capacity controller unavailable"})
		return
	}
	release, retryAfter, acquired := s.Limiter.Acquire(r.Context(), input.Chain)
	if !acquired {
		writeRetry(w, http.StatusTooManyRequests, errorResponse{State: "retry", ErrorCode: "capacity_exhausted", Message: "server concurrency capacity is temporarily full", RetryAfterMS: retryAfter.Milliseconds()}, retryAfter)
		return
	}
	started := time.Now()
	ctx := r.Context()
	if s.RequestTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.RequestTimeout)
		defer cancel()
	}
	balance, queryErr := provider.Check(ctx, normalized)
	providerErr := asProviderError(queryErr, input.Chain)
	release(adaptive.Outcome{Chain: input.Chain, Retryable: providerErr != nil && providerErr.Temporary, Latency: time.Since(started)})
	if providerErr != nil {
		if !providerErr.Temporary {
			writeError(w, http.StatusBadGateway, errorResponse{State: "retry", ErrorCode: providerErr.Code, Message: "provider query failed", Provider: providerErr.Provider})
			return
		}
		retryAfter = providerErr.RetryAfter
		if retryAfter <= 0 {
			retryAfter = time.Second
		}
		status := http.StatusServiceUnavailable
		if providerErr.Code == "rate_limited" {
			status = http.StatusTooManyRequests
		}
		writeRetry(w, status, errorResponse{State: "retry", ErrorCode: providerErr.Code, Message: "provider is temporarily unavailable", Provider: providerErr.Provider, RetryAfterMS: retryAfter.Milliseconds()}, retryAfter)
		return
	}
	if balance.Atomic == nil {
		writeError(w, http.StatusBadGateway, errorResponse{State: "retry", ErrorCode: "response_decode_error", Message: "provider returned an invalid balance", Provider: input.Chain})
		return
	}
	writeJSON(w, http.StatusOK, balanceResponse{
		State:         "checked",
		AddressType:   input.AddressType,
		Address:       normalized,
		Chain:         input.Chain,
		BalanceAtomic: balance.Atomic.String(),
		AssetSymbol:   balance.Symbol,
		HasBalance:    balance.Atomic.Sign() > 0,
		CheckedAt:     time.Now().UTC().Format(time.RFC3339),
	})
}

func (s *BalanceService) authorized(r *http.Request) bool {
	if s.APIKey == "" {
		return true
	}
	provided := r.Header.Get("X-Internal-API-Key")
	return len(provided) == len(s.APIKey) && subtle.ConstantTimeCompare([]byte(provided), []byte(s.APIKey)) == 1
}

func ensureSingleJSONValue(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
	return fmt.Errorf("extra JSON value")
}

func asProviderError(err error, fallback string) *providers.ProviderError {
	if err == nil {
		return nil
	}
	var providerErr *providers.ProviderError
	if errors.As(err, &providerErr) {
		return providerErr
	}
	return &providers.ProviderError{Code: "rpc_error", Provider: fallback, Temporary: true, Err: err}
}

func chainAllowed(addressType, chain string) bool {
	switch addressType {
	case "evm":
		return chain == "ethereum" || chain == "arbitrum" || chain == "bsc"
	case "btc":
		return chain == "btc"
	case "sol":
		return chain == "solana"
	case "trx":
		return chain == "tron"
	default:
		return false
	}
}

func writeRetry(w http.ResponseWriter, status int, response errorResponse, retryAfter time.Duration) {
	if retryAfter > 0 {
		seconds := int64((retryAfter + time.Second - 1) / time.Second)
		w.Header().Set("Retry-After", fmt.Sprintf("%d", seconds))
	}
	writeError(w, status, response)
}

func writeError(w http.ResponseWriter, status int, response errorResponse) {
	writeJSON(w, status, response)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
