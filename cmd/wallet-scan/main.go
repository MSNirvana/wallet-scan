package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/uuid"

	"wallet-scan/internal/config"
	"wallet-scan/internal/db"
	"wallet-scan/internal/httpserver"
	"wallet-scan/internal/importer"
	"wallet-scan/internal/maintenance"
	"wallet-scan/internal/notifications"
	"wallet-scan/internal/providers"
	"wallet-scan/internal/scanner"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		log.Fatal(err)
	}
}

func run(args []string) error {
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
		return fmt.Errorf("unknown command %q; use run, import, scan, status, retry-failed, export-hits, or cleanup", command)
	}
}

func retryFailed(ctx context.Context, store *db.Store, cfg config.Config) error {
	if cfg.WeComWebhookURL == "" {
		return fmt.Errorf("WECOM_WEBHOOK_URL is required for retry-failed")
	}
	service, outbox := buildScanner(store, cfg)
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
	service, outbox := buildScanner(store, cfg)
	service.Notifier = outbox
	return service.RunOnce(ctx)
}

func runService(ctx context.Context, store *db.Store, cfg config.Config) error {
	if cfg.WeComWebhookURL == "" {
		return fmt.Errorf("WECOM_WEBHOOK_URL is required for service mode")
	}
	service, outbox := buildScanner(store, cfg)
	service.Notifier = outbox
	statusServer := &httpserver.Server{Store: store}
	serverErrors := make(chan error, 1)
	go func() { serverErrors <- statusServer.ListenAndServe(ctx, cfg.BindAddress) }()
	if err := service.RunOnce(ctx); err != nil {
		log.Printf("scan stopped: %v", err)
	}
	cleanup := &maintenance.Cleaner{Store: store}
	outboxTicker := time.NewTicker(30 * time.Second)
	scanTicker := time.NewTicker(30 * time.Second)
	cleanupTicker := time.NewTicker(24 * time.Hour)
	defer outboxTicker.Stop()
	defer scanTicker.Stop()
	defer cleanupTicker.Stop()
	for {
		select {
		case err := <-serverErrors:
			return err
		case <-outboxTicker.C:
			if err := outbox.Drain(ctx, 100); err != nil {
				log.Printf("notification retry: %v", err)
			}
		case <-scanTicker.C:
			if err := service.RunOnce(ctx); err != nil {
				log.Printf("scan retry: %v", err)
			}
		case <-cleanupTicker.C:
			if err := runCleanupWith(cleanup, ctx, cfg); err != nil {
				log.Printf("cleanup: %v", err)
			}
		case <-ctx.Done():
			return nil
		}
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

func buildScanner(store *db.Store, cfg config.Config) (*scanner.Scanner, *notifications.Outbox) {
	httpClient := providers.NewHTTPClient(cfg.RequestTimeout)
	providerSet := map[string]providers.Provider{
		"btc":      providers.NewBTCProvider(httpClient, cfg.Provider.BTCURL),
		"ethereum": providers.NewEVMProvider(httpClient, cfg.Provider.ETHURL, "ethereum", "ETH"),
		"arbitrum": providers.NewEVMProvider(httpClient, cfg.Provider.ARBURL, "arbitrum", "ETH"),
		"bsc":      providers.NewEVMProvider(httpClient, cfg.Provider.BSCURL, "bsc", "BNB"),
		"solana":   providers.NewSolanaProvider(httpClient, cfg.Provider.SOLURL),
		"tron":     providers.NewTRONProvider(httpClient, cfg.Provider.TRONURL),
	}
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
	return service, outbox
}
