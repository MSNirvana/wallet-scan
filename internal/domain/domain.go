package domain

import "github.com/google/uuid"

const (
	TypeEVM = "evm"
	TypeBTC = "btc"
	TypeSOL = "sol"
	TypeTRX = "trx"

	ChainBTC      = "btc"
	ChainEthereum = "ethereum"
	ChainArbitrum = "arbitrum"
	ChainBSC      = "bsc"
	ChainSolana   = "solana"
	ChainTRON     = "tron"
)

// AddressInput is the public data accepted by the importer.
type AddressInput struct {
	AddressType string
	Address     string
	Normalized  string
	Label       string
}

// Address is a stored public address.
type Address struct {
	ID          int64
	AddressType string
	Address     string
	Label       string
}

// ScanRun is a durable one-time scan checkpoint.
type ScanRun struct {
	ID             uuid.UUID
	StartID        int64
	EndID          int64
	CursorID       int64
	Status         string
	ProcessedCount int64
	EmptyCount     int64
	PositiveCount  int64
	ErrorCount     int64
}
