package store

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/domain"
)

const accountCols = `id, ledger_id, code, name, type, normal_balance, currency, allow_negative, status,
	posted_debits, posted_credits, pending_debits, pending_credits, version, metadata, created_at, updated_at`

func scanAccount(row pgx.Row) (*domain.Account, error) {
	var a domain.Account
	err := row.Scan(&a.ID, &a.LedgerID, &a.Code, &a.Name, &a.Type, &a.NormalBalance, &a.Currency,
		&a.AllowNegative, &a.Status, &a.PostedDebits, &a.PostedCredits, &a.PendingDebits,
		&a.PendingCredits, &a.Version, &a.Metadata, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// InsertAccount inserts a new account with zero balances.
func InsertAccount(ctx context.Context, db DBTX, a *domain.Account) error {
	row := db.QueryRow(ctx, `
		INSERT INTO accounts (id, ledger_id, code, name, type, currency, allow_negative, metadata)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING `+accountCols,
		a.ID, a.LedgerID, a.Code, a.Name, a.Type, a.Currency, a.AllowNegative, nonNilMeta(a.Metadata))
	got, err := scanAccount(row)
	if err != nil {
		return MapError(err)
	}
	*a = *got
	return nil
}

// GetAccount loads an account by id.
func GetAccount(ctx context.Context, db DBTX, id uuid.UUID) (*domain.Account, error) {
	a, err := scanAccount(db.QueryRow(ctx, `SELECT `+accountCols+` FROM accounts WHERE id = $1`, id))
	return a, MapError(err)
}

// GetAccountForUpdate loads and row-locks an account.
func GetAccountForUpdate(ctx context.Context, db DBTX, id uuid.UUID) (*domain.Account, error) {
	a, err := scanAccount(db.QueryRow(ctx, `SELECT `+accountCols+` FROM accounts WHERE id = $1 FOR UPDATE`, id))
	return a, MapError(err)
}

// ListAccounts returns a page of accounts in a ledger, newest first.
func ListAccounts(ctx context.Context, db DBTX, ledgerID uuid.UUID, before *uuid.UUID, limit int) ([]*domain.Account, error) {
	rows, err := db.Query(ctx, `
		SELECT `+accountCols+` FROM accounts
		 WHERE ledger_id = $1 AND ($2::uuid IS NULL OR id < $2)
		 ORDER BY id DESC LIMIT $3`, ledgerID, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.Account
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// LockAccounts row-locks the given accounts of a ledger with SELECT ... FOR
// UPDATE, in ascending id order.
//
// Every writer locks in the same global order, so two transactions touching
// overlapping account sets can block each other but can never deadlock. The
// ORDER BY is applied before the LockRows node in the plan, so rows are locked
// in exactly that order.
func LockAccounts(ctx context.Context, db DBTX, ledgerID uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]*domain.Account, error) {
	rows, err := db.Query(ctx, `
		SELECT `+accountCols+` FROM accounts
		 WHERE ledger_id = $1 AND id = ANY($2)
		 ORDER BY id
		 FOR UPDATE`, ledgerID, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[uuid.UUID]*domain.Account, len(ids))
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		out[a.ID] = a
	}
	return out, rows.Err()
}

// queueAccountUpdate adds a balance-counter update for a to the batch.
func queueAccountUpdate(b *pgx.Batch, a *domain.Account) {
	b.Queue(`
		UPDATE accounts
		   SET posted_debits = $2, posted_credits = $3, pending_debits = $4, pending_credits = $5,
		       version = $6, updated_at = now()
		 WHERE id = $1`,
		a.ID, a.PostedDebits, a.PostedCredits, a.PendingDebits, a.PendingCredits, a.Version)
}

// UpdateAccountAttributes persists name, status and metadata.
func UpdateAccountAttributes(ctx context.Context, db DBTX, a *domain.Account) error {
	err := db.QueryRow(ctx, `
		UPDATE accounts SET name = $2, status = $3, metadata = $4, updated_at = now()
		 WHERE id = $1 RETURNING updated_at`,
		a.ID, a.Name, a.Status, nonNilMeta(a.Metadata)).Scan(&a.UpdatedAt)
	return MapError(err)
}
