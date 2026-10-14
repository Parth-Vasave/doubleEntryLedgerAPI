// Package domain defines the ledger's core types and accounting rules.
// It has no I/O dependencies so the rules can be unit tested in isolation.
package domain

import (
	"time"

	"github.com/google/uuid"

	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/money"
)

// Direction is the side of an entry.
type Direction string

// Entry directions.
const (
	Debit  Direction = "DEBIT"
	Credit Direction = "CREDIT"
)

// Valid reports whether d is a known direction.
func (d Direction) Valid() bool { return d == Debit || d == Credit }

// Opposite returns the other side.
func (d Direction) Opposite() Direction {
	if d == Debit {
		return Credit
	}
	return Debit
}

// AccountType is the accounting classification of an account.
type AccountType string

// Account types.
const (
	Asset     AccountType = "ASSET"
	Liability AccountType = "LIABILITY"
	Equity    AccountType = "EQUITY"
	Revenue   AccountType = "REVENUE"
	Expense   AccountType = "EXPENSE"
)

// Valid reports whether t is a known account type.
func (t AccountType) Valid() bool {
	switch t {
	case Asset, Liability, Equity, Revenue, Expense:
		return true
	}
	return false
}

// NormalBalance is the side that increases an account of this type.
func (t AccountType) NormalBalance() Direction {
	if t == Asset || t == Expense {
		return Debit
	}
	return Credit
}

// AccountStatus controls whether an account can be posted to.
type AccountStatus string

// Account statuses.
const (
	AccountActive AccountStatus = "ACTIVE"
	AccountFrozen AccountStatus = "FROZEN"
)

// TxnStatus is the lifecycle state of a transaction.
type TxnStatus string

// Transaction statuses.
const (
	TxnPending TxnStatus = "PENDING"
	TxnPosted  TxnStatus = "POSTED"
	TxnVoided  TxnStatus = "VOIDED"
)

// Metadata is free-form string key/value data attached to resources.
type Metadata map[string]string

// Ledger is an isolated book of accounts.
type Ledger struct {
	ID        uuid.UUID `json:"id"`
	Name      string    `json:"name"`
	Metadata  Metadata  `json:"metadata"`
	CreatedAt time.Time `json:"created_at"`
}

// Account is a single-currency account with running balance counters.
type Account struct {
	ID             uuid.UUID     `json:"id"`
	LedgerID       uuid.UUID     `json:"ledger_id"`
	Code           string        `json:"code"`
	Name           string        `json:"name"`
	Type           AccountType   `json:"type"`
	NormalBalance  Direction     `json:"normal_balance"`
	Currency       string        `json:"currency"`
	AllowNegative  bool          `json:"allow_negative"`
	Status         AccountStatus `json:"status"`
	PostedDebits   money.Amount  `json:"-"`
	PostedCredits  money.Amount  `json:"-"`
	PendingDebits  money.Amount  `json:"-"`
	PendingCredits money.Amount  `json:"-"`
	Version        int64         `json:"version"`
	Metadata       Metadata      `json:"metadata"`
	CreatedAt      time.Time     `json:"created_at"`
	UpdatedAt      time.Time     `json:"updated_at"`
}

// Transaction is a balanced journal entry made of two or more entries.
type Transaction struct {
	ID           uuid.UUID  `json:"id"`
	LedgerID     uuid.UUID  `json:"ledger_id"`
	Status       TxnStatus  `json:"status"`
	Description  string     `json:"description"`
	ExternalRef  *string    `json:"external_ref,omitempty"`
	EffectiveAt  time.Time  `json:"effective_at"`
	ExpiresAt    *time.Time `json:"expires_at,omitempty"`
	PostedAt     *time.Time `json:"posted_at,omitempty"`
	VoidedAt     *time.Time `json:"voided_at,omitempty"`
	ReversesID   *uuid.UUID `json:"reverses_id,omitempty"`
	ReversedByID *uuid.UUID `json:"reversed_by_id,omitempty"`
	Metadata     Metadata   `json:"metadata"`
	CreatedAt    time.Time  `json:"created_at"`
	Entries      []Entry    `json:"entries"`
}

// Entry is a single debit or credit line against one account.
type Entry struct {
	ID             int64        `json:"id"`
	TransactionID  uuid.UUID    `json:"transaction_id"`
	AccountID      uuid.UUID    `json:"account_id"`
	Direction      Direction    `json:"direction"`
	Amount         money.Amount `json:"amount"`
	Currency       string       `json:"currency"`
	Pending        bool         `json:"pending"`
	AccountVersion int64        `json:"account_version"`
	BalanceAfter   money.Amount `json:"balance_after"`
	CreatedAt      time.Time    `json:"created_at"`
}
