package store

import (
	"bytes"
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// IdempotencyRecord is a stored idempotent response.
type IdempotencyRecord struct {
	RequestHash    []byte
	ResponseStatus int
	ResponseBody   json.RawMessage
}

// ClaimIdempotencyKey inserts the key inside the caller's transaction. It
// returns (nil, nil) when the key is new and now owned by this transaction,
// or the previously completed record when the key already exists.
//
// If another in-flight transaction holds the key, Postgres blocks the INSERT
// until that transaction finishes, so callers never observe a half-done key.
func ClaimIdempotencyKey(ctx context.Context, db DBTX, ledgerID uuid.UUID, key string, hash []byte) (*IdempotencyRecord, error) {
	tag, err := db.Exec(ctx, `
		INSERT INTO idempotency_keys (ledger_id, key, request_hash) VALUES ($1, $2, $3)
		ON CONFLICT DO NOTHING`, ledgerID, key, hash)
	if err != nil {
		return nil, MapError(err)
	}
	if tag.RowsAffected() == 1 {
		return nil, nil
	}
	var rec IdempotencyRecord
	var status *int
	err = db.QueryRow(ctx, `
		SELECT request_hash, response_status, response_body FROM idempotency_keys
		 WHERE ledger_id = $1 AND key = $2`, ledgerID, key).
		Scan(&rec.RequestHash, &status, &rec.ResponseBody)
	if err != nil {
		return nil, err
	}
	if status != nil {
		rec.ResponseStatus = *status
	}
	return &rec, nil
}

// SameRequest reports whether the stored record was produced by a request
// with the given hash.
func (r *IdempotencyRecord) SameRequest(hash []byte) bool { return bytes.Equal(r.RequestHash, hash) }

// CompleteIdempotencyKey stores the response for a claimed key.
func CompleteIdempotencyKey(ctx context.Context, db DBTX, ledgerID uuid.UUID, key string, status int, body []byte) error {
	_, err := db.Exec(ctx, `
		UPDATE idempotency_keys SET response_status = $3, response_body = $4
		 WHERE ledger_id = $1 AND key = $2`, ledgerID, key, status, body)
	return err
}

// DeleteExpiredIdempotencyKeys removes keys older than ttl, in bounded batches.
func DeleteExpiredIdempotencyKeys(ctx context.Context, db DBTX, ttl time.Duration, batch int) (int64, error) {
	tag, err := db.Exec(ctx, `
		DELETE FROM idempotency_keys WHERE ctid IN (
			SELECT ctid FROM idempotency_keys WHERE created_at < now() - make_interval(secs => $1) LIMIT $2)`,
		ttl.Seconds(), batch)
	return tag.RowsAffected(), err
}
