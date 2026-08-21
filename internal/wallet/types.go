package wallet

const (
	BTCPath = "m/84'/0'/0'/0/0"
	ETHPath = "m/44'/60'/0'/0/0"
	SOLPath = "m/44'/501'/0'/0'"
	TRXPath = "m/44'/195'/0'/0/0"
)

type Address struct {
	Address string `json:"address"`
	Path    string `json:"path"`
}

type Addresses struct {
	BTC Address `json:"btc"`
	ETH Address `json:"eth"`
	SOL Address `json:"sol"`
	TRX Address `json:"trx"`
}

type Result struct {
	Mnemonic  string    `json:"mnemonic"`
	Addresses Addresses `json:"addresses"`
}
