package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"

	"wallet-scan/internal/domain"
)

type evmResponse struct {
	JSONRPC string       `json:"jsonrpc"`
	ID      int          `json:"id"`
	Result  string       `json:"result"`
	Error   *evmRPCError `json:"error"`
}

type evmRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// EVMProvider reads native balances with eth_getBalance.
type EVMProvider struct {
	HTTP      *HTTPClient
	URL       string
	ChainName string
	Symbol    string
}

// NewEVMProvider constructs an EVM provider for one network.
func NewEVMProvider(httpClient *HTTPClient, rawURL, chainName, symbol string) *EVMProvider {
	return &EVMProvider{HTTP: httpClient, URL: rawURL, ChainName: chainName, Symbol: symbol}
}

// Check returns the latest native balance for an EVM address.
func (p *EVMProvider) Check(ctx context.Context, address string) (Balance, error) {
	payload := map[string]any{"jsonrpc": "2.0", "method": "eth_getBalance", "params": []any{address, "latest"}, "id": 1}
	var response evmResponse
	if err := p.HTTP.request(ctx, "POST", p.URL, payload, &response, p.ChainName); err != nil {
		return Balance{}, err
	}
	if response.Error != nil {
		return Balance{}, &ProviderError{Code: "rpc_error", Provider: p.ChainName, Temporary: response.Error.Code == -32005 || response.Error.Code == -32016, Err: fmt.Errorf("code %d", response.Error.Code)}
	}
	value := strings.TrimPrefix(response.Result, "0x")
	if value == "" {
		value = "0"
	}
	atomic := new(big.Int)
	if _, ok := atomic.SetString(value, 16); !ok {
		return Balance{}, providerError("response_decode_error", p.ChainName, false, fmt.Errorf("invalid hex quantity"))
	}
	return Balance{Atomic: atomic, Symbol: p.Symbol, Chain: p.ChainName, Provider: p.ChainName}, nil
}

var _ Provider = (*EVMProvider)(nil)
var _ = json.RawMessage{}

func evmChain(name string) string {
	switch name {
	case domain.ChainEthereum, domain.ChainArbitrum, domain.ChainBSC:
		return name
	default:
		return name
	}
}
