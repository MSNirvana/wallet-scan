package db

import (
	"context"
	"embed"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"wallet-scan/internal/domain"
)

//go:embed migrations/001_initial.sql
var migrationFS embed.FS

// Store owns PostgreSQL access for durable scanner state.
type Store struct {
	Pool *pgxpool.Pool
}

// Open connects to PostgreSQL and waits until the connection is usable.
func Open(ctx context.Context, databaseURL string) (*Store, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("create database pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return &Store{Pool: pool}, nil
}

// Close releases all database connections.
func (s *Store) Close() {
	s.Pool.Close()
}

// Migrate applies the embedded schema idempotently.
func (s *Store) Migrate(ctx context.Context) error {
	schema, err := migrationFS.ReadFile("migrations/001_initial.sql")
	if err != nil {
		return fmt.Errorf("read migration: %w", err)
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin migration: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, string(schema)); err != nil {
		return fmt.Errorf("apply migration: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit migration: %w", err)
	}
	return nil
}

// InsertAddresses inserts a batch and returns the number of new rows.
func (s *Store) InsertAddresses(ctx context.Context, batchID uuid.UUID, addresses []domain.AddressInput) (int, error) {
	if len(addresses) == 0 {
		return 0, nil
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin address import: %w", err)
	}
	defer tx.Rollback(ctx)
	inserted := 0
	for _, address := range addresses {
		result, err := tx.Exec(ctx, `
			INSERT INTO wallet_addresses (address_type, address, normalized_address, label, import_batch_id)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (address_type, normalized_address) DO NOTHING`,
			address.AddressType, address.Address, address.Normalized, address.Label, batchID)
		if err != nil {
			return 0, fmt.Errorf("insert address: %w", err)
		}
		inserted += int(result.RowsAffected())
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit address import: %w", err)
	}
	return inserted, nil
}

// AddressRange returns the current source range, or false when empty.
func (s *Store) AddressRange(ctx context.Context) (int64, int64, bool, error) {
	var minID, maxID *int64
	if err := s.Pool.QueryRow(ctx, "SELECT min(id), max(id) FROM wallet_addresses").Scan(&minID, &maxID); err != nil {
		return 0, 0, false, fmt.Errorf("read address range: %w", err)
	}
	if minID == nil || maxID == nil {
		return 0, 0, false, nil
	}
	return *minID, *maxID, true, nil
}

// UnscannedRange returns the next address range after the latest completed run.
func (s *Store) UnscannedRange(ctx context.Context) (int64, int64, bool, error) {
	var lastEnd int64
	if err := s.Pool.QueryRow(ctx, "SELECT COALESCE(max(end_id), 0) FROM scan_runs WHERE status = 'completed'").Scan(&lastEnd); err != nil {
		return 0, 0, false, fmt.Errorf("read completed scan range: %w", err)
	}
	var startID, endID *int64
	if err := s.Pool.QueryRow(ctx, "SELECT min(id), max(id) FROM wallet_addresses WHERE id > $1", lastEnd).Scan(&startID, &endID); err != nil {
		return 0, 0, false, fmt.Errorf("read unscanned range: %w", err)
	}
	if startID == nil || endID == nil {
		return 0, 0, false, nil
	}
	return *startID, *endID, true, nil
}

// ActiveScan returns the current run, if one exists.
func (s *Store) ActiveScan(ctx context.Context) (*domain.ScanRun, error) {
	row := s.Pool.QueryRow(ctx, `
		SELECT id, start_id, end_id, cursor_id, status, processed_count, empty_count, positive_count, error_count
		FROM scan_runs WHERE status IN ('running', 'paused') ORDER BY started_at LIMIT 1`)
	var run domain.ScanRun
	if err := row.Scan(&run.ID, &run.StartID, &run.EndID, &run.CursorID, &run.Status, &run.ProcessedCount, &run.EmptyCount, &run.PositiveCount, &run.ErrorCount); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("read active scan: %w", err)
	}
	return &run, nil
}

// CreateScan creates a one-time run for the current address range.
func (s *Store) CreateScan(ctx context.Context, startID, endID int64) (*domain.ScanRun, error) {
	run := domain.ScanRun{ID: uuid.New(), StartID: startID, EndID: endID, CursorID: startID - 1, Status: "running"}
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO scan_runs (id, start_id, end_id, cursor_id, status)
		VALUES ($1, $2, $3, $4, $5)`, run.ID, run.StartID, run.EndID, run.CursorID, run.Status)
	if err != nil {
		return nil, fmt.Errorf("create scan: %w", err)
	}
	return &run, nil
}

// NextAddresses returns the next keyset-paginated batch for a run.
func (s *Store) NextAddresses(ctx context.Context, run domain.ScanRun, batchSize int) ([]domain.Address, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT id, address_type, address, label
		FROM wallet_addresses
		WHERE id > $1 AND id <= $2
		ORDER BY id
		LIMIT $3`, run.CursorID, run.EndID, batchSize)
	if err != nil {
		return nil, fmt.Errorf("read scan batch: %w", err)
	}
	defer rows.Close()
	addresses := make([]domain.Address, 0, batchSize)
	for rows.Next() {
		var address domain.Address
		if err := rows.Scan(&address.ID, &address.AddressType, &address.Address, &address.Label); err != nil {
			return nil, fmt.Errorf("scan address row: %w", err)
		}
		addresses = append(addresses, address)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read scan batch rows: %w", err)
	}
	return addresses, nil
}

// AdvanceScan records a completed batch and its counters atomically.
func (s *Store) AdvanceScan(ctx context.Context, runID uuid.UUID, cursorID int64, processed, empty, positive, failures int64) error {
	_, err := s.Pool.Exec(ctx, `
		UPDATE scan_runs
		SET cursor_id = $2, processed_count = processed_count + $3, empty_count = empty_count + $4,
		    positive_count = positive_count + $5, error_count = error_count + $6
		WHERE id = $1 AND status = 'running'`, runID, cursorID, processed, empty, positive, failures)
	if err != nil {
		return fmt.Errorf("advance scan: %w", err)
	}
	return nil
}

// CompleteScan marks a run completed after its cursor reaches the end.
func (s *Store) CompleteScan(ctx context.Context, runID uuid.UUID) error {
	_, err := s.Pool.Exec(ctx, `UPDATE scan_runs SET status = 'completed', completed_at = now() WHERE id = $1 AND status = 'running'`, runID)
	return err
}

// ResumeScan makes a paused run eligible for processing again.
func (s *Store) ResumeScan(ctx context.Context, runID uuid.UUID) error {
	_, err := s.Pool.Exec(ctx, `UPDATE scan_runs SET status = 'running' WHERE id = $1 AND status = 'paused'`, runID)
	return err
}

// PauseScan marks a run paused without changing its checkpoint.
func (s *Store) PauseScan(ctx context.Context, runID uuid.UUID) error {
	_, err := s.Pool.Exec(ctx, `UPDATE scan_runs SET status = 'paused' WHERE id = $1 AND status = 'running'`, runID)
	return err
}

// PositiveFinding contains one positive native balance.
type PositiveFinding struct {
	AddressID   int64
	Chain       string
	Balance     string
	AssetSymbol string
}

// PositiveView is a positive balance joined with its public address.
type PositiveView struct {
	Chain       string
	Balance     string
	AssetSymbol string
}

// NotificationView contains the data needed for an address notification.
type NotificationView struct {
	EventID   int64
	EventType string
	Address   string
	Label     string
	Findings  []PositiveView
}

// NodeNotificationView contains a node incident for an alert message.
type NodeNotificationView struct {
	EventID             int64
	Chain               string
	Provider            string
	ErrorCode           string
	ConsecutiveFailures int
}

// Status summarizes current source and scan counts.
type Status struct {
	AddressCount  int64
	PositiveCount int64
	RetryCount    int64
	ActiveRun     *domain.ScanRun
}

// RetryItem is one failed address-chain query eligible for retry.
type RetryItem struct {
	ID          int64
	AddressID   int64
	AddressType string
	Address     string
	Label       string
	Chain       string
}

// SavePositiveFindings upserts one address's positive chains and creates one outbox event atomically.
func (s *Store) SavePositiveFindings(ctx context.Context, findings []PositiveFinding) (*int64, error) {
	if len(findings) == 0 {
		return nil, nil
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin positive finding: %w", err)
	}
	defer tx.Rollback(ctx)
	var eventFinding *int64
	createdAny := false
	for _, finding := range findings {
		var id int64
		var previous string
		err = tx.QueryRow(ctx, `SELECT id, balance_atomic::text FROM positive_findings WHERE address_id = $1 AND chain = $2 FOR UPDATE`, finding.AddressID, finding.Chain).Scan(&id, &previous)
		changed := false
		created := false
		if errors.Is(err, pgx.ErrNoRows) {
			err = tx.QueryRow(ctx, `
				INSERT INTO positive_findings (address_id, chain, balance_atomic, asset_symbol)
				VALUES ($1, $2, $3::numeric, $4) RETURNING id`, finding.AddressID, finding.Chain, finding.Balance, finding.AssetSymbol).Scan(&id)
			changed = true
			created = true
		} else if err == nil {
			changed = previous != finding.Balance
			_, err = tx.Exec(ctx, `
				UPDATE positive_findings
				SET balance_atomic = $3::numeric, asset_symbol = $4, last_seen_at = now()
				WHERE address_id = $1 AND chain = $2`, finding.AddressID, finding.Chain, finding.Balance, finding.AssetSymbol)
		}
		if err != nil {
			return nil, fmt.Errorf("upsert positive finding: %w", err)
		}
		if changed && eventFinding == nil {
			idCopy := id
			eventFinding = &idCopy
		}
		createdAny = createdAny || created
	}
	if eventFinding == nil {
		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("commit positive findings: %w", err)
		}
		return nil, nil
	}
	eventType := "balance_changed"
	if createdAny {
		eventType = "first_positive"
	}
	var eventID int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO notification_events (finding_id, event_type, status)
		VALUES ($1, $2, 'pending') RETURNING id`, eventFinding, eventType).Scan(&eventID); err != nil {
		return nil, fmt.Errorf("create positive notification: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit positive findings: %w", err)
	}
	return &eventID, nil
}

// CreateNotificationEvent adds a durable outbox event.
func (s *Store) CreateNotificationEvent(ctx context.Context, findingID *int64, eventType string) (int64, error) {
	var id int64
	err := s.Pool.QueryRow(ctx, `
		INSERT INTO notification_events (finding_id, event_type, status)
		VALUES ($1, $2, 'pending') RETURNING id`, findingID, eventType).Scan(&id)
	return id, err
}

// CreateNodeIncident records one provider outage.
func (s *Store) CreateNodeIncident(ctx context.Context, chain, provider, code string, failures int) (int64, error) {
	var id int64
	err := s.Pool.QueryRow(ctx, `
		INSERT INTO node_incidents (chain, provider, error_code, consecutive_failures, status)
		VALUES ($1, $2, $3, $4, 'active') RETURNING id`, chain, provider, code, failures).Scan(&id)
	return id, err
}

// CreateNodeNotificationEvent adds a node incident to the notification outbox.
func (s *Store) CreateNodeNotificationEvent(ctx context.Context, incidentID int64) (int64, error) {
	var id int64
	err := s.Pool.QueryRow(ctx, `
		INSERT INTO notification_events (node_incident_id, event_type, status)
		VALUES ($1, 'node_error', 'pending') RETURNING id`, incidentID).Scan(&id)
	return id, err
}

// NotificationType returns the durable event kind.
func (s *Store) NotificationType(ctx context.Context, eventID int64) (string, error) {
	var eventType string
	err := s.Pool.QueryRow(ctx, "SELECT event_type FROM notification_events WHERE id = $1", eventID).Scan(&eventType)
	return eventType, err
}

// LoadNodeNotification loads a node incident event.
func (s *Store) LoadNodeNotification(ctx context.Context, eventID int64) (NodeNotificationView, error) {
	var view NodeNotificationView
	err := s.Pool.QueryRow(ctx, `
		SELECT e.id, n.chain, n.provider, n.error_code, n.consecutive_failures
		FROM notification_events e JOIN node_incidents n ON n.id = e.node_incident_id
		WHERE e.id = $1`, eventID).Scan(&view.EventID, &view.Chain, &view.Provider, &view.ErrorCode, &view.ConsecutiveFailures)
	return view, err
}

// LoadNotification joins an event with the current positive balances for its address.
func (s *Store) LoadNotification(ctx context.Context, eventID int64) (NotificationView, error) {
	var view NotificationView
	var addressID int64
	if err := s.Pool.QueryRow(ctx, `
		SELECT e.id, e.event_type, a.id, a.address, a.label
		FROM notification_events e
		JOIN positive_findings f ON f.id = e.finding_id
		JOIN wallet_addresses a ON a.id = f.address_id
		WHERE e.id = $1`, eventID).Scan(&view.EventID, &view.EventType, &addressID, &view.Address, &view.Label); err != nil {
		return NotificationView{}, fmt.Errorf("load notification: %w", err)
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT chain, balance_atomic::text, asset_symbol
		FROM positive_findings WHERE address_id = $1 ORDER BY chain`, addressID)
	if err != nil {
		return NotificationView{}, fmt.Errorf("load notification findings: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var finding PositiveView
		if err := rows.Scan(&finding.Chain, &finding.Balance, &finding.AssetSymbol); err != nil {
			return NotificationView{}, fmt.Errorf("scan notification finding: %w", err)
		}
		view.Findings = append(view.Findings, finding)
	}
	if err := rows.Err(); err != nil {
		return NotificationView{}, fmt.Errorf("read notification findings: %w", err)
	}
	return view, nil
}

// MarkNotification stores the result of a Webhook delivery attempt.
func (s *Store) MarkNotification(ctx context.Context, eventID int64, status, message string) error {
	_, err := s.Pool.Exec(ctx, `
		UPDATE notification_events
		SET status = $2, last_error = NULLIF($3, ''), sent_at = CASE WHEN $2 = 'sent' THEN now() ELSE sent_at END,
		    attempts = attempts + 1,
		    next_attempt_at = CASE WHEN $2 = 'sent' THEN now() ELSE now() + make_interval(secs => LEAST(3600, power(2::double precision, LEAST(attempts, 11)))::int) END
		WHERE id = $1`, eventID, status, message)
	return err
}

// PendingNotificationIDs returns outbox events that can be delivered.
func (s *Store) PendingNotificationIDs(ctx context.Context, limit int) ([]int64, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT id FROM notification_events
		WHERE (status = 'pending' OR (status = 'failed' AND next_attempt_at <= now()))
		ORDER BY created_at LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make([]int64, 0, limit)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ReadStatus returns current counts and active scan state.
func (s *Store) ReadStatus(ctx context.Context) (Status, error) {
	var status Status
	if err := s.Pool.QueryRow(ctx, "SELECT count(*) FROM wallet_addresses").Scan(&status.AddressCount); err != nil {
		return Status{}, err
	}
	if err := s.Pool.QueryRow(ctx, "SELECT count(*) FROM positive_findings").Scan(&status.PositiveCount); err != nil {
		return Status{}, err
	}
	if err := s.Pool.QueryRow(ctx, "SELECT count(*) FROM retry_queue WHERE resolved_at IS NULL").Scan(&status.RetryCount); err != nil {
		return Status{}, err
	}
	active, err := s.ActiveScan(ctx)
	if err != nil {
		return Status{}, err
	}
	status.ActiveRun = active
	return status, nil
}

// ExportFindings writes a CSV of positive public-address findings.
func (s *Store) ExportFindings(ctx context.Context, w io.Writer) error {
	writer := csv.NewWriter(w)
	if err := writer.Write([]string{"address_type", "address", "label", "chain", "balance_atomic", "asset_symbol", "first_seen_at", "last_seen_at"}); err != nil {
		return err
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT a.address_type, a.address, a.label, f.chain, f.balance_atomic::text, f.asset_symbol,
		       f.first_seen_at, f.last_seen_at
		FROM positive_findings f JOIN wallet_addresses a ON a.id = f.address_id
		ORDER BY f.id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var addressType, address, label, chain, balance, symbol string
		var firstSeen, lastSeen time.Time
		if err := rows.Scan(&addressType, &address, &label, &chain, &balance, &symbol, &firstSeen, &lastSeen); err != nil {
			return err
		}
		if err := writer.Write([]string{addressType, address, label, chain, balance, symbol, firstSeen.Format(time.RFC3339), lastSeen.Format(time.RFC3339)}); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	writer.Flush()
	return writer.Error()
}

// RecordRetry stores the latest failure for one address and chain.
func (s *Store) RecordRetry(ctx context.Context, addressID int64, chain, code, provider, message string, next time.Time) error {
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO retry_queue (address_id, chain, error_code, provider, attempts, next_retry_at, last_error)
		VALUES ($1, $2, $3, $4, 1, $5, $6)
		ON CONFLICT (address_id, chain) DO UPDATE
		SET error_code = EXCLUDED.error_code, provider = EXCLUDED.provider, attempts = retry_queue.attempts + 1,
		    next_retry_at = EXCLUDED.next_retry_at, last_error = EXCLUDED.last_error, resolved_at = NULL`,
		addressID, chain, code, provider, next, message)
	return err
}

// CloseRetry marks a retry row resolved after a successful retry.
func (s *Store) CloseRetry(ctx context.Context, addressID int64, chain string) error {
	_, err := s.Pool.Exec(ctx, `UPDATE retry_queue SET resolved_at = now() WHERE address_id = $1 AND chain = $2`, addressID, chain)
	return err
}

// NextRetries returns unresolved failed queries that are ready to retry.
func (s *Store) NextRetries(ctx context.Context, limit int) ([]RetryItem, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT q.id, q.address_id, a.address_type, a.address, a.label, q.chain
		FROM retry_queue q JOIN wallet_addresses a ON a.id = q.address_id
		WHERE q.resolved_at IS NULL AND q.next_retry_at <= now()
		ORDER BY q.id LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]RetryItem, 0, limit)
	for rows.Next() {
		var item RetryItem
		if err := rows.Scan(&item.ID, &item.AddressID, &item.AddressType, &item.Address, &item.Label, &item.Chain); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
