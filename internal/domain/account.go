package domain

import (
	"encoding/json"
	"fmt"

	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/money"
)

// Balances are the signed balances of an account, expressed on its normal
// side: a positive balance means the account holds value in the direction it
// normally does (e.g. a customer wallet LIABILITY with credits > debits).
type Balances struct {
	// Posted is the settled balance.
	Posted money.Amount `json:"posted"`
	// Pending is the balance if every outstanding hold were posted.
	Pending money.Amount `json:"pending"`
	// Available is the posted balance minus outgoing holds; it is what can be
	// spent right now and is the balance overdraft checks apply to.
	Available money.Amount `json:"available"`

	PostedDebits   money.Amount `json:"posted_debits"`
	PostedCredits  money.Amount `json:"posted_credits"`
	PendingDebits  money.Amount `json:"pending_debits"`
	PendingCredits money.Amount `json:"pending_credits"`
}

// signed returns increase-side minus decrease-side for an account's normal balance.
func (a *Account) signed(debits, credits money.Amount) (money.Amount, error) {
	if a.NormalBalance == Debit {
		return money.Sub(debits, credits)
	}
	return money.Sub(credits, debits)
}

// PostedBalance is the settled balance on the account's normal side.
func (a *Account) PostedBalance() (money.Amount, error) {
	return a.signed(a.PostedDebits, a.PostedCredits)
}

// Balances derives all balance views from the account counters.
func (a *Account) Balances() (Balances, error) {
	b := Balances{
		PostedDebits:   a.PostedDebits,
		PostedCredits:  a.PostedCredits,
		PendingDebits:  a.PendingDebits,
		PendingCredits: a.PendingCredits,
	}
	var err error
	if b.Posted, err = a.PostedBalance(); err != nil {
		return b, err
	}
	totalDebits, err := money.Add(a.PostedDebits, a.PendingDebits)
	if err != nil {
		return b, err
	}
	totalCredits, err := money.Add(a.PostedCredits, a.PendingCredits)
	if err != nil {
		return b, err
	}
	if b.Pending, err = a.signed(totalDebits, totalCredits); err != nil {
		return b, err
	}
	// Outgoing holds reduce what is available; incoming holds are not yet spendable.
	outgoing := a.PendingCredits
	if a.NormalBalance == Credit {
		outgoing = a.PendingDebits
	}
	if b.Available, err = money.Sub(b.Posted, outgoing); err != nil {
		return b, err
	}
	return b, nil
}

// Apply records an entry against the account's counters and bumps its
// version. It does not enforce overdraft rules; see CheckAvailable.
func (a *Account) Apply(dir Direction, amt money.Amount, pending bool) error {
	if amt <= 0 {
		return fmt.Errorf("apply: non-positive amount %d", amt)
	}
	var counter *money.Amount
	switch {
	case dir == Debit && !pending:
		counter = &a.PostedDebits
	case dir == Credit && !pending:
		counter = &a.PostedCredits
	case dir == Debit && pending:
		counter = &a.PendingDebits
	default:
		counter = &a.PendingCredits
	}
	v, err := money.Add(*counter, amt)
	if err != nil {
		return E(KindUnprocessable, CodeAmountOverflow, "account %s balance would overflow", a.ID)
	}
	*counter = v
	a.Version++
	return nil
}

// ReleaseHold removes a previously applied pending amount (on post or void).
// It does not bump the version: no journal entry is written for a release.
func (a *Account) ReleaseHold(dir Direction, amt money.Amount) error {
	counter := &a.PendingCredits
	if dir == Debit {
		counter = &a.PendingDebits
	}
	if *counter < amt {
		return fmt.Errorf("release hold: account %s pending %s %d < %d", a.ID, dir, *counter, amt)
	}
	*counter -= amt
	return nil
}

// CheckAvailable enforces the overdraft rule for accounts that may not go negative.
func (a *Account) CheckAvailable() error {
	if a.AllowNegative {
		return nil
	}
	b, err := a.Balances()
	if err != nil {
		return E(KindUnprocessable, CodeAmountOverflow, "account %s balance overflow", a.ID)
	}
	if b.Available < 0 {
		return E(KindUnprocessable, CodeInsufficientFunds,
			"account %s (%s) has insufficient available balance", a.ID, a.Code)
	}
	return nil
}

// CheckPostable rejects postings to frozen accounts.
func (a *Account) CheckPostable() error {
	if a.Status == AccountFrozen {
		return E(KindUnprocessable, CodeAccountFrozen, "account %s (%s) is frozen", a.ID, a.Code)
	}
	return nil
}

// MarshalJSON adds derived balances to the account representation.
func (a Account) MarshalJSON() ([]byte, error) {
	type plain Account
	b, err := a.Balances()
	if err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		plain
		Balances Balances `json:"balances"`
	}{plain(a), b})
}
