package notifications

import (
	"context"
	"fmt"
	"time"

	"wallet-scan/internal/db"
)

// Outbox delivers persisted notification events and retries failures.
type Outbox struct {
	Store  *db.Store
	Client *WeComClient
}

// Deliver sends one event and persists its status.
func (o *Outbox) Deliver(ctx context.Context, eventID int64) error {
	eventType, err := o.Store.NotificationType(ctx, eventID)
	if err != nil {
		_ = o.Store.MarkNotification(ctx, eventID, "failed", err.Error())
		return err
	}
	if eventType == "node_error" {
		view, loadErr := o.Store.LoadNodeNotification(ctx, eventID)
		if loadErr != nil {
			_ = o.Store.MarkNotification(ctx, eventID, "failed", loadErr.Error())
			return loadErr
		}
		if sendErr := o.Client.SendNode(ctx, view); sendErr != nil {
			_ = o.Store.MarkNotification(ctx, eventID, "failed", sendErr.Error())
			return sendErr
		}
		return o.Store.MarkNotification(ctx, eventID, "sent", "")
	}
	view, err := o.Store.LoadNotification(ctx, eventID)
	if err != nil {
		_ = o.Store.MarkNotification(ctx, eventID, "failed", err.Error())
		return err
	}
	if err := o.Client.SendPositive(ctx, view); err != nil {
		_ = o.Store.MarkNotification(ctx, eventID, "failed", err.Error())
		return err
	}
	return o.Store.MarkNotification(ctx, eventID, "sent", "")
}

// Drain attempts pending events once and returns the first delivery error.
func (o *Outbox) Drain(ctx context.Context, limit int) error {
	ids, err := o.Store.PendingNotificationIDs(ctx, limit)
	if err != nil {
		return err
	}
	var firstErr error
	for _, id := range ids {
		if err := o.Deliver(ctx, id); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("deliver event %d: %w", id, err)
		}
	}
	return firstErr
}

// RetryDelay returns a bounded delay for a notification attempt.
func RetryDelay(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	if attempt > 8 {
		attempt = 8
	}
	return time.Duration(1<<attempt) * time.Second
}
