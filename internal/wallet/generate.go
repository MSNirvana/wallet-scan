package wallet

import (
	"fmt"

	"github.com/tyler-smith/go-bip39"
)

// Generate creates a BIP-39 mnemonic from the operating system's secure random source.
func Generate(words int) (Result, error) {
	bits, err := entropyBits(words)
	if err != nil {
		return Result{}, err
	}
	entropy, err := bip39.NewEntropy(bits)
	if err != nil {
		return Result{}, fmt.Errorf("generate secure entropy: %w", err)
	}
	mnemonic, err := bip39.NewMnemonic(entropy)
	if err != nil {
		return Result{}, fmt.Errorf("create BIP-39 mnemonic: %w", err)
	}
	return Derive(mnemonic)
}

func entropyBits(words int) (int, error) {
	switch words {
	case 12:
		return 128, nil
	case 24:
		return 256, nil
	default:
		return 0, fmt.Errorf("--words must be 12 or 24, got %d", words)
	}
}

// Derive derives the first address for each supported mainnet from a BIP-39 mnemonic.
func Derive(mnemonic string) (Result, error) {
	if !bip39.IsMnemonicValid(mnemonic) {
		return Result{}, fmt.Errorf("invalid BIP-39 mnemonic")
	}
	seed := bip39.NewSeed(mnemonic, "")

	btc, err := deriveBTC(seed)
	if err != nil {
		return Result{}, fmt.Errorf("derive BTC address: %w", err)
	}
	eth, err := deriveETH(seed)
	if err != nil {
		return Result{}, fmt.Errorf("derive ETH address: %w", err)
	}
	sol, err := deriveSOL(seed)
	if err != nil {
		return Result{}, fmt.Errorf("derive Solana address: %w", err)
	}
	trx, err := deriveTRX(seed)
	if err != nil {
		return Result{}, fmt.Errorf("derive TRX address: %w", err)
	}

	return Result{
		Mnemonic: mnemonic,
		Addresses: Addresses{
			BTC: Address{Address: btc, Path: BTCPath},
			ETH: Address{Address: eth, Path: ETHPath},
			SOL: Address{Address: sol, Path: SOLPath},
			TRX: Address{Address: trx, Path: TRXPath},
		},
	}, nil
}
