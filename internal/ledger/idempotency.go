package ledger

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/domain"
	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/store"
)

// Idempotency identifies a client retry-safe request. Hash is a digest of the
// request (method, path and body) used to detect a key being reused for a
// different request.
type Idempotency struct {
	Key  string
	Hash []byte
}

// idempotent runs fn inside tx guarded by an idempotency key.
//
// The key row is inserted first, in the same database transaction as fn's
// writes, and completed with fn's result before commit. Therefore either both
// the effect and the key are committed or neither is: a crash can never leave
// money moved without the key recorded (which would allow a double-post on
// retry) or a key recorded without the money moved.
//
// Only successful results are stored. A request that fails (e.g. insufficient
// funds) rolls back, key included, so the client may retry with the same key.
func idempotent[T any](ctx context.Context, tx pgx.Tx, ledgerID uuid.UUID, idem *Idempotency, status int, fn func() (T, error)) (T, bool, error) {
	var zero T
	if idem == nil {
		v, err := fn()
		return v, false, err
	}
	rec, err := store.ClaimIdempotencyKey(ctx, tx, ledgerID, idem.Key, idem.Hash)
	if err != nil {
		return zero, false, err
	}
	if rec != nil {
		if !rec.SameRequest(idem.Hash) {
			return zero, false, domain.E(domain.KindUnprocessable, domain.CodeIdempotencyReused,
				"idempotency key %q was already used for a different request", idem.Key)
		}
		var v T
		if err := json.Unmarshal(rec.ResponseBody, &v); err != nil {
			return zero, false, fmt.Errorf("decode stored idempotent response: %w", err)
		}
		return v, true, nil
	}
	v, err := fn()
	if err != nil {
		return zero, false, err
	}
	body, err := json.Marshal(v)
	if err != nil {
		return zero, false, err
	}
	if err := store.CompleteIdempotencyKey(ctx, tx, ledgerID, idem.Key, status, body); err != nil {
		return zero, false, err
	}
	return v, false, nil
}
