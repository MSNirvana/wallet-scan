package providers

import (
	"context"
	"math/big"

	"wallet-scan/internal/domain"
)

type tronResponse struct {
	Balance uint64 `json:"balance"`
}

// TRONProvider reads TRX sun from the TronGrid account endpoint.
type TRONProvider struct {
	HTTP *HTTPClient
	URL  string
}

// NewTRONProvider constructs a TRON HTTP provider.
func NewTRONProvider(httpClient *HTTPClient, rawURL string) *TRONProvider {
	return &TRONProvider{HTTP: httpClient, URL: rawURL}
}

// Check returns sun for a TRON address.
func (p *TRONProvider) Check(ctx context.Context, address string) (Balance, error) {
	payload := map[string]any{"address": address, "visible": true}
	var response tronResponse
	if err := p.HTTP.request(ctx, "POST", rpcURL(p.URL)+"/wallet/getaccount", payload, &response, domain.ChainTRON); err != nil {
		return Balance{}, err
	}
	return Balance{Atomic: new(big.Int).SetUint64(response.Balance), Symbol: "TRX", Chain: domain.ChainTRON, Provider: domain.ChainTRON}, nil
}

var _ Provider = (*TRONProvider)(nil)
