package wallet

import (
	"crypto/ecdsa"

	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/btcutil/base58"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/tyler-smith/go-bip32"
)

var (
	btcDerivation = []uint32{84 + bip32.FirstHardenedChild, 0 + bip32.FirstHardenedChild, 0 + bip32.FirstHardenedChild, 0, 0}
	ethDerivation = []uint32{44 + bip32.FirstHardenedChild, 60 + bip32.FirstHardenedChild, 0 + bip32.FirstHardenedChild, 0, 0}
	trxDerivation = []uint32{44 + bip32.FirstHardenedChild, 195 + bip32.FirstHardenedChild, 0 + bip32.FirstHardenedChild, 0, 0}
)

func deriveSecpPrivate(seed []byte, path []uint32) (*ecdsa.PrivateKey, error) {
	key, err := bip32.NewMasterKey(seed)
	if err != nil {
		return nil, err
	}
	for _, index := range path {
		key, err = key.NewChildKey(index)
		if err != nil {
			return nil, err
		}
	}
	return crypto.ToECDSA(key.Key)
}

func deriveBTC(seed []byte) (string, error) {
	privateKey, err := deriveSecpPrivate(seed, btcDerivation)
	if err != nil {
		return "", err
	}
	compressedPublicKey := crypto.CompressPubkey(&privateKey.PublicKey)
	address, err := btcutil.NewAddressWitnessPubKeyHash(btcutil.Hash160(compressedPublicKey), &chaincfg.MainNetParams)
	if err != nil {
		return "", err
	}
	return address.EncodeAddress(), nil
}

func deriveETH(seed []byte) (string, error) {
	privateKey, err := deriveSecpPrivate(seed, ethDerivation)
	if err != nil {
		return "", err
	}
	return crypto.PubkeyToAddress(privateKey.PublicKey).Hex(), nil
}

func deriveTRX(seed []byte) (string, error) {
	privateKey, err := deriveSecpPrivate(seed, trxDerivation)
	if err != nil {
		return "", err
	}
	address := crypto.PubkeyToAddress(privateKey.PublicKey)
	return base58.CheckEncode(address.Bytes(), 0x41), nil
}
