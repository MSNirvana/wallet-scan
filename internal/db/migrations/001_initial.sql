CREATE TABLE IF NOT EXISTS wallet_addresses (
    id BIGSERIAL PRIMARY KEY,
    address_type TEXT NOT NULL CHECK (address_type IN ('evm', 'btc', 'sol', 'trx')),
    address TEXT NOT NULL,
    normalized_address TEXT NOT NULL,
    label TEXT NOT NULL DEFAULT '',
    import_batch_id UUID NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (address_type, normalized_address)
);

CREATE TABLE IF NOT EXISTS scan_runs (
    id UUID PRIMARY KEY,
    start_id BIGINT NOT NULL,
    end_id BIGINT NOT NULL,
    cursor_id BIGINT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('running', 'paused', 'completed', 'failed')),
    processed_count BIGINT NOT NULL DEFAULT 0,
    empty_count BIGINT NOT NULL DEFAULT 0,
    positive_count BIGINT NOT NULL DEFAULT 0,
    error_count BIGINT NOT NULL DEFAULT 0,
    started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ,
    CHECK (start_id >= 1),
    CHECK (end_id >= start_id),
    CHECK (cursor_id <= end_id)
);

CREATE TABLE IF NOT EXISTS positive_findings (
    id BIGSERIAL PRIMARY KEY,
    address_id BIGINT NOT NULL REFERENCES wallet_addresses(id) ON DELETE RESTRICT,
    chain TEXT NOT NULL CHECK (chain IN ('btc', 'ethereum', 'arbitrum', 'bsc', 'solana', 'tron')),
    balance_atomic NUMERIC(78, 0) NOT NULL CHECK (balance_atomic > 0),
    asset_symbol TEXT NOT NULL,
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    notified_at TIMESTAMPTZ,
    UNIQUE (address_id, chain)
);

CREATE TABLE IF NOT EXISTS retry_queue (
    id BIGSERIAL PRIMARY KEY,
    address_id BIGINT NOT NULL REFERENCES wallet_addresses(id) ON DELETE CASCADE,
    chain TEXT NOT NULL,
    error_code TEXT NOT NULL,
    provider TEXT NOT NULL,
    attempts INTEGER NOT NULL DEFAULT 0,
    next_retry_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_error TEXT NOT NULL,
    resolved_at TIMESTAMPTZ,
    UNIQUE (address_id, chain)
);

CREATE TABLE IF NOT EXISTS node_incidents (
    id BIGSERIAL PRIMARY KEY,
    chain TEXT NOT NULL,
    provider TEXT NOT NULL,
    error_code TEXT NOT NULL,
    consecutive_failures INTEGER NOT NULL DEFAULT 0,
    status TEXT NOT NULL CHECK (status IN ('active', 'recovered')),
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    recovered_at TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS notification_events (
    id BIGSERIAL PRIMARY KEY,
    finding_id BIGINT REFERENCES positive_findings(id) ON DELETE CASCADE,
    node_incident_id BIGINT REFERENCES node_incidents(id) ON DELETE CASCADE,
    event_type TEXT NOT NULL CHECK (event_type IN ('first_positive', 'balance_changed', 'node_error')),
    status TEXT NOT NULL CHECK (status IN ('pending', 'sent', 'failed')),
    attempts INTEGER NOT NULL DEFAULT 0,
    last_error TEXT,
    sent_at TIMESTAMPTZ,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE notification_events ADD COLUMN IF NOT EXISTS next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now();

CREATE INDEX IF NOT EXISTS idx_wallet_addresses_id ON wallet_addresses(id);
CREATE INDEX IF NOT EXISTS idx_scan_runs_status ON scan_runs(status);
CREATE INDEX IF NOT EXISTS idx_positive_findings_address ON positive_findings(address_id);
CREATE INDEX IF NOT EXISTS idx_retry_queue_next_retry ON retry_queue(next_retry_at);
CREATE INDEX IF NOT EXISTS idx_retry_queue_address ON retry_queue(address_id);
CREATE INDEX IF NOT EXISTS idx_notification_events_pending ON notification_events(status, created_at);
CREATE INDEX IF NOT EXISTS idx_node_incidents_active ON node_incidents(status, chain);
