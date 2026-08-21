package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"

	"wallet-scan/internal/adaptive"
	"wallet-scan/internal/balanceclient"
	"wallet-scan/internal/config"
	"wallet-scan/internal/db"
	"wallet-scan/internal/httpserver"
	"wallet-scan/internal/importer"
	"wallet-scan/internal/maintenance"
	"wallet-scan/internal/notifications"
	"wallet-scan/internal/providers"
	"wallet-scan/internal/scanner"
	"wallet-scan/internal/startupcheck"
	"wallet-scan/internal/wallet"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		log.Fatal(err)
	}
}

func run(args []string) error {
	return runWithIO(args, os.Stdout, os.Stderr)
}

func runWithIO(args []string, stdout, stderr io.Writer) error {
	if len(args) > 0 && args[0] == "generate" {
		return generateCommand(args[1:], stdout, stderr)
	}
	configValue, err := config.Load()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	store, err := db.Open(ctx, configValue.DatabaseURL)
	if err != nil {
		return err
	}
	defer store.Close()
	if err := store.Migrate(ctx); err != nil {
		return err
	}
	command := "run"
	if len(args) > 0 {
		command = args[0]
		args = args[1:]
	}
	switch command {
	case "import":
		return importCSV(ctx, store, args)
	case "scan":
		return runScan(ctx, store, configValue)
	case "status":
		return printStatus(ctx, store)
	case "retry-failed":
		return retryFailed(ctx, store, configValue)
	case "export-hits":
		return store.ExportFindings(ctx, os.Stdout)
	case "cleanup":
		return runCleanup(ctx, store, configValue)
	case "run":
		return runService(ctx, store, configValue)
	default:
		return fmt.Errorf("unknown command %q; use generate, run, import, scan, status, retry-failed, export-hits, or cleanup", command)
	}
}

func generateCommand(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("generate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	words := flags.Int("words", 12, "mnemonic word count: 12 or 24")
	jsonOutput := flags.Bool("json", false, "write machine-readable JSON")
	scan := flags.Bool("scan", false, "query generated addresses through wallet-scan")
	apiURL := flags.String("api-url", "http://127.0.0.1:8080", "wallet-scan API base URL")
	apiKey := flags.String("api-key", os.Getenv("SCANNER_API_KEY"), "wallet-scan API key; prefer SCANNER_API_KEY")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("generate does not accept positional arguments")
	}
	var client *balanceclient.Client
	if *scan {
		var err error
		client, err = balanceclient.New(*apiURL, *apiKey, nil)
		if err != nil {
			return err
		}
	}
	result, err := wallet.Generate(*words)
	if err != nil {
		return err
	}
	var scanResults []balanceclient.Result
	var scanErr error
	if client != nil {
		scanResults, scanErr = scanGeneratedWallet(context.Background(), client, result)
	}
	if *jsonOutput {
		if client == nil {
			encoder := json.NewEncoder(stdout)
			encoder.SetIndent("", "  ")
			return encoder.Encode(result)
		}
		if err := writeJSON(stdout, generatedOutput{Mnemonic: result.Mnemonic, Addresses: result.Addresses, Scan: scanResults}); err != nil {
			return err
		}
		return scanErr
	}
	if err := writeHumanResult(stdout, result); err != nil {
		return err
	}
	if client == nil {
		return nil
	}
	if err := writeScanText(stdout, scanResults); err != nil {
		return err
	}
	return scanErr
}

type generatedOutput struct {
	Mnemonic  string                 `json:"mnemonic"`
	Addresses wallet.Addresses       `json:"addresses"`
	Scan      []balanceclient.Result `json:"scan"`
}

func writeJSON(stdout io.Writer, output generatedOutput) error {
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(output)
}

func scanGeneratedWallet(ctx context.Context, client *balanceclient.Client, result wallet.Result) ([]balanceclient.Result, error) {
	queries := []balanceclient.Query{
		{AddressType: "btc", Chain: "btc", Address: result.Addresses.BTC.Address},
		{AddressType: "evm", Chain: "ethereum", Address: result.Addresses.ETH.Address},
		{AddressType: "evm", Chain: "arbitrum", Address: result.Addresses.ETH.Address},
		{AddressType: "evm", Chain: "bsc", Address: result.Addresses.ETH.Address},
		{AddressType: "sol", Chain: "solana", Address: result.Addresses.SOL.Address},
		{AddressType: "trx", Chain: "tron", Address: result.Addresses.TRX.Address},
	}
	results := make([]balanceclient.Result, len(queries))
	errorsByIndex := make([]error, len(queries))
	var waitGroup sync.WaitGroup
	waitGroup.Add(len(queries))
	for index, query := range queries {
		go func(index int, query balanceclient.Query) {
			defer waitGroup.Done()
			results[index], errorsByIndex[index] = client.Check(ctx, query)
		}(index, query)
	}
	waitGroup.Wait()

	failed := make([]string, 0)
	for index, result := range results {
		if errorsByIndex[index] != nil || result.State != "checked" {
			state := result.State
			if state == "" {
				state = "client_error"
			}
			failed = append(failed, result.Chain+":"+state)
		}
	}
	if len(failed) > 0 {
		return results, fmt.Errorf("balance scan failed for %d/%d queries: %s", len(failed), len(results), strings.Join(failed, ", "))
	}
	return results, nil
}

func writeScanText(stdout io.Writer, results []balanceclient.Result) error {
	if _, err := fmt.Fprintln(stdout, "\nBalance checks:"); err != nil {
		return err
	}
	for _, result := range results {
		if result.State != "checked" {
			if _, err := fmt.Fprintf(stdout, "%-8s %-9s %s: %s\n", result.AddressType, result.Chain, result.State, result.ErrorCode); err != nil {
				return err
			}
			continue
		}
		status := "zero"
		if result.HasBalance {
			status = "positive"
		}
		if _, err := fmt.Fprintf(stdout, "%-8s %-9s %s %s (%s, %s)\n", result.AddressType, result.Chain, result.BalanceAtomic, atomicUnit(result.Chain), result.AssetSymbol, status); err != nil {
			return err
		}
	}
	return nil
}

func atomicUnit(chain string) string {
	switch chain {
	case "btc":
		return "satoshi"
	case "ethereum", "arbitrum", "bsc":
		return "wei"
	case "solana":
		return "lamport"
	case "tron":
		return "sun"
	default:
		return "atomic units"
	}
}

func writeHumanResult(stdout io.Writer, result wallet.Result) error {
	if _, err := fmt.Fprintf(stdout, "Mnemonic: %s\n\n", result.Mnemonic); err != nil {
		return err
	}
	for _, item := range []struct {
		name    string
		address wallet.Address
	}{
		{name: "BTC", address: result.Addresses.BTC},
		{name: "ETH", address: result.Addresses.ETH},
		{name: "SOL", address: result.Addresses.SOL},
		{name: "TRX", address: result.Addresses.TRX},
	} {
		if _, err := fmt.Fprintf(stdout, "%-3s %-19s %s\n", item.name, item.address.Path, item.address.Address); err != nil {
			return err
		}
	}
	return nil
}

func retryFailed(ctx context.Context, store *db.Store, cfg config.Config) error {
	if cfg.WeComWebhookURL == "" {
		return fmt.Errorf("WECOM_WEBHOOK_URL is required for retry-failed")
	}
	service, outbox, _ := buildScanner(store, cfg)
	service.Notifier = outbox
	for {
		before, err := store.ReadStatus(ctx)
		if err != nil {
			return err
		}
		if before.RetryCount == 0 {
			return nil
		}
		if err := service.RetryFailed(ctx, cfg.BatchSize); err != nil {
			return err
		}
		if err := outbox.Drain(ctx, cfg.BatchSize); err != nil {
			log.Printf("notification retry: %v", err)
		}
		after, err := store.ReadStatus(ctx)
		if err != nil {
			return err
		}
		if after.RetryCount >= before.RetryCount {
			ready, err := store.NextRetries(ctx, 1)
			if err != nil {
				return err
			}
			if len(ready) == 0 {
				return nil
			}
			return fmt.Errorf("retry queue made no progress")
		}
	}
}

func importCSV(ctx context.Context, store *db.Store, args []string) error {
	flags := flag.NewFlagSet("import", flag.ContinueOnError)
	path := flags.String("file", "", "CSV file path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *path == "" {
		return fmt.Errorf("import requires --file")
	}
	file, err := os.Open(*path)
	if err != nil {
		return fmt.Errorf("open import file: %w", err)
	}
	defer file.Close()
	addresses, err := importer.ParseCSV(file)
	if err != nil {
		return err
	}
	inserted, err := store.InsertAddresses(ctx, uuid.New(), addresses)
	if err != nil {
		return err
	}
	log.Printf("imported %d new addresses from %s", inserted, *path)
	return nil
}

func printStatus(ctx context.Context, store *db.Store) error {
	status, err := store.ReadStatus(ctx)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(status)
}

func runScan(ctx context.Context, store *db.Store, cfg config.Config) error {
	if cfg.WeComWebhookURL == "" {
		return fmt.Errorf("WECOM_WEBHOOK_URL is required for scan")
	}
	service, outbox, _ := buildScanner(store, cfg)
	service.Notifier = outbox
	return service.RunOnce(ctx)
}

func runService(ctx context.Context, store *db.Store, cfg config.Config) error {
	providerSet := buildProviders(cfg)
	apiLimiter := adaptive.New(adaptive.Config{
		InitialConcurrency: cfg.APIInitialConcurrency,
		MaxConcurrency:     cfg.APIMaxInFlight,
		QueueWait:          cfg.APIQueueWait,
		AdjustInterval:     cfg.APIAdjustInterval,
		TargetLatency:      cfg.APITargetLatency,
	})
	for chain := range providerSet {
		apiLimiter.Register(chain)
	}
	if cfg.GenerateWalletOnStartup {
		startup := startupcheck.New(store, providerSet, apiLimiter)
		if err := startup.RunOnce(ctx); err != nil {
			log.Printf("startup wallet check failed: %v", err)
		}
	}
	balanceService := &httpserver.BalanceService{
		Providers:      providerSet,
		Limiter:        apiLimiter,
		APIKey:         cfg.APIKey,
		RequestTimeout: cfg.RequestTimeout,
	}
	statusServer := &httpserver.Server{Store: store, Balance: balanceService}
	serverErrors := make(chan error, 1)
	go func() { serverErrors <- statusServer.ListenAndServe(ctx, cfg.BindAddress) }()
	select {
	case err := <-serverErrors:
		return err
	case <-ctx.Done():
		return nil
	}
}

func runCleanup(ctx context.Context, store *db.Store, cfg config.Config) error {
	return runCleanupWith(&maintenance.Cleaner{Store: store}, ctx, cfg)
}

func runCleanupWith(cleaner *maintenance.Cleaner, ctx context.Context, cfg config.Config) error {
	cutoff := time.Now().Add(-time.Duration(cfg.EmptyRetentionDays) * 24 * time.Hour)
	for {
		deleted, err := cleaner.Run(ctx, cutoff, cfg.BatchSize)
		if err != nil {
			return err
		}
		if deleted == 0 {
			return nil
		}
		log.Printf("cleaned %d empty addresses", deleted)
	}
}

func buildScanner(store *db.Store, cfg config.Config) (*scanner.Scanner, *notifications.Outbox, map[string]providers.Provider) {
	providerSet := buildProviders(cfg)
	limits := map[string]int{
		"btc": cfg.Provider.BTCConcurrency, "ethereum": cfg.Provider.ETHConcurrency,
		"arbitrum": cfg.Provider.ARBConcurrency, "bsc": cfg.Provider.BSCConcurrency,
		"solana": cfg.Provider.SOLConcurrency, "tron": cfg.Provider.TRONConcurrency,
	}
	service := scanner.New(store, providerSet, scanner.Config{
		BatchSize: cfg.BatchSize, AddressWorkers: cfg.AddressWorkers, MaxRetries: cfg.MaxRetries,
		RetryBaseDelay: cfg.RetryBaseDelay, NodeFailureThreshold: cfg.NodeFailureThreshold,
	}, limits)
	outbox := &notifications.Outbox{Store: store, Client: notifications.NewWeComClient(cfg.WeComWebhookURL, cfg.RequestTimeout)}
	return service, outbox, providerSet
}

func buildProviders(cfg config.Config) map[string]providers.Provider {
	httpClient := providers.NewHTTPClient(cfg.RequestTimeout)
	return map[string]providers.Provider{
		"btc":      providers.NewBTCProvider(httpClient, cfg.Provider.BTCURL),
		"ethereum": providers.NewEVMProvider(httpClient, cfg.Provider.ETHURL, "ethereum", "ETH"),
		"arbitrum": providers.NewEVMProvider(httpClient, cfg.Provider.ARBURL, "arbitrum", "ETH"),
		"bsc":      providers.NewEVMProvider(httpClient, cfg.Provider.BSCURL, "bsc", "BNB"),
		"solana":   providers.NewSolanaProvider(httpClient, cfg.Provider.SOLURL),
		"tron":     providers.NewTRONProvider(httpClient, cfg.Provider.TRONURL),
	}
}
