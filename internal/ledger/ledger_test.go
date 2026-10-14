package ledger_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/domain"
	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/ledger"
	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/money"
	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/store"
	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/testutil"
)

var testPool *pgxpool.Pool

func TestMain(m *testing.M) { testutil.RunMain(m, &testPool) }

type fataler interface {
	Helper()
	Fatalf(format string, args ...any)
}

func newLedger(t fataler, svc *ledger.Service) uuid.UUID {
	t.Helper()
	l, err := svc.CreateLedger(context.Background(), domain.CreateLedgerRequest{Name: "test"})
	if err != nil {
		t.Fatalf("create ledger: %v", err)
	}
	return l.ID
}

func newAccount(t fataler, svc *ledger.Service, ledgerID uuid.UUID, code string, typ domain.AccountType, currency string, allowNeg bool) *domain.Account {
	t.Helper()
	a, err := svc.CreateAccount(context.Background(), ledgerID, domain.CreateAccountRequest{
		Code: code, Name: code, Type: typ, Currency: currency, AllowNegative: allowNeg,
	})
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	return a
}

func transferReq(from, to uuid.UUID, amt money.Amount, cur string) domain.CreateTransactionRequest {
	return domain.CreateTransactionRequest{Entries: []domain.EntryInput{
		{AccountID: from, Direction: domain.Debit, Amount: amt, Currency: cur},
		{AccountID: to, Direction: domain.Credit, Amount: amt, Currency: cur},
	}}
}

// assertLedgerInvariants checks the global invariants of a ledger:
//   - cached counters equal what the journal says, versions are gapless;
//   - total debits equal total credits in every currency;
//   - no account that forbids it has a negative available balance;
//   - each account's latest entry carries its current posted balance.
func assertLedgerInvariants(t fataler, ledgerID uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	drift, err := store.Reconcile(ctx, testPool, &ledgerID)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(drift) > 0 {
		t.Fatalf("balance drift detected: %+v", drift)
	}
	tb, err := store.TrialBalance(ctx, testPool, ledgerID)
	if err != nil {
		t.Fatalf("trial balance: %v", err)
	}
	for _, row := range tb {
		if !row.Balanced || row.DebitBalances != row.CreditBalances {
			t.Fatalf("trial balance broken: %+v", row)
		}
	}
	// The newest entry of every account records the account's current posted balance.
	var stale int
	if err := testPool.QueryRow(ctx, `
		SELECT count(*) FROM accounts a
		  JOIN LATERAL (SELECT balance_after FROM entries e WHERE e.account_id = a.id
		                 ORDER BY account_version DESC LIMIT 1) last ON true
		 WHERE a.ledger_id = $1
		   AND last.balance_after <> CASE WHEN a.normal_balance = 'DEBIT'
		                                  THEN a.posted_debits - a.posted_credits
		                                  ELSE a.posted_credits - a.posted_debits END`, ledgerID).Scan(&stale); err != nil {
		t.Fatalf("balance_after check: %v", err)
	}
	if stale > 0 {
		t.Fatalf("%d accounts have a stale balance_after on their latest entry", stale)
	}
	accts, err := store.ListAccounts(ctx, testPool, ledgerID, nil, 1000)
	if err != nil {
		t.Fatalf("list accounts: %v", err)
	}
	for _, a := range accts {
		b, err := a.Balances()
		if err != nil {
			t.Fatalf("balances: %v", err)
		}
		if !a.AllowNegative && b.Available < 0 {
			t.Fatalf("account %s overdrawn: %+v", a.Code, b)
		}
	}
}
