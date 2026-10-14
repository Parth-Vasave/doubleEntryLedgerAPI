package domain

import (
	"testing"

	"github.com/google/uuid"

	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/money"
)

func acct(t AccountType, allowNeg bool) *Account {
	return &Account{ID: uuid.New(), Type: t, NormalBalance: t.NormalBalance(), AllowNegative: allowNeg}
}

func TestNormalBalance(t *testing.T) {
	for typ, want := range map[AccountType]Direction{
		Asset: Debit, Expense: Debit, Liability: Credit, Equity: Credit, Revenue: Credit,
	} {
		if got := typ.NormalBalance(); got != want {
			t.Errorf("%s: got %s want %s", typ, got, want)
		}
	}
}

func TestBalancesBySide(t *testing.T) {
	// A customer wallet (liability): credits increase it.
	w := acct(Liability, false)
	must(t, w.Apply(Credit, 1000, false))
	must(t, w.Apply(Debit, 300, false))
	must(t, w.Apply(Debit, 200, true)) // outgoing hold
	must(t, w.Apply(Credit, 50, true)) // incoming hold

	b, err := w.Balances()
	must(t, err)
	if b.Posted != 700 {
		t.Errorf("posted = %d, want 700", b.Posted)
	}
	if b.Pending != 550 { // 700 - 200 + 50
		t.Errorf("pending = %d, want 550", b.Pending)
	}
	if b.Available != 500 { // incoming hold not spendable
		t.Errorf("available = %d, want 500", b.Available)
	}
	if w.Version != 4 {
		t.Errorf("version = %d, want 4", w.Version)
	}

	// Cash (asset): debits increase it.
	c := acct(Asset, false)
	must(t, c.Apply(Debit, 500, false))
	must(t, c.Apply(Credit, 100, true))
	b, err = c.Balances()
	must(t, err)
	if b.Posted != 500 || b.Available != 400 {
		t.Errorf("asset balances = %+v", b)
	}
}

func TestCheckAvailable(t *testing.T) {
	w := acct(Liability, false)
	must(t, w.Apply(Credit, 100, false))
	must(t, w.Apply(Debit, 101, false))
	if err := w.CheckAvailable(); !IsCode(err, CodeInsufficientFunds) {
		t.Fatalf("want insufficient_funds, got %v", err)
	}
	ext := acct(Asset, true)
	must(t, ext.Apply(Credit, 1_000_000, false))
	if err := ext.CheckAvailable(); err != nil {
		t.Fatalf("allow_negative account rejected: %v", err)
	}
}

func TestReleaseHold(t *testing.T) {
	w := acct(Liability, true)
	must(t, w.Apply(Debit, 100, true))
	must(t, w.ReleaseHold(Debit, 60))
	if w.PendingDebits != 40 {
		t.Fatalf("pending debits = %d", w.PendingDebits)
	}
	if err := w.ReleaseHold(Debit, 41); err == nil {
		t.Fatal("expected error releasing more than held")
	}
}

func TestApplyOverflow(t *testing.T) {
	a := acct(Asset, true)
	a.PostedDebits = money.Amount(1<<63 - 1)
	if err := a.Apply(Debit, 1, false); !IsCode(err, CodeAmountOverflow) {
		t.Fatalf("want overflow, got %v", err)
	}
}

func TestValidateEntries(t *testing.T) {
	a, b, c := uuid.New(), uuid.New(), uuid.New()
	cases := []struct {
		name    string
		entries []EntryInput
		code    string
	}{
		{"ok", []EntryInput{{a, Debit, 100, "USD"}, {b, Credit, 100, "USD"}}, ""},
		{"split", []EntryInput{{a, Debit, 100, "USD"}, {b, Credit, 60, "USD"}, {c, Credit, 40, "USD"}}, ""},
		{"multi-currency", []EntryInput{{a, Debit, 100, "USD"}, {b, Credit, 100, "USD"}, {b, Debit, 90, "EUR"}, {c, Credit, 90, "EUR"}}, ""},
		{"one entry", []EntryInput{{a, Debit, 100, "USD"}}, CodeValidation},
		{"unbalanced", []EntryInput{{a, Debit, 100, "USD"}, {b, Credit, 99, "USD"}}, CodeUnbalanced},
		{"cross-currency unbalanced", []EntryInput{{a, Debit, 100, "USD"}, {b, Credit, 100, "EUR"}}, CodeUnbalanced},
		{"zero", []EntryInput{{a, Debit, 0, "USD"}, {b, Credit, 0, "USD"}}, CodeValidation},
		{"negative", []EntryInput{{a, Debit, -5, "USD"}, {b, Credit, -5, "USD"}}, CodeValidation},
		{"bad direction", []EntryInput{{a, "UP", 100, "USD"}, {b, Credit, 100, "USD"}}, CodeValidation},
		{"bad currency", []EntryInput{{a, Debit, 100, "usd"}, {b, Credit, 100, "usd"}}, CodeValidation},
		{"nil account", []EntryInput{{uuid.Nil, Debit, 100, "USD"}, {b, Credit, 100, "USD"}}, CodeValidation},
		{"overflow", []EntryInput{{a, Debit, 1<<63 - 1, "USD"}, {a, Debit, 1, "USD"}, {b, Credit, 1, "USD"}}, CodeAmountOverflow},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateEntries(tc.entries)
			if tc.code == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if !IsCode(err, tc.code) {
				t.Fatalf("want %s, got %v", tc.code, err)
			}
		})
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
