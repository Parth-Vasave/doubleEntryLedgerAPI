package store_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/domain"
	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/ledger"
	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/testutil"
)

var testPool *pgxpool.Pool

func TestMain(m *testing.M) { testutil.RunMain(m, &testPool) }

// seed creates a ledger with two USD accounts and one posted transfer.
func seed(t *testing.T) (ledgerID, a, b uuid.UUID, txn *domain.Transaction) {
	t.Helper()
	testutil.SkipIfShort(t)
	ctx := context.Background()
	svc := ledger.NewService(testPool)
	l, err := svc.CreateLedger(ctx, domain.CreateLedgerRequest{Name: "invariants"})
	if err != nil {
		t.Fatal(err)
	}
	mk := func(code string) uuid.UUID {
		acc, err := svc.CreateAccount(ctx, l.ID, domain.CreateAccountRequest{Code: code, Name: code, Type: domain.Asset, Currency: "USD", AllowNegative: true})
		if err != nil {
			t.Fatal(err)
		}
		return acc.ID
	}
	a, b = mk("a"), mk("b")
	txn, err = svc.CreateTransaction(ctx, l.ID, domain.CreateTransactionRequest{Entries: []domain.EntryInput{
		{AccountID: a, Direction: domain.Debit, Amount: 100, Currency: "USD"},
		{AccountID: b, Direction: domain.Credit, Amount: 100, Currency: "USD"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return l.ID, a, b, txn
}

func expectDBError(t *testing.T, contains string, fn func(ctx context.Context, tx pgx.Tx) error) {
	t.Helper()
	ctx := context.Background()
	err := pgx.BeginFunc(ctx, testPool, func(tx pgx.Tx) error { return fn(ctx, tx) })
	if err == nil {
		t.Fatalf("expected database to reject the write (%s)", contains)
	}
	if !strings.Contains(err.Error(), contains) {
		t.Fatalf("error %q does not mention %q", err, contains)
	}
}

// These tests bypass the service and talk SQL directly, proving the database
// itself refuses to corrupt the ledger even if application code is buggy.

func TestEntriesAreAppendOnly(t *testing.T) {
	_, _, _, txn := seed(t)
	expectDBError(t, "append-only", func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE entries SET amount = 1 WHERE transaction_id = $1`, txn.ID)
		return err
	})
	expectDBError(t, "append-only", func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM entries WHERE transaction_id = $1`, txn.ID)
		return err
	})
	expectDBError(t, "append-only", func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM transactions WHERE id = $1`, txn.ID)
		return err
	})
}

func TestPostedTransactionsAreImmutable(t *testing.T) {
	_, _, _, txn := seed(t)
	expectDBError(t, "cannot be modified", func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE transactions SET description = 'tampered' WHERE id = $1`, txn.ID)
		return err
	})
}

func TestUnbalancedInsertRejectedAtCommit(t *testing.T) {
	ledgerID, a, _, _ := seed(t)
	expectDBError(t, "unbalanced", func(ctx context.Context, tx pgx.Tx) error {
		id := uuid.Must(uuid.NewV7())
		if _, err := tx.Exec(ctx, `INSERT INTO transactions (id, ledger_id, status, effective_at, posted_at)
			VALUES ($1, $2, 'POSTED', now(), now())`, id, ledgerID); err != nil {
			return err
		}
		// A lone debit: the deferred trigger fires at COMMIT.
		_, err := tx.Exec(ctx, `INSERT INTO entries (ledger_id, transaction_id, account_id, direction, amount,
			currency, pending, account_version, balance_after) VALUES ($1, $2, $3, 'DEBIT', 50, 'USD', false, 1000, 0)`,
			ledgerID, id, a)
		return err
	})
}

func TestEntryCurrencyMustMatchAccount(t *testing.T) {
	ledgerID, a, b, _ := seed(t)
	expectDBError(t, "entries_account_fk", func(ctx context.Context, tx pgx.Tx) error {
		id := uuid.Must(uuid.NewV7())
		if _, err := tx.Exec(ctx, `INSERT INTO transactions (id, ledger_id, status, effective_at, posted_at)
			VALUES ($1, $2, 'POSTED', now(), now())`, id, ledgerID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO entries (ledger_id, transaction_id, account_id, direction, amount,
			currency, pending, account_version, balance_after) VALUES
			($1, $2, $3, 'DEBIT', 50, 'EUR', false, 1000, 0), ($1, $2, $4, 'CREDIT', 50, 'EUR', false, 1000, 0)`,
			ledgerID, id, a, b)
		return err
	})
}

func TestOverdraftCheckConstraint(t *testing.T) {
	testutil.SkipIfShort(t)
	ctx := context.Background()
	svc := ledger.NewService(testPool)
	l, _ := svc.CreateLedger(ctx, domain.CreateLedgerRequest{Name: "overdraft"})
	w, err := svc.CreateAccount(ctx, l.ID, domain.CreateAccountRequest{Code: "w", Name: "w", Type: domain.Liability, Currency: "USD"})
	if err != nil {
		t.Fatal(err)
	}
	expectDBError(t, "accounts_non_negative_available", func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE accounts SET posted_debits = 1 WHERE id = $1`, w.ID)
		return err
	})
}
