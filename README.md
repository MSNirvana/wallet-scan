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

The service runs migrations, resumes an unfinished scan, or creates one scan for the current address range. It does not restart completed scans automatically.

## Import addresses

The CSV header must be `address_type,address,label`. The optional label is shown in WeCom messages. Do not add secret columns.

```bash
docker compose exec scanner wallet-scan import --file /data/addresses.csv
```

Mount the input file into the scanner container or copy it into a mounted data directory before running the command.

## Operations

```bash
docker compose exec scanner wallet-scan status
docker compose exec scanner wallet-scan retry-failed
docker compose exec scanner wallet-scan export-hits
docker compose exec scanner wallet-scan cleanup
```

`/healthz` and `/status` are internal endpoints. A provider timeout, rate limit, or RPC error is never treated as a zero balance. Failed addresses remain in `retry_queue`; successful zero-balance addresses become eligible for deletion seven days after their completed scan. Positive findings are retained.

## Backups

Back up PostgreSQL before cleanup or server migration:

```bash
docker compose exec -T postgres pg_dump -U scanner wallet_scan > wallet-scan-backup.sql
```

## Public endpoints

The default providers are free public endpoints and are intentionally conservative. They have no production SLA and may rate-limit bulk scans. Replace the endpoint environment variables with dedicated providers for sustained workloads.
