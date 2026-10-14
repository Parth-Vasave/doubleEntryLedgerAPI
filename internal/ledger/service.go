// Package ledger implements the ledger's use cases on top of the store,
// owning transaction boundaries, locking and invariant checks.
package ledger

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/domain"
	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/money"
	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/store"
)

// Service is the ledger application service.
type Service struct {
	pool *pgxpool.Pool
	now  func() time.Time
}

// NewService builds a Service.
func NewService(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool, now: func() time.Time { return time.Now().UTC() }}
}

func newID() uuid.UUID { return uuid.Must(uuid.NewV7()) }

// notFound maps store.ErrNotFound to a domain not-found error.
func notFound(err error, what string, id uuid.UUID) error {
	if errors.Is(err, store.ErrNotFound) {
		return domain.NotFound(what, id)
	}
	return err
}

// CreateLedger creates a ledger.
func (s *Service) CreateLedger(ctx context.Context, req domain.CreateLedgerRequest) (*domain.Ledger, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	l := &domain.Ledger{ID: newID(), Name: req.Name, Metadata: req.Metadata}
	if l.Metadata == nil {
		l.Metadata = domain.Metadata{}
	}
	if err := store.InsertLedger(ctx, s.pool, l); err != nil {
		return nil, err
	}
	return l, nil
}

// GetLedger returns a ledger.
func (s *Service) GetLedger(ctx context.Context, id uuid.UUID) (*domain.Ledger, error) {
	l, err := store.GetLedger(ctx, s.pool, id)
	return l, notFound(err, "ledger", id)
}

// ListLedgers lists ledgers, optionally restricted to ids.
func (s *Service) ListLedgers(ctx context.Context, ids []uuid.UUID, before *uuid.UUID, limit int) ([]domain.Ledger, error) {
	return store.ListLedgers(ctx, s.pool, ids, before, limit)
}

// CreateAccount opens an account in a ledger.
func (s *Service) CreateAccount(ctx context.Context, ledgerID uuid.UUID, req domain.CreateAccountRequest) (*domain.Account, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	a := &domain.Account{
		ID:            newID(),
		LedgerID:      ledgerID,
		Code:          req.Code,
		Name:          req.Name,
		Type:          req.Type,
		Currency:      req.Currency,
		AllowNegative: req.AllowNegative,
		Metadata:      req.Metadata,
	}
	if err := store.InsertAccount(ctx, s.pool, a); err != nil {
		return nil, err
	}
	return a, nil
}

// GetAccount returns an account with its current balances.
func (s *Service) GetAccount(ctx context.Context, id uuid.UUID) (*domain.Account, error) {
	a, err := store.GetAccount(ctx, s.pool, id)
	return a, notFound(err, "account", id)
}

// ListAccounts lists a ledger's accounts.
func (s *Service) ListAccounts(ctx context.Context, ledgerID uuid.UUID, before *uuid.UUID, limit int) ([]*domain.Account, error) {
	return store.ListAccounts(ctx, s.pool, ledgerID, before, limit)
}

// UpdateAccount changes an account's name, status or metadata.
func (s *Service) UpdateAccount(ctx context.Context, id uuid.UUID, req domain.UpdateAccountRequest) (*domain.Account, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	var out *domain.Account
	err := store.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		a, err := store.GetAccountForUpdate(ctx, tx, id)
		if err != nil {
			return notFound(err, "account", id)
		}
		if req.Name != nil {
			a.Name = *req.Name
		}
		if req.Status != nil {
			a.Status = *req.Status
		}
		if req.Metadata != nil {
			a.Metadata = req.Metadata
		}
		if err := store.UpdateAccountAttributes(ctx, tx, a); err != nil {
			return err
		}
		out = a
		return nil
	})
	return out, err
}

// AccountBalance is a balance view, optionally at a past point in time.
type AccountBalance struct {
	AccountID uuid.UUID        `json:"account_id"`
	Currency  string           `json:"currency"`
	Version   int64            `json:"version"`
	AsOf      *time.Time       `json:"as_of,omitempty"`
	Balances  *domain.Balances `json:"balances,omitempty"`
	// Posted is set for historical queries, which only track settled balances.
	Posted *money.Amount `json:"posted,omitempty"`
}

// GetBalance returns the account's live balances, or its posted balance at asOf.
func (s *Service) GetBalance(ctx context.Context, id uuid.UUID, asOf *time.Time) (*AccountBalance, error) {
	a, err := s.GetAccount(ctx, id)
	if err != nil {
		return nil, err
	}
	out := &AccountBalance{AccountID: a.ID, Currency: a.Currency}
	if asOf == nil {
		b, err := a.Balances()
		if err != nil {
			return nil, err
		}
		out.Version, out.Balances = a.Version, &b
		return out, nil
	}
	bal, ver, err := store.PostedBalanceAt(ctx, s.pool, id, *asOf)
	if err != nil {
		return nil, err
	}
	out.AsOf, out.Posted, out.Version = asOf, &bal, ver
	return out, nil
}

// ListEntries returns an account statement page.
func (s *Service) ListEntries(ctx context.Context, accountID uuid.UUID, f store.EntryFilter) ([]domain.Entry, error) {
	if _, err := s.GetAccount(ctx, accountID); err != nil {
		return nil, err
	}
	return store.ListEntries(ctx, s.pool, accountID, f)
}

// GetTransaction returns a transaction with its entries.
func (s *Service) GetTransaction(ctx context.Context, id uuid.UUID) (*domain.Transaction, error) {
	t, err := store.GetTransaction(ctx, s.pool, id)
	return t, notFound(err, "transaction", id)
}

// ListTransactions lists a ledger's transactions.
func (s *Service) ListTransactions(ctx context.Context, ledgerID uuid.UUID, f store.TxnFilter) ([]*domain.Transaction, error) {
	return store.ListTransactions(ctx, s.pool, ledgerID, f)
}

// TrialBalance returns per-currency debit/credit totals for a ledger.
func (s *Service) TrialBalance(ctx context.Context, ledgerID uuid.UUID) ([]store.TrialBalanceRow, error) {
	if _, err := s.GetLedger(ctx, ledgerID); err != nil {
		return nil, err
	}
	return store.TrialBalance(ctx, s.pool, ledgerID)
}
