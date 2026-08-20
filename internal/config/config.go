package config

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"time"
)

// Config contains runtime configuration for the scanner service.
type Config struct {
	DatabaseURL          string
	BindAddress          string
	WeComWebhookURL      string
	ScanMode             string
	BatchSize            int
	MaxRetries           int
	EmptyRetentionDays   int
	RequestTimeout       time.Duration
	AddressWorkers       int
	NodeFailureThreshold int
	RetryBaseDelay       time.Duration
	Provider             ProviderConfig
}

// ProviderConfig contains endpoint and per-chain concurrency settings.
type ProviderConfig struct {
	BTCURL          string
	ETHURL          string
	ARBURL          string
	BSCURL          string
	SOLURL          string
	TRONURL         string
	BTCConcurrency  int
	ETHConcurrency  int
	ARBConcurrency  int
	BSCConcurrency  int
	SOLConcurrency  int
	TRONConcurrency int
}

// Load reads environment variables and validates required values.
func Load() (Config, error) {
	c := Config{
		DatabaseURL:          os.Getenv("DATABASE_URL"),
		BindAddress:          envString("BIND_ADDRESS", "0.0.0.0:8080"),
		WeComWebhookURL:      os.Getenv("WECOM_WEBHOOK_URL"),
		ScanMode:             envString("SCAN_MODE", "once"),
		BatchSize:            envInt("BATCH_SIZE", 100),
		MaxRetries:           envInt("MAX_RETRIES", 3),
		EmptyRetentionDays:   envInt("EMPTY_RETENTION_DAYS", 7),
		RequestTimeout:       time.Duration(envInt("REQUEST_TIMEOUT_SECONDS", 12)) * time.Second,
		AddressWorkers:       envInt("ADDRESS_WORKERS", 8),
		NodeFailureThreshold: envInt("NODE_FAILURE_THRESHOLD", 5),
		RetryBaseDelay:       time.Duration(envInt("RETRY_BASE_DELAY_SECONDS", 2)) * time.Second,
		Provider: ProviderConfig{
			BTCURL:          envString("BTC_API_URL", "https://mempool.space/api"),
			ETHURL:          envString("ETH_RPC_URL", "https://ethereum-rpc.publicnode.com"),
			ARBURL:          envString("ARB_RPC_URL", "https://arb1.arbitrum.io/rpc"),
			BSCURL:          envString("BSC_RPC_URL", "https://bsc-dataseed.bnbchain.org"),
			SOLURL:          envString("SOL_RPC_URL", "https://solana-rpc.publicnode.com"),
			TRONURL:         envString("TRON_API_URL", "https://api.trongrid.io"),
			BTCConcurrency:  envInt("BTC_CONCURRENCY", 2),
			ETHConcurrency:  envInt("ETH_CONCURRENCY", 4),
			ARBConcurrency:  envInt("ARB_CONCURRENCY", 4),
			BSCConcurrency:  envInt("BSC_CONCURRENCY", 4),
			SOLConcurrency:  envInt("SOL_CONCURRENCY", 2),
			TRONConcurrency: envInt("TRON_CONCURRENCY", 2),
		},
	}
	if c.DatabaseURL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL is required")
	}
	if c.ScanMode != "once" {
		return Config{}, fmt.Errorf("SCAN_MODE must be once, got %q", c.ScanMode)
	}
	if c.BatchSize <= 0 || c.BatchSize > 10000 {
		return Config{}, fmt.Errorf("BATCH_SIZE must be between 1 and 10000")
	}
	if c.MaxRetries < 0 || c.MaxRetries > 20 {
		return Config{}, fmt.Errorf("MAX_RETRIES must be between 0 and 20")
	}
	if c.EmptyRetentionDays < 1 {
		return Config{}, fmt.Errorf("EMPTY_RETENTION_DAYS must be at least 1")
	}
	if c.RequestTimeout <= 0 {
		return Config{}, fmt.Errorf("REQUEST_TIMEOUT_SECONDS must be positive")
	}
	if c.AddressWorkers <= 0 || c.AddressWorkers > 256 {
		return Config{}, fmt.Errorf("ADDRESS_WORKERS must be between 1 and 256")
	}
	if c.NodeFailureThreshold < 1 || c.NodeFailureThreshold > 100 {
		return Config{}, fmt.Errorf("NODE_FAILURE_THRESHOLD must be between 1 and 100")
	}
	if c.RetryBaseDelay <= 0 {
		return Config{}, fmt.Errorf("RETRY_BASE_DELAY_SECONDS must be positive")
	}
	if _, _, err := net.SplitHostPort(c.BindAddress); err != nil {
		return Config{}, fmt.Errorf("BIND_ADDRESS must be host:port: %w", err)
	}
	return c, nil
}

func envString(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func envInt(name string, fallback int) int {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}
