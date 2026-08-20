package scanner

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"wallet-scan/internal/db"
	"wallet-scan/internal/domain"
	"wallet-scan/internal/providers"
)

// Config controls scan batch and retry behavior.
type Config struct {
	BatchSize            int
	AddressWorkers       int
	MaxRetries           int
	RetryBaseDelay       time.Duration
	NodeFailureThreshold int
}

// Scanner executes one durable scan run.
type Scanner struct {
	Store     *db.Store
	Providers map[string]providers.Provider
	Config    Config
	Health    *HealthTracker
	Notifier  Notifier
	limits    map[string]chan struct{}
}

// Notifier delivers a persisted notification event immediately after commit.
type Notifier interface {
	Deliver(context.Context, int64) error
}

// New creates a scanner with per-chain concurrency limits.
func New(store *db.Store, providerSet map[string]providers.Provider, config Config, limits map[string]int) *Scanner {
	semaphores := make(map[string]chan struct{}, len(limits))
	for chain, size := range limits {
		if size < 1 {
			size = 1
		}
		semaphores[chain] = make(chan struct{}, size)
	}
	return &Scanner{Store: store, Providers: providerSet, Config: config, Health: NewHealthTracker(config.NodeFailureThreshold), limits: semaphores}
}

// RunOnce resumes an active run or creates a new run for all current addresses.
func (s *Scanner) RunOnce(ctx context.Context) error {
	run, err := s.Store.ActiveScan(ctx)
	if err != nil {
		return err
	}
	if run == nil {
		startID, endID, ok, err := s.Store.UnscannedRange(ctx)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		run, err = s.Store.CreateScan(ctx, startID, endID)
		if err != nil {
			return err
		}
	} else if run.Status == "paused" {
		if err := s.Store.ResumeScan(ctx, run.ID); err != nil {
			return err
		}
		run.Status = "running"
	}
	return s.run(ctx, *run)
}

// RetryFailed rechecks unresolved address-chain failures without changing the main cursor.
func (s *Scanner) RetryFailed(ctx context.Context, limit int) error {
	items, err := s.Store.NextRetries(ctx, limit)
	if err != nil {
		return err
	}
	for _, item := range items {
		provider, ok := s.Providers[item.Chain]
		if !ok {
			continue
		}
		balance, checkErr := s.checkWithRetry(ctx, item.Chain, provider, item.Address)
		if checkErr != nil {
			providerErr := asProviderError(checkErr, item.Chain)
			if err := s.Store.RecordRetry(ctx, item.AddressID, item.Chain, providerErr.Code, providerErr.Provider, providerErr.Error(), time.Now().Add(s.Config.RetryBaseDelay)); err != nil {
				return err
			}
			continue
		}
		if err := s.Store.CloseRetry(ctx, item.AddressID, item.Chain); err != nil {
			return err
		}
		if balance.Atomic.Sign() <= 0 {
			continue
		}
		eventID, err := s.Store.SavePositiveFindings(ctx, []db.PositiveFinding{{
			AddressID: item.AddressID, Chain: balance.Chain,
			Balance: balance.Atomic.String(), AssetSymbol: balance.Symbol,
		}})
		if err != nil {
			return err
		}
		if eventID != nil && s.Notifier != nil {
			_ = s.Notifier.Deliver(ctx, *eventID)
		}
	}
	return nil
}

func (s *Scanner) run(ctx context.Context, run domain.ScanRun) error {
	for {
		addresses, err := s.Store.NextAddresses(ctx, run, s.Config.BatchSize)
		if err != nil {
			return err
		}
		if len(addresses) == 0 {
			return s.Store.CompleteScan(ctx, run.ID)
		}
		results := s.processBatch(ctx, addresses)
		var processed, empty, positive, failures int64
		pause := false
		for _, result := range results {
			processed++
			if len(result.Findings) == 0 && len(result.Errors) == 0 {
				empty++
			}
			positive += int64(len(result.Findings))
			failures += int64(len(result.Errors))
			for _, failure := range result.Errors {
				next := time.Now().Add(s.Config.RetryBaseDelay)
				if err := s.Store.RecordRetry(ctx, result.Address.ID, failure.Chain, failure.Code, failure.Provider, failure.Message, next); err != nil {
					return err
				}
				if failure.Code == "invalid_address" {
					if err := s.Store.CloseRetry(ctx, result.Address.ID, failure.Chain); err != nil {
						return err
					}
				}
			}
			for _, alert := range result.NodeAlerts {
				incidentID, err := s.Store.CreateNodeIncident(ctx, alert.Chain, alert.Provider, alert.Code, alert.Failures)
				if err != nil {
					return err
				}
				eventID, err := s.Store.CreateNodeNotificationEvent(ctx, incidentID)
				if err != nil {
					return err
				}
				if s.Notifier != nil {
					_ = s.Notifier.Deliver(ctx, eventID)
				}
			}
			eventID, err := s.Store.SavePositiveFindings(ctx, result.Findings)
			if err != nil {
				return err
			}
			if eventID != nil && s.Notifier != nil {
				_ = s.Notifier.Deliver(ctx, *eventID)
			}
			pause = pause || result.Pause
		}
		if pause {
			if err := s.Store.PauseScan(ctx, run.ID); err != nil {
				return err
			}
			return fmt.Errorf("provider health threshold reached; scan paused at cursor %d", run.CursorID)
		}
		cursor := addresses[len(addresses)-1].ID
		if err := s.Store.AdvanceScan(ctx, run.ID, cursor, processed, empty, positive, failures); err != nil {
			return err
		}
		run.CursorID = cursor
	}
}

type addressResult struct {
	Address    domain.Address
	Findings   []db.PositiveFinding
	Errors     []scanFailure
	NodeAlerts []nodeAlert
	Pause      bool
}

type scanFailure struct {
	Chain    string
	Code     string
	Provider string
	Message  string
}

type nodeAlert struct {
	Chain    string
	Provider string
	Code     string
	Failures int
}

func (s *Scanner) processBatch(ctx context.Context, addresses []domain.Address) []addressResult {
	jobs := make(chan domain.Address)
	results := make(chan addressResult, len(addresses))
	workers := s.Config.AddressWorkers
	if workers > len(addresses) {
		workers = len(addresses)
	}
	var wait sync.WaitGroup
	for i := 0; i < workers; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for address := range jobs {
				results <- s.checkAddress(ctx, address)
			}
		}()
	}
	go func() {
		for _, address := range addresses {
			jobs <- address
		}
		close(jobs)
		wait.Wait()
		close(results)
	}()
	ordered := make([]addressResult, 0, len(addresses))
	for result := range results {
		ordered = append(ordered, result)
	}
	return ordered
}

func (s *Scanner) checkAddress(ctx context.Context, address domain.Address) addressResult {
	result := addressResult{Address: address}
	for _, chain := range chainsFor(address.AddressType) {
		provider, ok := s.Providers[chain]
		if !ok {
			result.Errors = append(result.Errors, scanFailure{Chain: chain, Code: "provider_unconfigured", Message: "provider is not configured"})
			continue
		}
		balance, err := s.checkWithRetry(ctx, chain, provider, address.Address)
		if err != nil {
			providerErr := asProviderError(err, chain)
			result.Errors = append(result.Errors, scanFailure{Chain: chain, Code: providerErr.Code, Provider: providerErr.Provider, Message: providerErr.Error()})
			if s.Health.IsUnhealthy(chain) {
				result.Pause = true
				if alert, failures := s.Health.TakeAlert(chain); alert {
					result.NodeAlerts = append(result.NodeAlerts, nodeAlert{Chain: chain, Provider: providerErr.Provider, Code: providerErr.Code, Failures: failures})
				}
			}
			continue
		}
		if balance.Atomic.Sign() > 0 {
			result.Findings = append(result.Findings, db.PositiveFinding{AddressID: address.ID, Chain: balance.Chain, Balance: balance.Atomic.String(), AssetSymbol: balance.Symbol})
		}
	}
	return result
}

func (s *Scanner) checkWithRetry(ctx context.Context, chain string, provider providers.Provider, address string) (providers.Balance, error) {
	for attempt := 0; attempt <= s.Config.MaxRetries; attempt++ {
		if limiter, ok := s.limits[chain]; ok {
			select {
			case limiter <- struct{}{}:
			case <-ctx.Done():
				return providers.Balance{}, ctx.Err()
			}
		}
		balance, err := provider.Check(ctx, address)
		if limiter, ok := s.limits[chain]; ok {
			<-limiter
		}
		if err == nil {
			recovered := s.Health.Success(chain)
			_ = recovered
			return balance, nil
		}
		providerErr := asProviderError(err, chain)
		if !providerErr.Temporary || attempt == s.Config.MaxRetries {
			s.Health.Failure(chain)
			return providers.Balance{}, providerErr
		}
		delay := providerErr.RetryAfter
		if delay <= 0 {
			delay = s.Config.RetryBaseDelay * time.Duration(1<<attempt)
		}
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return providers.Balance{}, ctx.Err()
		}
	}
	return providers.Balance{}, errors.New("unreachable retry state")
}

func asProviderError(err error, fallback string) *providers.ProviderError {
	var providerErr *providers.ProviderError
	if errors.As(err, &providerErr) {
		return providerErr
	}
	return &providers.ProviderError{Code: "rpc_error", Provider: fallback, Temporary: false, Err: err}
}

func chainsFor(addressType string) []string {
	switch addressType {
	case domain.TypeEVM:
		return []string{domain.ChainEthereum, domain.ChainArbitrum, domain.ChainBSC}
	case domain.TypeBTC:
		return []string{domain.ChainBTC}
	case domain.TypeSOL:
		return []string{domain.ChainSolana}
	case domain.TypeTRX:
		return []string{domain.ChainTRON}
	default:
		return nil
	}
}
