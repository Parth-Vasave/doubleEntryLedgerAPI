package ledger_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"pgregory.net/rapid"

	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/domain"
	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/ledger"
	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/money"
	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/testutil"
)

// modelAccount is a deliberately naive, independent model of an account used
// to predict what the real ledger should do.
type modelAccount struct {
	id            uuid.UUID
	currency      string
	debitNormal   bool
	allowNegative bool
	debits        int64 // posted
	credits       int64 // posted
	holdDebits    int64 // outstanding pending
	holdCredits   int64 // outstanding pending
}

func (m *modelAccount) posted() int64 {
	if m.debitNormal {
		return m.debits - m.credits
	}
	return m.credits - m.debits
}

func (m *modelAccount) available() int64 {
	if m.debitNormal {
		return m.posted() - m.holdCredits
	}
	return m.posted() - m.holdDebits
}

type model struct {
	svc      *ledger.Service
	ledgerID uuid.UUID
	accounts []*modelAccount
	byID     map[uuid.UUID]*modelAccount
}

func newModel(t *rapid.T, svc *ledger.Service) *model {
	m := &model{svc: svc, ledgerID: newLedger(t, svc), byID: map[uuid.UUID]*modelAccount{}}
	types := []domain.AccountType{domain.Asset, domain.Liability, domain.Equity, domain.Revenue, domain.Expense}
	n := rapid.IntRange(3, 7).Draw(t, "accounts")
	for i := range n {
		typ := rapid.SampledFrom(types).Draw(t, fmt.Sprintf("type%d", i))
		cur := rapid.SampledFrom([]string{"USD", "EUR"}).Draw(t, fmt.Sprintf("currency%d", i))
		// Make the first account of each currency a funding source that may go negative.
		allowNeg := i < 2 || rapid.Bool().Draw(t, fmt.Sprintf("allowNeg%d", i))
		if i < 2 {
			cur = []string{"USD", "EUR"}[i]
		}
		a := newAccount(t, svc, m.ledgerID, fmt.Sprintf("acct:%d", i), typ, cur, allowNeg)
		ma := &modelAccount{id: a.ID, currency: cur, debitNormal: typ.NormalBalance() == domain.Debit, allowNegative: allowNeg}
		m.accounts = append(m.accounts, ma)
		m.byID[a.ID] = ma
	}
	return m
}

// drawEntries draws a balanced set of 2-4 entries in one currency.
func (m *model) drawEntries(t *rapid.T) []domain.EntryInput {
	cur := rapid.SampledFrom([]string{"USD", "EUR"}).Draw(t, "currency")
	var pool []*modelAccount
	for _, a := range m.accounts {
		if a.currency == cur {
			pool = append(pool, a)
		}
	}
	debit := rapid.SampledFrom(pool).Draw(t, "debit")
	credits := rapid.IntRange(1, 3).Draw(t, "credit legs")
	var entries []domain.EntryInput
	var total int64
	for i := range credits {
		c := rapid.SampledFrom(pool).Draw(t, fmt.Sprintf("credit%d", i))
		amt := rapid.Int64Range(1, 5_000).Draw(t, fmt.Sprintf("amount%d", i))
		total += amt
		entries = append(entries, domain.EntryInput{AccountID: c.id, Direction: domain.Credit, Amount: money.Amount(amt), Currency: cur})
	}
	return append(entries, domain.EntryInput{AccountID: debit.id, Direction: domain.Debit, Amount: money.Amount(total), Currency: cur})
}

// predict applies entries to a copy of the touched model accounts and reports
// whether the ledger should accept them.
func (m *model) predict(entries []domain.EntryInput, pending bool) (map[uuid.UUID]modelAccount, bool) {
	next := map[uuid.UUID]modelAccount{}
	for _, e := range entries {
		a, ok := next[e.AccountID]
		if !ok {
			a = *m.byID[e.AccountID]
		}
		switch {
		case e.Direction == domain.Debit && !pending:
			a.debits += int64(e.Amount)
		case e.Direction == domain.Credit && !pending:
			a.credits += int64(e.Amount)
		case e.Direction == domain.Debit:
			a.holdDebits += int64(e.Amount)
		default:
			a.holdCredits += int64(e.Amount)
		}
		next[e.AccountID] = a
	}
	for _, a := range next {
		if !a.allowNegative && a.available() < 0 {
			return nil, false
		}
	}
	return next, true
}

func (m *model) commit(next map[uuid.UUID]modelAccount) {
	for id, a := range next {
		*m.byID[id] = a
	}
}

// check compares every account's real balances to the model.
func (m *model) check(t *rapid.T) {
	ctx := context.Background()
	for _, ma := range m.accounts {
		a, err := m.svc.GetAccount(ctx, ma.id)
		if err != nil {
			t.Fatalf("get account: %v", err)
		}
		b, _ := a.Balances()
		if int64(b.Posted) != ma.posted() || int64(b.Available) != ma.available() {
			t.Fatalf("account %s: ledger posted=%d available=%d, model posted=%d available=%d",
				a.Code, b.Posted, b.Available, ma.posted(), ma.available())
		}
	}
	assertLedgerInvariants(t, m.ledgerID)
}

// TestLedgerProperties runs random sequences of operations against the real
// ledger and an independent model, asserting after every step that both agree
// and that all global invariants hold. On failure rapid shrinks the sequence
// to a minimal reproduction.
func TestLedgerProperties(t *testing.T) {
	testutil.SkipIfShort(t)
	svc := ledger.NewService(testPool)
	ctx := context.Background()

	rapid.Check(t, func(t *rapid.T) {
		m := newModel(t, svc)
		t.Repeat(map[string]func(*rapid.T){
			"transfer": func(t *rapid.T) {
				entries := m.drawEntries(t)
				next, accept := m.predict(entries, false)
				_, _, err := svc.CreateTransaction(ctx, m.ledgerID, domain.CreateTransactionRequest{Entries: entries}, nil)
				switch {
				case accept && err != nil:
					t.Fatalf("model accepted but ledger rejected: %v", err)
				case !accept && !domain.IsCode(err, domain.CodeInsufficientFunds):
					t.Fatalf("model predicted insufficient funds, ledger returned %v", err)
				case accept:
					m.commit(next)
				}
			},
			"": func(t *rapid.T) { m.check(t) },
		})
	})
}
