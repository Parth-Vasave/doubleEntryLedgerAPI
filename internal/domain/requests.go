package domain

import (
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/money"
)

// Limits on request sizes.
const (
	MaxEntriesPerTransaction = 100
	MaxMetadataKeys          = 50
	MaxMetadataKeyLen        = 40
	MaxMetadataValueLen      = 500
)

var (
	currencyRe = regexp.MustCompile(`^[A-Z][A-Z0-9]{2,9}$`)
	codeRe     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:/-]{0,99}$`)
)

// CreateLedgerRequest creates a ledger.
type CreateLedgerRequest struct {
	Name     string   `json:"name"`
	Metadata Metadata `json:"metadata,omitempty"`
}

// Validate checks the request.
func (r *CreateLedgerRequest) Validate() error {
	r.Name = strings.TrimSpace(r.Name)
	if r.Name == "" || len(r.Name) > 200 {
		return Invalid("name is required and must be at most 200 characters")
	}
	return validateMetadata(r.Metadata)
}

// CreateAccountRequest creates an account in a ledger.
type CreateAccountRequest struct {
	Code          string      `json:"code"`
	Name          string      `json:"name"`
	Type          AccountType `json:"type"`
	Currency      string      `json:"currency"`
	AllowNegative bool        `json:"allow_negative"`
	Metadata      Metadata    `json:"metadata,omitempty"`
}

// Validate checks the request.
func (r *CreateAccountRequest) Validate() error {
	if !codeRe.MatchString(r.Code) {
		return Invalid("code must be 1-100 characters of letters, digits and _ . : / -")
	}
	r.Name = strings.TrimSpace(r.Name)
	if r.Name == "" || len(r.Name) > 200 {
		return Invalid("name is required and must be at most 200 characters")
	}
	if !r.Type.Valid() {
		return Invalid("type must be one of ASSET, LIABILITY, EQUITY, REVENUE, EXPENSE")
	}
	if !currencyRe.MatchString(r.Currency) {
		return Invalid("currency must be an upper-case currency code such as USD")
	}
	return validateMetadata(r.Metadata)
}

// UpdateAccountRequest changes mutable account attributes. Balances, type and
// currency are immutable.
type UpdateAccountRequest struct {
	Name     *string        `json:"name,omitempty"`
	Status   *AccountStatus `json:"status,omitempty"`
	Metadata Metadata       `json:"metadata,omitempty"`
}

// Validate checks the request.
func (r *UpdateAccountRequest) Validate() error {
	if r.Name == nil && r.Status == nil && r.Metadata == nil {
		return Invalid("at least one of name, status, metadata is required")
	}
	if r.Name != nil {
		n := strings.TrimSpace(*r.Name)
		if n == "" || len(n) > 200 {
			return Invalid("name must be 1-200 characters")
		}
		r.Name = &n
	}
	if r.Status != nil && *r.Status != AccountActive && *r.Status != AccountFrozen {
		return Invalid("status must be ACTIVE or FROZEN")
	}
	return validateMetadata(r.Metadata)
}

// EntryInput is one requested debit or credit.
type EntryInput struct {
	AccountID uuid.UUID    `json:"account_id"`
	Direction Direction    `json:"direction"`
	Amount    money.Amount `json:"amount"`
	Currency  string       `json:"currency"`
}

// CreateTransactionRequest posts a journal entry.
type CreateTransactionRequest struct {
	Description string       `json:"description,omitempty"`
	ExternalRef *string      `json:"external_ref,omitempty"`
	EffectiveAt *time.Time   `json:"effective_at,omitempty"`
	Metadata    Metadata     `json:"metadata,omitempty"`
	Entries     []EntryInput `json:"entries"`
	// Pending creates a two-phase transfer: funds are reserved (held) and only
	// move when the transaction is later posted, or are released if voided.
	Pending bool `json:"pending,omitempty"`
	// ExpiresAt auto-voids a pending transaction that is not posted in time.
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	// ExpectedVersions optionally pins account versions for optimistic
	// concurrency: the transaction fails with version_conflict if any listed
	// account has moved on since the client read it.
	ExpectedVersions map[uuid.UUID]int64 `json:"expected_versions,omitempty"`
}

// Validate checks the request shape and that it balances per currency.
func (r *CreateTransactionRequest) Validate() error {
	if len(r.Description) > 1000 {
		return Invalid("description must be at most 1000 characters")
	}
	if r.ExternalRef != nil && (len(*r.ExternalRef) == 0 || len(*r.ExternalRef) > 200) {
		return Invalid("external_ref must be 1-200 characters")
	}
	if err := validateMetadata(r.Metadata); err != nil {
		return err
	}
	if r.ExpiresAt != nil && !r.Pending {
		return Invalid("expires_at is only valid for pending transactions")
	}
	for id := range r.ExpectedVersions {
		if !slices.ContainsFunc(r.Entries, func(e EntryInput) bool { return e.AccountID == id }) {
			return Invalid("expected_versions references account %s which has no entry", id)
		}
	}
	return ValidateEntries(r.Entries)
}

// ValidateEntries checks entry shape and the double-entry rule: for every
// currency, total debits equal total credits.
func ValidateEntries(entries []EntryInput) error {
	if len(entries) < 2 {
		return Invalid("a transaction needs at least 2 entries")
	}
	if len(entries) > MaxEntriesPerTransaction {
		return Invalid("a transaction may have at most %d entries", MaxEntriesPerTransaction)
	}
	type sums struct{ debits, credits money.Amount }
	totals := map[string]*sums{}
	for i, e := range entries {
		if e.AccountID == uuid.Nil {
			return Invalid("entries[%d].account_id is required", i)
		}
		if !e.Direction.Valid() {
			return Invalid("entries[%d].direction must be DEBIT or CREDIT", i)
		}
		if e.Amount <= 0 {
			return Invalid("entries[%d].amount must be positive", i)
		}
		if !currencyRe.MatchString(e.Currency) {
			return Invalid("entries[%d].currency is invalid", i)
		}
		s := totals[e.Currency]
		if s == nil {
			s = &sums{}
			totals[e.Currency] = s
		}
		var err error
		if e.Direction == Debit {
			s.debits, err = money.Add(s.debits, e.Amount)
		} else {
			s.credits, err = money.Add(s.credits, e.Amount)
		}
		if err != nil {
			return E(KindUnprocessable, CodeAmountOverflow, "entry totals overflow in %s", e.Currency)
		}
	}
	for cur, s := range totals {
		if s.debits != s.credits {
			return E(KindUnprocessable, CodeUnbalanced,
				"debits (%s) must equal credits (%s) in %s", s.debits, s.credits, cur)
		}
	}
	return nil
}

func validateMetadata(m Metadata) error {
	if len(m) > MaxMetadataKeys {
		return Invalid("metadata may have at most %d keys", MaxMetadataKeys)
	}
	for k, v := range m {
		if k == "" || len(k) > MaxMetadataKeyLen {
			return Invalid("metadata keys must be 1-%d characters", MaxMetadataKeyLen)
		}
		if len(v) > MaxMetadataValueLen {
			return Invalid("metadata values must be at most %d characters", MaxMetadataValueLen)
		}
	}
	return nil
}

// MaxBatchSize bounds the number of transactions in one atomic batch.
const MaxBatchSize = 100

// BatchRequest posts several transactions atomically: all or none.
type BatchRequest struct {
	Transactions []CreateTransactionRequest `json:"transactions"`
}

// Validate checks every transaction in the batch.
func (r *BatchRequest) Validate() error {
	if len(r.Transactions) == 0 || len(r.Transactions) > MaxBatchSize {
		return Invalid("a batch must contain 1-%d transactions", MaxBatchSize)
	}
	for i := range r.Transactions {
		if err := r.Transactions[i].Validate(); err != nil {
			return Prefix(err, "transactions[%d]: ", i)
		}
	}
	return nil
}

// PostPendingRequest captures a pending transaction. With no entries the full
// held amounts are posted. Otherwise entries must be a balanced subset of the
// held legs (same account, direction and currency) with amounts no greater
// than held: a partial capture. Anything not captured is released.
type PostPendingRequest struct {
	Entries []EntryInput `json:"entries,omitempty"`
}

// ReverseRequest describes a reversal.
type ReverseRequest struct {
	Description string   `json:"description,omitempty"`
	Metadata    Metadata `json:"metadata,omitempty"`
}

// Validate checks the request.
func (r *ReverseRequest) Validate() error {
	if len(r.Description) > 1000 {
		return Invalid("description must be at most 1000 characters")
	}
	return validateMetadata(r.Metadata)
}
