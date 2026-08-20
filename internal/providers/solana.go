package providers

import (
	"context"
	"fmt"
	"math/big"

	"wallet-scan/internal/domain"
)

type solanaResponse struct {
	JSONRPC string `json:"jsonrpc"`
	Result  struct {
		Value uint64 `json:"value"`
	} `json:"result"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// SolanaProvider reads lamports through getBalance.
type SolanaProvider struct {
	HTTP *HTTPClient
	URL  string
}

// NewSolanaProvider constructs a Solana JSON-RPC provider.
func NewSolanaProvider(httpClient *HTTPClient, rawURL string) *SolanaProvider {
	return &SolanaProvider{HTTP: httpClient, URL: rawURL}
}

// Check returns lamports for a Solana address.
func (p *SolanaProvider) Check(ctx context.Context, address string) (Balance, error) {
	payload := map[string]any{"jsonrpc": "2.0", "method": "getBalance", "params": []any{address}, "id": 1}
	var response solanaResponse
	if err := p.HTTP.request(ctx, "POST", p.URL, payload, &response, domain.ChainSolana); err != nil {
		return Balance{}, err
	}
	if response.Error != nil {
		return Balance{}, &ProviderError{Code: "rpc_error", Provider: domain.ChainSolana, Temporary: true, Err: fmt.Errorf("code %d", response.Error.Code)}
	}
	return Balance{Atomic: new(big.Int).SetUint64(response.Result.Value), Symbol: "SOL", Chain: domain.ChainSolana, Provider: domain.ChainSolana}, nil
}

var _ Provider = (*SolanaProvider)(nil)
