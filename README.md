# wallet-scan

`wallet-scan` is an internal, read-only native-balance scanner for public wallet addresses. The service can perform one startup wallet generation and balance check, while the separate `generate` command remains available for offline use. The scanner accepts CSV address imports, scans each imported address once, stores positive native balances in PostgreSQL, and sends positive findings to an Enterprise WeChat group bot.

The scanner service does not accept seed phrases or private keys and does not sign or submit transactions. The startup check and `generate` command create a new mnemonic locally; the startup check clears it before persistence and never logs or sends it. The first version does not discover ERC-20, TRC-20, SPL tokens, or NFTs.

## Generate a wallet offline

Generate one 12-word English BIP-39 mnemonic and the first mainnet address for each supported chain:

```bash
wallet-scan generate
wallet-scan generate --words 24
wallet-scan generate --json
```

Without `--scan`, this command runs before configuration and database startup. It does not require `DATABASE_URL`, PostgreSQL, RPC endpoints, or network access. It uses the operating system secure random source, keeps the result in process memory, and prints only the mnemonic and public addresses. It does not print private keys, extended private keys, or xpubs. The BIP-39 passphrase is empty and cannot be supplied as a command-line argument.

The derivation paths are fixed to the first mainnet address:

| Chain | Address format | Derivation path |
| --- | --- | --- |
| BTC | Native SegWit `bc1q...` | `m/84'/0'/0'/0/0` |
| ETH | EIP-55 `0x...` | `m/44'/60'/0'/0/0` |
| Solana | Base58 | `m/44'/501'/0'/0'` |
| TRX | Base58Check `T...` | `m/44'/195'/0'/0/0` |

Solana wallets have historical path variations. This generator uses the path shown above and displays it in the output so the same path can be selected when restoring the wallet.

To query the generated public addresses through the existing synchronous balance API, opt in explicitly:

```bash
wallet-scan generate --scan \
  --api-url http://127.0.0.1:8080 \
  --api-key your-key
```

`--api-url` defaults to `http://127.0.0.1:8080`. `--api-key` defaults to `SCANNER_API_KEY`; using the environment variable avoids putting the key in shell history or the process list. The command sends six read-only requests: `btc/btc`, `evm/ethereum`, `evm/arbitrum`, `evm/bsc`, `sol/solana`, and `trx/tron`. It sends only public addresses and the API key header, never the mnemonic or private keys.

Scan output reports each API `state`, exact `balance_atomic` value, asset symbol, and `has_balance`. HTTP 429, 503, timeout, connection, or malformed-response results are shown per chain and make the command exit nonzero; a successful generation and any successful chain results are still retained in the output.

Treat the printed mnemonic as the wallet. Terminal scrollback, shell redirection, screen recording, logs, and clipboard history can retain it. Never paste it into chat, issue trackers, shell commands, or untrusted websites. For real funds, prefer a hardware wallet or audited wallet software and verify a receiving address with a small amount before use.

## Supported address types

| `address_type` | Networks queried | Native unit |
| --- | --- | --- |
| `evm` | Ethereum, Arbitrum, BSC | wei / ETH / BNB |
| `btc` | Bitcoin | satoshi |
| `sol` | Solana | lamport |
| `trx` | TRON | sun |

## Run with Compose

1. Copy `.env.example` to `.env` and set a long PostgreSQL password, `DATABASE_URL`, and `WECOM_WEBHOOK_URL`.
2. Keep PostgreSQL unpublished. Set `SCANNER_BIND_HOST` to `127.0.0.1` or the server's private address.
3. Start the service:

```bash
docker compose up -d --build
```

The default service runs migrations, generates one wallet, queries its six native-balance targets once, records the four public addresses and all six results, and exposes the internal API. It does not automatically run the legacy full-address scan. Set `GENERATE_WALLET_ON_STARTUP=false` to disable the startup task.

The startup task never stores the mnemonic, writes `positive_findings`, or sends balance notifications. A provider failure is recorded as a failed result and is never converted to zero balance. The task does not loop or automatically retry.

## Synchronous balance API

The service exposes a synchronous API for callers that already own the public-address list. This mode does not insert the submitted address into `wallet_addresses`; the caller owns result storage.

Set `SCANNER_API_KEY` when the service is reachable from any private-network peer. The default Compose binding is `127.0.0.1`, so an empty key is acceptable only for local testing.

Check the current dynamic recommendation:

```bash
curl -H "X-Internal-API-Key: your-key" \
  http://127.0.0.1:8080/v1/capacity
```

Query one native balance:

```bash
curl -sS -X POST \
  -H "Content-Type: application/json" \
  -H "X-Internal-API-Key: your-key" \
  -d '{"address_type":"evm","chain":"ethereum","address":"0x0000000000000000000000000000000000000001"}' \
  http://127.0.0.1:8080/v1/balance
```

A successful response has `state: "checked"`. `balance_atomic` is an exact integer in the chain's smallest unit and `has_balance` tells the caller whether it is positive. A zero result can be discarded by the caller; a positive result should be persisted by the caller.

The caller must retry `429`, `503`, timeouts, and other `state: "retry"` responses. `400` responses with `invalid_address` or `invalid_chain` are rejected inputs. A `429` response includes `Retry-After` and `retry_after_ms`.

Concurrency starts at `API_INITIAL_CONCURRENCY` (default `20`) and adapts from latency and retryable errors up to `API_MAX_IN_FLIGHT` (default `100`). `/v1/capacity` is a recommendation, not a guarantee; the server still enforces the hard limit. Use one caller-side semaphore per process and lower concurrency after rate limits.

Relevant API settings:

```env
SCANNER_API_KEY=
API_MAX_IN_FLIGHT=100
API_INITIAL_CONCURRENCY=20
API_QUEUE_WAIT_MS=250
API_ADJUST_INTERVAL_SECONDS=5
API_TARGET_LATENCY_MS=800
```

## Import addresses

The CSV header must be `address_type,address,label`. The optional label is shown in WeCom messages. Do not add secret columns.

```bash
docker compose exec scanner wallet-scan import --file /data/addresses.csv
```

Mount the input file into the scanner container or copy it into a mounted data directory before running the command.

## Operations

```bash
docker compose exec scanner wallet-scan status
docker compose exec scanner wallet-scan scan
docker compose exec scanner wallet-scan retry-failed
docker compose exec scanner wallet-scan export-hits
docker compose exec scanner wallet-scan cleanup
```

The startup wallet check runs once per `wallet-scan run` process start by default. It records results in `wallet_balance_checks`; this table is separate from `positive_findings` and does not affect the legacy scan counters.

`scan`, `retry-failed`, `export-hits`, and `cleanup` are legacy database-mode operations. Do not run `scan` at the same time as a caller-driven API load unless you intentionally want both workloads to share the configured provider endpoints.

`/healthz` and `/status` are internal endpoints. A provider timeout, rate limit, or RPC error is never treated as a zero balance. Failed addresses remain in `retry_queue`; successful zero-balance addresses become eligible for deletion seven days after their completed scan. Positive findings are retained.

## Backups

Back up PostgreSQL before cleanup or server migration:

```bash
docker compose exec -T postgres pg_dump -U scanner wallet_scan > wallet-scan-backup.sql
```

## Public endpoints

The default providers are free public endpoints and are intentionally conservative. They have no production SLA and may rate-limit bulk scans. Replace the endpoint environment variables with dedicated providers for sustained workloads.
