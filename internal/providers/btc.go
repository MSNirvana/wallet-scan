package providers

import (
	"context"
	"math/big"

	"wallet-scan/internal/domain"
)

type btcResponse struct {
	ChainStats struct {
		Funded uint64 `json:"funded_txo_sum"`
		Spent  uint64 `json:"spent_txo_sum"`
	} `json:"chain_stats"`
}

// BTCProvider reads confirmed Bitcoin UTXO totals from Mempool REST.
type BTCProvider struct {
	HTTP *HTTPClient
	URL  string
}

// NewBTCProvider constructs a Bitcoin REST provider.
func NewBTCProvider(httpClient *HTTPClient, rawURL string) *BTCProvider {
	return &BTCProvider{HTTP: httpClient, URL: rawURL}
}

// Check returns confirmed satoshis for a Bitcoin address.
func (p *BTCProvider) Check(ctx context.Context, address string) (Balance, error) {
	var response btcResponse
	if err := p.HTTP.request(ctx, "GET", addressPath(p.URL, address), nil, &response, domain.ChainBTC); err != nil {
		return Balance{}, err
	}
	atomic := new(big.Int).SetUint64(response.ChainStats.Funded)
	atomic.Sub(atomic, new(big.Int).SetUint64(response.ChainStats.Spent))
	if atomic.Sign() < 0 {
		atomic.SetInt64(0)
	}
	return Balance{Atomic: atomic, Symbol: "BTC", Chain: domain.ChainBTC, Provider: domain.ChainBTC}, nil
}

var _ Provider = (*BTCProvider)(nil)
