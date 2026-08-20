package maintenance

import (
	"context"
	"fmt"
	"time"

	"wallet-scan/internal/db"
)

// Cleaner removes only confirmed-empty addresses from completed old runs.
type Cleaner struct {
	Store *db.Store
}

// Run deletes one bounded batch and returns the number of removed rows.
func (c *Cleaner) Run(ctx context.Context, cutoff time.Time, batchSize int) (int64, error) {
	if batchSize < 1 {
		return 0, fmt.Errorf("cleanup batch size must be positive")
	}
	rows, err := c.Store.Pool.Query(ctx, `
		WITH candidates AS (
			SELECT a.id
			FROM wallet_addresses a
			WHERE EXISTS (
				SELECT 1 FROM scan_runs r
				WHERE r.status = 'completed' AND r.completed_at < $1
				  AND a.id BETWEEN r.start_id AND r.end_id
			)
			AND NOT EXISTS (SELECT 1 FROM positive_findings f WHERE f.address_id = a.id)
			AND NOT EXISTS (SELECT 1 FROM retry_queue q WHERE q.address_id = a.id)
			ORDER BY a.id
			LIMIT $2
		)
		DELETE FROM wallet_addresses a USING candidates c WHERE a.id = c.id
		RETURNING a.id`, cutoff, batchSize)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var deleted int64
	for rows.Next() {
		deleted++
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	return deleted, nil
}
