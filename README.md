# wallet-scan

`wallet-scan` is an internal, read-only native-balance scanner for public wallet addresses. It accepts CSV address imports, scans each imported address once, stores positive native balances in PostgreSQL, and sends positive findings to an Enterprise WeChat group bot.

The service does not accept seed phrases or private keys and does not sign or submit transactions. The first version does not discover ERC-20, TRC-20, SPL tokens, or NFTs.

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

The default service runs migrations and exposes the internal API. It does not automatically scan `wallet_addresses`; the legacy database scanner remains available through the explicit `scan` command below.

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

`scan`, `retry-failed`, `export-hits`, and `cleanup` are legacy database-mode operations. Do not run `scan` at the same time as a caller-driven API load unless you intentionally want both workloads to share the configured provider endpoints.

`/healthz` and `/status` are internal endpoints. A provider timeout, rate limit, or RPC error is never treated as a zero balance. Failed addresses remain in `retry_queue`; successful zero-balance addresses become eligible for deletion seven days after their completed scan. Positive findings are retained.

## Backups

Back up PostgreSQL before cleanup or server migration:

```bash
docker compose exec -T postgres pg_dump -U scanner wallet_scan > wallet-scan-backup.sql
```

## Public endpoints

The default providers are free public endpoints and are intentionally conservative. They have no production SLA and may rate-limit bulk scans. Replace the endpoint environment variables with dedicated providers for sustained workloads.
