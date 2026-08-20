package importer

import (
	"encoding/csv"
	"fmt"
	"io"
	"regexp"
	"strings"

	"wallet-scan/internal/domain"
)

var evmPattern = regexp.MustCompile(`^0x[0-9a-fA-F]{40}$`)
var base58Pattern = regexp.MustCompile(`^[1-9A-HJ-NP-Za-km-z]+$`)

// ParseCSV validates and parses the public-address CSV format.
func ParseCSV(r io.Reader) ([]domain.AddressInput, error) {
	reader := csv.NewReader(r)
	reader.FieldsPerRecord = -1
	header, err := reader.Read()
	if err != nil {
		return nil, fmt.Errorf("read CSV header: %w", err)
	}
	if len(header) < 2 || strings.TrimSpace(header[0]) != "address_type" || strings.TrimSpace(header[1]) != "address" || len(header) > 3 || (len(header) == 3 && strings.TrimSpace(header[2]) != "label") {
		return nil, fmt.Errorf("CSV header must be address_type,address,label and must not contain secret fields")
	}
	seen := make(map[string]struct{})
	addresses := make([]domain.AddressInput, 0)
	line := 1
	for {
		line++
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read CSV line %d: %w", line, err)
		}
		if len(record) < 2 || len(record) > 3 {
			return nil, fmt.Errorf("CSV line %d must have 2 or 3 fields", line)
		}
		addressType := strings.ToLower(strings.TrimSpace(record[0]))
		address := strings.TrimSpace(record[1])
		label := ""
		if len(record) == 3 {
			label = strings.TrimSpace(record[2])
		}
		normalized, err := NormalizeAndValidate(addressType, address)
		if err != nil {
			return nil, fmt.Errorf("CSV line %d: %w", line, err)
		}
		key := addressType + "\x00" + normalized
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		addresses = append(addresses, domain.AddressInput{AddressType: addressType, Address: address, Normalized: normalized, Label: label})
	}
	return addresses, nil
}

// NormalizeAndValidate returns the database identity representation of an address.
func NormalizeAndValidate(addressType, address string) (string, error) {
	if address == "" || strings.ContainsAny(address, "\r\n\t ") {
		return "", fmt.Errorf("address is empty or contains whitespace")
	}
	switch addressType {
	case domain.TypeEVM:
		if !evmPattern.MatchString(address) {
			return "", fmt.Errorf("invalid EVM address")
		}
		return strings.ToLower(address), nil
	case domain.TypeBTC:
		if len(address) < 14 || len(address) > 90 || !(strings.HasPrefix(address, "1") || strings.HasPrefix(address, "3") || strings.HasPrefix(strings.ToLower(address), "bc1")) {
			return "", fmt.Errorf("invalid Bitcoin address prefix or length")
		}
		return address, nil
	case domain.TypeSOL:
		if len(address) < 32 || len(address) > 44 || !base58Pattern.MatchString(address) {
			return "", fmt.Errorf("invalid Solana address encoding")
		}
		return address, nil
	case domain.TypeTRX:
		if len(address) != 34 || !strings.HasPrefix(address, "T") || !base58Pattern.MatchString(address) {
			return "", fmt.Errorf("invalid TRON address encoding")
		}
		return address, nil
	default:
		return "", fmt.Errorf("unsupported address_type %q", addressType)
	}
}
