package wallet

import (
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/binary"
	"fmt"

	"github.com/btcsuite/btcd/btcutil/base58"
)

const solanaHardened = uint32(1 << 31)

func deriveSOL(seed []byte) (string, error) {
	key, err := deriveEd25519Hardened(seed, []uint32{
		44 | solanaHardened,
		501 | solanaHardened,
		0 | solanaHardened,
		0 | solanaHardened,
	})
	if err != nil {
		return "", err
	}
	privateKey := ed25519.NewKeyFromSeed(key)
	publicKey := privateKey.Public().(ed25519.PublicKey)
	return base58.Encode(publicKey), nil
}

func deriveEd25519Hardened(seed []byte, path []uint32) ([]byte, error) {
	state := hmacSHA512([]byte("ed25519 seed"), seed)
	key, chainCode := state[:32], state[32:]
	for _, index := range path {
		if index < solanaHardened {
			return nil, fmt.Errorf("ed25519 path contains non-hardened index %d", index)
		}
		message := make([]byte, 0, 37)
		message = append(message, 0)
		message = append(message, key...)
		var encodedIndex [4]byte
		binary.BigEndian.PutUint32(encodedIndex[:], index)
		message = append(message, encodedIndex[:]...)
		state = hmacSHA512(chainCode, message)
		key, chainCode = state[:32], state[32:]
	}
	return append([]byte(nil), key...), nil
}

func hmacSHA512(key, data []byte) []byte {
	mac := hmac.New(sha512.New, key)
	_, _ = mac.Write(data)
	return mac.Sum(nil)
}
