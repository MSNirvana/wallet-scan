package startupcheck

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"

	"wallet-scan/internal/adaptive"
	"wallet-scan/internal/db"
	"wallet-scan/internal/domain"
	"wallet-scan/internal/providers"
	"wallet-scan/internal/wallet"
)

const defaultWordCount = 12

// Store contains only the persistence operations needed by the startup check.
type Store interface {
	InsertAddressesWithIDs(context.Context, uuid.UUID, []domain.AddressInput) ([]int64, error)
	SaveWalletBalanceCheck(context.Context, db.WalletBalanceCheck) error
}

// Service generates one wallet and records one query result for each supported target.
type Service struct {
	Store     Store
	Providers map[string]providers.Provider
	Limiter   *adaptive.Limiter
}

type target struct {
	AddressID   int64
	AddressType string
	Chain       string
	Address     string
}

// New constructs a one-shot startup wallet check service.
func New(store Store, providerSet map[string]providers.Provider, limiter *adaptive.Limiter) *Service {
	return &Service{Store: store, Providers: providerSet, Limiter: limiter}
}

// RunOnce generates one wallet, queries all six targets exactly once, and stores every result.
func (s *Service) RunOnce(ctx context.Context) error {
	generated, err := wallet.Generate(defaultWordCount)
	if err != nil {
		return fmt.Errorf("generate startup wallet: %w", err)
	}
	addresses := generated.Addresses
	// The mnemonic is deliberately cleared before any persistence or provider work.
	generated.Mnemonic = ""

	batchID := uuid.New()
	inputs := []domain.AddressInput{
		{AddressType: domain.TypeBTC, Address: addresses.BTC.Address, Normalized: addresses.BTC.Address, Label: "startup-generated"},
		{AddressType: domain.TypeEVM, Address: addresses.ETH.Address, Normalized: addresses.ETH.Address, Label: "startup-generated"},
		{AddressType: domain.TypeSOL, Address: addresses.SOL.Address, Normalized: addresses.SOL.Address, Label: "startup-generated"},
		{AddressType: domain.TypeTRX, Address: addresses.TRX.Address, Normalized: addresses.TRX.Address, Label: "startup-generated"},
	}
	ids, err := s.Store.InsertAddressesWithIDs(ctx, batchID, inputs)
	if err != nil {
		return fmt.Errorf("persist startup wallet addresses: %w", err)
	}
	if len(ids) != len(inputs) {
		return fmt.Errorf("persist startup wallet addresses: got %d IDs, want %d", len(ids), len(inputs))
	}

	targets := []target{
		{AddressID: ids[0], AddressType: domain.TypeBTC, Chain: domain.ChainBTC, Address: addresses.BTC.Address},
		{AddressID: ids[1], AddressType: domain.TypeEVM, Chain: domain.ChainEthereum, Address: addresses.ETH.Address},
		{AddressID: ids[1], AddressType: domain.TypeEVM, Chain: domain.ChainArbitrum, Address: addresses.ETH.Address},
		{AddressID: ids[1], AddressType: domain.TypeEVM, Chain: domain.ChainBSC, Address: addresses.ETH.Address},
		{AddressID: ids[2], AddressType: domain.TypeSOL, Chain: domain.ChainSolana, Address: addresses.SOL.Address},
		{AddressID: ids[3], AddressType: domain.TypeTRX, Chain: domain.ChainTRON, Address: addresses.TRX.Address},
	}
	checks := make([]db.WalletBalanceCheck, len(targets))
	var waitGroup sync.WaitGroup
	waitGroup.Add(len(targets))
	for index, item := range targets {
		go func(index int, item target) {
			defer waitGroup.Done()
			checks[index] = s.check(ctx, item)
		}(index, item)
	}
	waitGroup.Wait()

	var firstSaveErr error
	for _, check := range checks {
		if err := s.Store.SaveWalletBalanceCheck(ctx, check); err != nil && firstSaveErr == nil {
			firstSaveErr = err
		}
	}
	if firstSaveErr != nil {
		return fmt.Errorf("persist startup wallet balance checks: %w", firstSaveErr)
	}
	return nil
}

func (s *Service) check(ctx context.Context, item target) db.WalletBalanceCheck {
	check := db.WalletBalanceCheck{
		AddressID: item.AddressID,
		Chain:     item.Chain,
		State:     "retry",
		CheckedAt: time.Now().UTC(),
	}
	if s.Limiter == nil {
		check.ErrorCode = "capacity_unavailable"
		check.ErrorMessage = "capacity controller unavailable"
		check.Provider = item.Chain
		return check
	}
	provider, ok := s.Providers[item.Chain]
	if !ok {
		check.ErrorCode = "provider_unconfigured"
		check.ErrorMessage = "provider is not configured"
		check.Provider = item.Chain
		return check
	}
	release, retryAfter, acquired := s.Limiter.Acquire(ctx, item.Chain)
	if !acquired {
		check.ErrorCode = "capacity_exhausted"
		check.ErrorMessage = "provider capacity is temporarily full"
		check.Provider = item.Chain
		check.RetryAfterMS = durationMillis(retryAfter)
		return check
	}
	started := time.Now()
	balance, queryErr := provider.Check(ctx, item.Address)
	providerErr := asProviderError(queryErr, item.Chain)
	release(adaptive.Outcome{
		Chain:     item.Chain,
		Retryable: providerErr != nil && providerErr.Temporary,
		Latency:   time.Since(started),
	})
	if providerErr != nil {
		check.ErrorCode = providerErr.Code
		check.ErrorMessage = providerErr.Error()
		check.Provider = providerErr.Provider
		check.RetryAfterMS = durationMillis(providerErr.RetryAfter)
		return check
	}
	if balance.Atomic == nil || balance.Atomic.Sign() < 0 || balance.Symbol == "" {
		check.ErrorCode = "response_decode_error"
		check.ErrorMessage = "provider returned an invalid balance"
		check.Provider = item.Chain
		return check
	}
	check.State = "checked"
	check.BalanceAtomic = balance.Atomic.String()
	check.AssetSymbol = balance.Symbol
	check.Provider = balance.Provider
	return check
}

func durationMillis(value time.Duration) *int64 {
	if value <= 0 {
		return nil
	}
	millis := value.Milliseconds()
	if millis < 1 {
		millis = 1
	}
	return &millis
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
