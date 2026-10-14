package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/domain"
)

// Event is a domain event recorded in the transactional outbox.
type Event struct {
	EventID     uuid.UUID
	LedgerID    uuid.UUID
	Type        string
	AggregateID uuid.UUID
	Payload     any
}

// InsertEvents appends events to the outbox inside the caller's transaction.
func InsertEvents(ctx context.Context, db DBTX, events []Event) error {
	if len(events) == 0 {
		return nil
	}
	b := &pgx.Batch{}
	for _, e := range events {
		payload, err := json.Marshal(e.Payload)
		if err != nil {
			return err
		}
		b.Queue(`INSERT INTO outbox_events (event_id, ledger_id, type, aggregate_id, payload)
		         VALUES ($1, $2, $3, $4, $5)`, e.EventID, e.LedgerID, e.Type, e.AggregateID, payload)
	}
	return db.SendBatch(ctx, b).Close()
}

// RelayOutbox claims up to limit unpublished events, creates a delivery for
// every active webhook subscribed to each event's type, and marks the events
// published - all in one statement. SKIP LOCKED lets several relays run in
// parallel without double-processing. It returns the number of events relayed.
func RelayOutbox(ctx context.Context, db DBTX, limit int) (int64, error) {
	tag, err := db.Exec(ctx, `
		WITH ev AS (
			SELECT id, event_id, ledger_id, type FROM outbox_events
			 WHERE published_at IS NULL
			 ORDER BY id
			 LIMIT $1
			 FOR UPDATE SKIP LOCKED
		), fanout AS (
			INSERT INTO webhook_deliveries (webhook_id, event_id)
			SELECT w.id, ev.event_id
			  FROM ev JOIN webhooks w
			    ON w.ledger_id = ev.ledger_id AND w.active AND ev.type = ANY (w.event_types)
			ON CONFLICT DO NOTHING
		)
		UPDATE outbox_events o SET published_at = now() FROM ev WHERE o.id = ev.id`, limit)
	return tag.RowsAffected(), err
}

// Delivery is a leased webhook delivery attempt.
type Delivery struct {
	ID        int64
	WebhookID uuid.UUID
	URL       string
	Secret    string
	EventID   uuid.UUID
	Type      string
	Payload   json.RawMessage
	Attempt   int
	CreatedAt time.Time
}

// LeaseDeliveries claims up to limit due deliveries for lease. The HTTP call
// is made outside any database transaction; the lease stops other workers
// from picking the same delivery while it is in flight, and expires so a
// crashed worker's deliveries are retried.
func LeaseDeliveries(ctx context.Context, db DBTX, limit int, lease time.Duration) ([]Delivery, error) {
	rows, err := db.Query(ctx, `
		WITH due AS (
			SELECT id FROM webhook_deliveries
			 WHERE status = 'PENDING' AND next_attempt_at <= now()
			   AND (leased_until IS NULL OR leased_until < now())
			 ORDER BY next_attempt_at
			 LIMIT $1
			 FOR UPDATE SKIP LOCKED
		)
		UPDATE webhook_deliveries d
		   SET leased_until = now() + make_interval(secs => $2), attempts = d.attempts + 1
		  FROM due, webhooks w, outbox_events e
		 WHERE d.id = due.id AND w.id = d.webhook_id AND e.event_id = d.event_id
		RETURNING d.id, w.id, w.url, w.secret, e.event_id, e.type, e.payload, d.attempts, e.created_at`,
		limit, lease.Seconds())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Delivery
	for rows.Next() {
		var d Delivery
		if err := rows.Scan(&d.ID, &d.WebhookID, &d.URL, &d.Secret, &d.EventID, &d.Type, &d.Payload, &d.Attempt, &d.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// DeliveryResult records the outcome of one attempt.
type DeliveryResult struct {
	ID         int64
	Delivered  bool
	Failed     bool // retries exhausted
	StatusCode int
	Error      string
	// RetryIn is the delay before the next attempt. It is a duration, not a
	// timestamp, so next_attempt_at is computed from the database clock that
	// LeaseDeliveries compares against (worker and DB clocks may be skewed).
	RetryIn time.Duration
}

// CompleteDelivery records an attempt's outcome and releases the lease.
func CompleteDelivery(ctx context.Context, db DBTX, r DeliveryResult) error {
	status := "PENDING"
	switch {
	case r.Delivered:
		status = "DELIVERED"
	case r.Failed:
		status = "FAILED"
	}
	var code *int
	if r.StatusCode != 0 {
		code = &r.StatusCode
	}
	_, err := db.Exec(ctx, `
		UPDATE webhook_deliveries
		   SET status = $2, last_status = $3, last_error = nullif($4, ''), leased_until = NULL,
		       next_attempt_at = CASE WHEN $2 = 'PENDING' THEN now() + make_interval(secs => $5) ELSE next_attempt_at END,
		       delivered_at = CASE WHEN $2 = 'DELIVERED' THEN now() END
		 WHERE id = $1`, r.ID, status, code, r.Error, r.RetryIn.Seconds())
	return err
}

// Webhook is a ledger's event subscription.
type Webhook struct {
	ID         uuid.UUID `json:"id"`
	LedgerID   uuid.UUID `json:"ledger_id"`
	URL        string    `json:"url"`
	EventTypes []string  `json:"event_types"`
	Active     bool      `json:"active"`
	CreatedAt  time.Time `json:"created_at"`
	// Secret is only returned when the webhook is created.
	Secret string `json:"secret,omitempty"`
}

// InsertWebhook stores a webhook subscription.
func InsertWebhook(ctx context.Context, db DBTX, w *Webhook) error {
	err := db.QueryRow(ctx, `
		INSERT INTO webhooks (id, ledger_id, url, secret, event_types) VALUES ($1, $2, $3, $4, $5)
		RETURNING active, created_at`, w.ID, w.LedgerID, w.URL, w.Secret, w.EventTypes).Scan(&w.Active, &w.CreatedAt)
	return MapError(err)
}

// ListWebhooks lists a ledger's webhooks (without secrets).
func ListWebhooks(ctx context.Context, db DBTX, ledgerID uuid.UUID) ([]Webhook, error) {
	rows, err := db.Query(ctx, `
		SELECT id, ledger_id, url, event_types, active, created_at FROM webhooks
		 WHERE ledger_id = $1 ORDER BY id`, ledgerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Webhook{}
	for rows.Next() {
		var w Webhook
		if err := rows.Scan(&w.ID, &w.LedgerID, &w.URL, &w.EventTypes, &w.Active, &w.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// DeactivateWebhook disables a webhook; pending deliveries are kept for audit.
func DeactivateWebhook(ctx context.Context, db DBTX, ledgerID, id uuid.UUID) error {
	tag, err := db.Exec(ctx, `UPDATE webhooks SET active = false WHERE id = $1 AND ledger_id = $2`, id, ledgerID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.NotFound("webhook", id)
	}
	return nil
}
