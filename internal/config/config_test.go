package config

import "testing"

func setValidConfigEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("DATABASE_URL", "postgres://scanner:password@localhost:5432/wallet_scan")
	t.Setenv("BIND_ADDRESS", "127.0.0.1:8080")
	t.Setenv("SCAN_MODE", "once")
}

func TestLoadEnablesStartupWalletGenerationByDefault(t *testing.T) {
	setValidConfigEnvironment(t)
	t.Setenv("GENERATE_WALLET_ON_STARTUP", "")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.GenerateWalletOnStartup {
		t.Fatal("startup wallet generation is disabled by default")
	}
}

func TestLoadCanDisableStartupWalletGeneration(t *testing.T) {
	setValidConfigEnvironment(t)
	t.Setenv("GENERATE_WALLET_ON_STARTUP", "false")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GenerateWalletOnStartup {
		t.Fatal("startup wallet generation remains enabled")
	}
}

func TestLoadRejectsInvalidStartupWalletGenerationValue(t *testing.T) {
	setValidConfigEnvironment(t)
	t.Setenv("GENERATE_WALLET_ON_STARTUP", "sometimes")

	if _, err := Load(); err == nil {
		t.Fatal("expected invalid boolean error")
	}
}
