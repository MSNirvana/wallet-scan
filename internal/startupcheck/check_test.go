package startupcheck

import (
	"context"
	"errors"
	"math/big"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"wallet-scan/internal/adaptive"
	"wallet-scan/internal/db"
	"wallet-scan/internal/domain"
	"wallet-scan/internal/providers"
)

type fakeStore struct {
	mu     sync.Mutex
	inputs []domain.AddressInput
	checks []db.WalletBalanceCheck
	nextID int64
	err    error
}

func (s *fakeStore) InsertAddressesWithIDs(_ context.Context, _ uuid.UUID, inputs []domain.AddressInput) ([]int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	s.inputs = append([]domain.AddressInput(nil), inputs...)
	ids := make([]int64, len(inputs))
	for index := range inputs {
		s.nextID++
		ids[index] = s.nextID
	}
	return ids, nil
}

func (s *fakeStore) SaveWalletBalanceCheck(_ context.Context, check db.WalletBalanceCheck) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.checks = append(s.checks, check)
	return nil
}

type fakeProvider struct {
	mu      sync.Mutex
	calls   int
	balance providers.Balance
	err     error
}

func (p *fakeProvider) Check(context.Context, string) (providers.Balance, error) {
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
	return p.balance, p.err
}

func newTestLimiter() *adaptive.Limiter {
	return adaptive.New(adaptive.Config{
		InitialConcurrency: 6,
		MaxConcurrency:     6,
		QueueWait:          time.Second,
		AdjustInterval:     time.Hour,
		TargetLatency:      time.Second,
	})
}

func newProviderSet(failureChain string) (map[string]providers.Provider, map[string]*fakeProvider) {
	chains := []struct {
		name   string
		symbol string
	}{
		{name: domain.ChainBTC, symbol: "BTC"},
		{name: domain.ChainEthereum, symbol: "ETH"},
		{name: domain.ChainArbitrum, symbol: "ETH"},
		{name: domain.ChainBSC, symbol: "BNB"},
		{name: domain.ChainSolana, symbol: "SOL"},
		{name: domain.ChainTRON, symbol: "TRX"},
	}
	set := make(map[string]providers.Provider, len(chains))
	fakes := make(map[string]*fakeProvider, len(chains))
	for _, chain := range chains {
		balance := providers.Balance{Atomic: big.NewInt(0), Symbol: chain.symbol, Chain: chain.name, Provider: chain.name}
		if chain.name == domain.ChainBSC {
			balance.Atomic = big.NewInt(7)
		}
		fake := &fakeProvider{balance: balance}
		if chain.name == failureChain {
			fake.err = &providers.ProviderError{Code: "rate_limited", Provider: chain.name, RetryAfter: 2 * time.Second, Temporary: true}
		}
		set[chain.name] = fake
		fakes[chain.name] = fake
	}
	return set, fakes
}

func TestRunOnceStoresAllSixQueryResults(t *testing.T) {
	store := &fakeStore{}
	providerSet, fakes := newProviderSet("")
	service := New(store, providerSet, newTestLimiter())

	if err := service.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.inputs) != 4 {
		t.Fatalf("got %d addresses, want 4", len(store.inputs))
	}
	if len(store.checks) != 6 {
		t.Fatalf("got %d checks, want 6", len(store.checks))
	}
	for chain, provider := range fakes {
		provider.mu.Lock()
		calls := provider.calls
		provider.mu.Unlock()
		if calls != 1 {
			t.Errorf("%s called %d times, want 1", chain, calls)
		}
	}
	for _, check := range store.checks {
		if check.State != "checked" {
			t.Errorf("%s state: got %q, want checked", check.Chain, check.State)
		}
		if check.BalanceAtomic == "" || check.AssetSymbol == "" {
			t.Errorf("%s missing successful balance fields: %#v", check.Chain, check)
		}
	}
}

func TestRunOncePersistsProviderFailureWithoutTreatingItAsZero(t *testing.T) {
	store := &fakeStore{}
	providerSet, _ := newProviderSet(domain.ChainBSC)
	service := New(store, providerSet, newTestLimiter())

	if err := service.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.checks) != 6 {
		t.Fatalf("got %d checks, want 6", len(store.checks))
	}
	var failed db.WalletBalanceCheck
	for _, check := range store.checks {
		if check.Chain == domain.ChainBSC {
			failed = check
		}
	}
	if failed.State != "retry" || failed.ErrorCode != "rate_limited" {
		t.Fatalf("unexpected failed result: %#v", failed)
	}
	if failed.BalanceAtomic != "" {
		t.Fatalf("failed result was recorded as a balance: %#v", failed)
	}
	if failed.RetryAfterMS == nil || *failed.RetryAfterMS != 2000 {
		t.Fatalf("missing retry-after value: %#v", failed.RetryAfterMS)
	}
}

func TestRunOnceReturnsPersistenceErrorWithoutExposingSecret(t *testing.T) {
	store := &fakeStore{err: errors.New("database unavailable")}
	providerSet, _ := newProviderSet("")
	service := New(store, providerSet, newTestLimiter())

	err := service.RunOnce(context.Background())
	if err == nil || err.Error() != "persist startup wallet addresses: database unavailable" {
		t.Fatalf("unexpected error: %v", err)
	}
}
