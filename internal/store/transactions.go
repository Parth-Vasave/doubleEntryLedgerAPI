package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/domain"
	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/money"
)

const txnCols = `t.id, t.ledger_id, t.status, t.description, t.external_ref, t.effective_at, t.expires_at,
	t.posted_at, t.voided_at, t.reverses_id,
	(SELECT r.id FROM transactions r WHERE r.reverses_id = t.id),
	t.metadata, t.created_at`

const entryCols = `id, transaction_id, account_id, direction, amount, currency, pending, account_version, balance_after, created_at`

func scanTxn(row pgx.Row) (*domain.Transaction, error) {
	var t domain.Transaction
	err := row.Scan(&t.ID, &t.LedgerID, &t.Status, &t.Description, &t.ExternalRef, &t.EffectiveAt,
		&t.ExpiresAt, &t.PostedAt, &t.VoidedAt, &t.ReversesID, &t.ReversedByID, &t.Metadata, &t.CreatedAt)
	if err != nil {
		return nil, err
	}
	t.Entries = []domain.Entry{}
	return &t, nil
}

func scanEntry(row pgx.Row) (domain.Entry, error) {
	var e domain.Entry
	err := row.Scan(&e.ID, &e.TransactionID, &e.AccountID, &e.Direction, &e.Amount, &e.Currency,
		&e.Pending, &e.AccountVersion, &e.BalanceAfter, &e.CreatedAt)
	return e, err
}

// StatusChange moves a PENDING transaction to POSTED or VOIDED.
type StatusChange struct {
	ID     uuid.UUID
	Status domain.TxnStatus
	At     time.Time
}

// Write is everything one ledger operation persists. ApplyWrite sends it to
// Postgres as a single pipelined batch (one network round trip).
type Write struct {
	LedgerID     uuid.UUID
	NewTxns      []*domain.Transaction // inserted, with all their Entries
	StatusChange *StatusChange
	Entries      []*domain.Entry // additional entries for existing transactions
	Accounts     []*domain.Account
}

// ApplyWrite persists w. Generated ids and timestamps are written back into
// the passed structs.
func ApplyWrite(ctx context.Context, db DBTX, w Write) error {
	b := &pgx.Batch{}
	type entryRef struct{ e *domain.Entry }
	var entryRefs []entryRef

	queueEntry := func(e *domain.Entry) {
		b.Queue(`
			INSERT INTO entries (ledger_id, transaction_id, account_id, direction, amount, currency,
			                     pending, account_version, balance_after)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
			RETURNING id, created_at`,
			w.LedgerID, e.TransactionID, e.AccountID, e.Direction, e.Amount, e.Currency,
			e.Pending, e.AccountVersion, e.BalanceAfter)
		entryRefs = append(entryRefs, entryRef{e})
	}

	for _, t := range w.NewTxns {
		b.Queue(`
			INSERT INTO transactions (id, ledger_id, status, description, external_ref, effective_at,
			                          expires_at, posted_at, reverses_id, metadata)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
			RETURNING created_at`,
			t.ID, w.LedgerID, t.Status, t.Description, t.ExternalRef, t.EffectiveAt,
			t.ExpiresAt, t.PostedAt, t.ReversesID, nonNilMeta(t.Metadata))
	}
	if sc := w.StatusChange; sc != nil {
		col := "posted_at"
		if sc.Status == domain.TxnVoided {
			col = "voided_at"
		}
		b.Queue(`UPDATE transactions SET status = $2, `+col+` = $3 WHERE id = $1 AND status = 'PENDING'`,
			sc.ID, sc.Status, sc.At)
	}
	for _, t := range w.NewTxns {
		for i := range t.Entries {
			queueEntry(&t.Entries[i])
		}
	}
	for _, e := range w.Entries {
		queueEntry(e)
	}
	for _, a := range w.Accounts {
		queueAccountUpdate(b, a)
	}

	br := db.SendBatch(ctx, b)
	defer func() { _ = br.Close() }()

	for _, t := range w.NewTxns {
		if err := br.QueryRow().Scan(&t.CreatedAt); err != nil {
			return MapError(err)
		}
	}
	if w.StatusChange != nil {
		tag, err := br.Exec()
		if err != nil {
			return MapError(err)
		}
		if tag.RowsAffected() != 1 {
			return fmt.Errorf("status change of %s affected %d rows", w.StatusChange.ID, tag.RowsAffected())
		}
	}
	for _, r := range entryRefs {
		if err := br.QueryRow().Scan(&r.e.ID, &r.e.CreatedAt); err != nil {
			return MapError(err)
		}
	}
	for range w.Accounts {
		if _, err := br.Exec(); err != nil {
			return MapError(err)
		}
	}
	return MapError(br.Close())
}

// GetTransaction loads a transaction with its entries.
func GetTransaction(ctx context.Context, db DBTX, id uuid.UUID) (*domain.Transaction, error) {
	return getTransaction(ctx, db, id, "")
}

// GetTransactionForUpdate loads and row-locks a transaction with its entries.
func GetTransactionForUpdate(ctx context.Context, db DBTX, id uuid.UUID) (*domain.Transaction, error) {
	return getTransaction(ctx, db, id, " FOR UPDATE OF t")
}

func getTransaction(ctx context.Context, db DBTX, id uuid.UUID, lock string) (*domain.Transaction, error) {
	t, err := scanTxn(db.QueryRow(ctx, `SELECT `+txnCols+` FROM transactions t WHERE t.id = $1`+lock, id))
	if err != nil {
		return nil, MapError(err)
	}
	if err := attachEntries(ctx, db, []*domain.Transaction{t}); err != nil {
		return nil, err
	}
	return t, nil
}

// TxnFilter narrows ListTransactions.
type TxnFilter struct {
	Status *domain.TxnStatus
	Before *uuid.UUID
	Limit  int
}

// ListTransactions returns a page of a ledger's transactions, newest first.
// Ids are UUIDv7 so ordering by id is ordering by creation time.
func ListTransactions(ctx context.Context, db DBTX, ledgerID uuid.UUID, f TxnFilter) ([]*domain.Transaction, error) {
	rows, err := db.Query(ctx, `
		SELECT `+txnCols+` FROM transactions t
		 WHERE t.ledger_id = $1
		   AND ($2::text IS NULL OR t.status = $2)
		   AND ($3::uuid IS NULL OR t.id < $3)
		 ORDER BY t.id DESC LIMIT $4`, ledgerID, f.Status, f.Before, f.Limit)
	if err != nil {
		return nil, err
	}
	var out []*domain.Transaction
	for rows.Next() {
		t, err := scanTxn(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, attachEntries(ctx, db, out)
}

func attachEntries(ctx context.Context, db DBTX, txns []*domain.Transaction) error {
	if len(txns) == 0 {
		return nil
	}
	byID := make(map[uuid.UUID]*domain.Transaction, len(txns))
	ids := make([]uuid.UUID, len(txns))
	for i, t := range txns {
		byID[t.ID] = t
		ids[i] = t.ID
	}
	rows, err := db.Query(ctx,
		`SELECT `+entryCols+` FROM entries WHERE transaction_id = ANY($1) ORDER BY id`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return err
		}
		t := byID[e.TransactionID]
		t.Entries = append(t.Entries, e)
	}
	return rows.Err()
}

// EntryFilter narrows ListEntries.
type EntryFilter struct {
	BeforeVersion *int64
	From, To      *time.Time
	Limit         int
}

// ListEntries returns an account statement page, newest first.
func ListEntries(ctx context.Context, db DBTX, accountID uuid.UUID, f EntryFilter) ([]domain.Entry, error) {
	rows, err := db.Query(ctx, `
		SELECT `+entryCols+` FROM entries
		 WHERE account_id = $1
		   AND ($2::bigint IS NULL OR account_version < $2)
		   AND ($3::timestamptz IS NULL OR created_at >= $3)
		   AND ($4::timestamptz IS NULL OR created_at < $4)
		 ORDER BY account_version DESC LIMIT $5`,
		accountID, f.BeforeVersion, f.From, f.To, f.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Entry{}
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// PostedBalanceAt returns the account's posted balance (normal side) as of t,
// read from the balance_after of the last entry at or before t, and the
// account version at that point. Zero entries means a zero balance.
func PostedBalanceAt(ctx context.Context, db DBTX, accountID uuid.UUID, t time.Time) (balance money.Amount, version int64, err error) {
	err = db.QueryRow(ctx, `
		SELECT balance_after, account_version FROM entries
		 WHERE account_id = $1 AND created_at <= $2
		 ORDER BY created_at DESC, account_version DESC LIMIT 1`, accountID, t).Scan(&balance, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, 0, nil
	}
	return balance, version, err
}
