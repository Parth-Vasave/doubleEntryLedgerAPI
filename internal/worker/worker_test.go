package worker_test

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/domain"
	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/ledger"
	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/observability"
	tu "github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/testutil"
	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/worker"
)

var testPool *pgxpool.Pool

func TestMain(m *testing.M) { tu.RunMain(m, &testPool) }

// TestReconcileDetectsDrift corrupts a cached balance behind the ledger's
// back and checks the reconciliation job notices and exports it.
func TestReconcileDetectsDrift(t *testing.T) {
	tu.SkipIfShort(t)
	ctx := context.Background()
	svc := ledger.NewService(testPool)
	w := &worker.Worker{Pool: testPool, Ledger: svc, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}

	l, _ := svc.CreateLedger(ctx, domain.CreateLedgerRequest{Name: "drift"})
	a, _ := svc.CreateAccount(ctx, l.ID, domain.CreateAccountRequest{Code: "a", Name: "a", Type: domain.Asset, Currency: "USD", AllowNegative: true})
	b, _ := svc.CreateAccount(ctx, l.ID, domain.CreateAccountRequest{Code: "b", Name: "b", Type: domain.Asset, Currency: "USD", AllowNegative: true})
	if _, _, err := svc.CreateTransaction(ctx, l.ID, domain.CreateTransactionRequest{Entries: []domain.EntryInput{
		{AccountID: a.ID, Direction: domain.Debit, Amount: 100, Currency: "USD"},
		{AccountID: b.ID, Direction: domain.Credit, Amount: 100, Currency: "USD"},
	}}, nil); err != nil {
		t.Fatal(err)
	}

	if _, err := w.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if got := testutil.ToFloat64(observability.ReconcileDrift); got != 0 {
		t.Fatalf("drift = %v on a healthy ledger", got)
	}

	// Simulate a bug (or a manual UPDATE) that changes a counter without an entry.
	if _, err := testPool.Exec(ctx, `UPDATE accounts SET posted_debits = posted_debits + 1 WHERE id = $1`, a.ID); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = testPool.Exec(ctx, `UPDATE accounts SET posted_debits = posted_debits - 1 WHERE id = $1`, a.ID)
	}()
	if _, err := w.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if got := testutil.ToFloat64(observability.ReconcileDrift); got != 1 {
		t.Fatalf("drift = %v, want 1", got)
	}
}
