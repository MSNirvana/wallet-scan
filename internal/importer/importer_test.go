package importer

import (
	"strings"
	"testing"
)

func TestParseCSVValidatesAndDeduplicates(t *testing.T) {
	input := "address_type,address,label\n" +
		"evm,0x0000000000000000000000000000000000000001,one\n" +
		"evm,0x0000000000000000000000000000000000000001,two\n" +
		"trx,TQF5mJ7vW8gGv7xXzJx7S5m8Pj8X4B2D7A,label\n"
	rows, err := ParseCSV(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	if rows[0].Normalized != "0x0000000000000000000000000000000000000001" {
		t.Fatalf("unexpected EVM normalization: %s", rows[0].Normalized)
	}
}

func TestParseCSVRejectsSecretColumns(t *testing.T) {
	_, err := ParseCSV(strings.NewReader("address_type,address,private_key\nevm,0x0000000000000000000000000000000000000001,secret\n"))
	if err == nil {
		t.Fatal("expected secret column rejection")
	}
}

func TestNormalizeAndValidateRejectsInvalidEVM(t *testing.T) {
	if _, err := NormalizeAndValidate("evm", "0x123"); err == nil {
		t.Fatal("expected invalid EVM error")
	}
}
