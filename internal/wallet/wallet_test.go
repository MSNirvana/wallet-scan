package wallet

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/btcsuite/btcd/btcutil/base58"
	"github.com/tyler-smith/go-bip39"
)

const testMnemonic = "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about"

func TestPathConstants(t *testing.T) {
	if BTCPath != "m/84'/0'/0'/0/0" || ETHPath != "m/44'/60'/0'/0/0" ||
		SOLPath != "m/44'/501'/0'/0'" || TRXPath != "m/44'/195'/0'/0/0" {
		t.Fatal("wallet derivation paths changed")
	}
}

func TestResultJSONHasNoSecretKeyFields(t *testing.T) {
	encoded, err := json.Marshal(Result{Mnemonic: "example"})
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"private_key", "xprv", "xpub"} {
		if _, ok := decoded[forbidden]; ok {
			t.Fatalf("found %s", forbidden)
		}
	}
}

func TestGenerateWordCounts(t *testing.T) {
	for _, words := range []int{12, 24} {
		result, err := Generate(words)
		if err != nil {
			t.Fatalf("Generate(%d): %v", words, err)
		}
		if got := len(strings.Fields(result.Mnemonic)); got != words {
			t.Fatalf("got %d words, want %d", got, words)
		}
		if !bip39.IsMnemonicValid(result.Mnemonic) {
			t.Fatalf("generated mnemonic is invalid: %q", result.Mnemonic)
		}
	}
}

func TestGenerateRejectsUnsupportedWordCounts(t *testing.T) {
	for _, words := range []int{0, 11, 15, 18, 21, 23, 25} {
		if _, err := Generate(words); err == nil {
			t.Fatalf("Generate(%d) accepted unsupported word count", words)
		}
	}
}

func TestBIP39VectorSeed(t *testing.T) {
	want, err := hex.DecodeString("5eb00bbddcf069084889a8ab9155568165f5c453ccb85e70811aaed6f6da5fc19a5ac40b389cd370d086206dec8aa6c43daea6690f20ad3d8d48b2d2ce9e38e4")
	if err != nil {
		t.Fatal(err)
	}
	got := bip39.NewSeed(testMnemonic, "")
	if !bytes.Equal(got, want) {
		t.Fatalf("seed mismatch: got %x, want %x", got, want)
	}
}

func TestBIP39VectorSeedWithPassphrase(t *testing.T) {
	want, err := hex.DecodeString("c55257c360c07c72029aebc1b53c05ed0362ada38ead3e3e9efa3708e53495531f09a6987599d18264c1e1c92f2cf141630c7a3c4ab7c81b2f001698e7463b04")
	if err != nil {
		t.Fatal(err)
	}
	got := bip39.NewSeed(testMnemonic, "TREZOR")
	if !bytes.Equal(got, want) {
		t.Fatalf("seed mismatch: got %x, want %x", got, want)
	}
}

func TestKnownMnemonicAddresses(t *testing.T) {
	result, err := Derive(testMnemonic)
	if err != nil {
		t.Fatal(err)
	}
	checks := map[string]struct {
		got  string
		want string
	}{
		"BTC": {result.Addresses.BTC.Address, "bc1qcr8te4kr609gcawutmrza0j4xv80jy8z306fyu"},
		"ETH": {result.Addresses.ETH.Address, "0x9858EfFD232B4033E47d90003D41EC34EcaEda94"},
		"SOL": {result.Addresses.SOL.Address, "HAgk14JpMQLgt6rVgv7cBQFJWFto5Dqxi472uT3DKpqk"},
		"TRX": {result.Addresses.TRX.Address, "TUEZSdKsoDHQMeZwihtdoBiN46zxhGWYdH"},
	}
	for chain, check := range checks {
		if check.got != check.want {
			t.Errorf("%s address: got %s, want %s", chain, check.got, check.want)
		}
	}
	if result.Addresses.BTC.Path != BTCPath || result.Addresses.ETH.Path != ETHPath ||
		result.Addresses.SOL.Path != SOLPath || result.Addresses.TRX.Path != TRXPath {
		t.Fatal("derived address path does not match the declared path")
	}
}

func TestSolanaDerivationUsesAccountIndex(t *testing.T) {
	seed := bip39.NewSeed(testMnemonic, "")
	key0, err := deriveEd25519Hardened(seed, []uint32{44 | solanaHardened, 501 | solanaHardened, 0 | solanaHardened, 0 | solanaHardened})
	if err != nil {
		t.Fatal(err)
	}
	key1, err := deriveEd25519Hardened(seed, []uint32{44 | solanaHardened, 501 | solanaHardened, 1 | solanaHardened, 0 | solanaHardened})
	if err != nil {
		t.Fatal(err)
	}
	pub0 := ed25519.NewKeyFromSeed(key0).Public().(ed25519.PublicKey)
	pub1 := ed25519.NewKeyFromSeed(key1).Public().(ed25519.PublicKey)
	if base58.Encode(pub0) == base58.Encode(pub1) {
		t.Fatal("account index did not change the Solana address")
	}
	if decoded := base58.Decode(base58.Encode(pub0)); len(decoded) != ed25519.PublicKeySize {
		t.Fatalf("decoded public key length: %d", len(decoded))
	}
}

func TestEd25519DerivationRejectsNonHardenedIndex(t *testing.T) {
	seed := bip39.NewSeed(testMnemonic, "")
	if _, err := deriveEd25519Hardened(seed, []uint32{44}); err == nil {
		t.Fatal("accepted non-hardened index")
	}
}
