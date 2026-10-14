package ledger

import (
	"context"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/domain"
	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/store"
)

// CreateTransaction validates and atomically posts a journal entry.
//
// The write path is:
//  1. validate shape and per-currency balance (no I/O);
//  2. lock every referenced account with SELECT ... FOR UPDATE in id order,
//     so concurrent writers serialise per account and never deadlock;
//  3. apply entries to the in-memory account counters, assigning each entry the
//     next account version and the resulting balance;
//  4. enforce frozen-account and overdraft rules on the final state;
//  5. persist transaction, entries and counters in one pipelined batch.
//
// The database re-checks balance (deferred trigger), currency/ledger matching
// (composite FK) and overdraft (CHECK) on commit.
func (s *Service) CreateTransaction(ctx context.Context, ledgerID uuid.UUID, req domain.CreateTransactionRequest) (*domain.Transaction, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	var out *domain.Transaction
	err := store.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		accts, err := lockAccounts(ctx, tx, ledgerID, entryAccountIDs(req.Entries))
		if err != nil {
			return err
		}
		t, touched, err := s.buildTransaction(ledgerID, &req, accts)
		if err != nil {
			return err
		}
		if err := store.ApplyWrite(ctx, tx, store.Write{
			LedgerID: ledgerID,
			NewTxns:  []*domain.Transaction{t},
			Accounts: touched,
		}); err != nil {
			return err
		}
		out = t
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// buildTransaction turns a validated request into a transaction whose entries
// have been applied to the locked accounts. It returns the touched accounts.
func (s *Service) buildTransaction(ledgerID uuid.UUID, req *domain.CreateTransactionRequest, accts map[uuid.UUID]*domain.Account) (*domain.Transaction, []*domain.Account, error) {
	for id, want := range req.ExpectedVersions {
		if got := accts[id].Version; got != want {
			return nil, nil, domain.E(domain.KindConflict, domain.CodeVersionConflict,
				"account %s is at version %d, expected %d", id, got, want)
		}
	}
	now := s.now()
	t := &domain.Transaction{
		ID:          newID(),
		LedgerID:    ledgerID,
		Status:      domain.TxnPosted,
		Description: req.Description,
		ExternalRef: req.ExternalRef,
		EffectiveAt: now,
		PostedAt:    &now,
		Metadata:    req.Metadata,
	}
	if req.EffectiveAt != nil {
		t.EffectiveAt = req.EffectiveAt.UTC()
	}
	if t.Metadata == nil {
		t.Metadata = domain.Metadata{}
	}
	touched, err := applyEntries(t, req.Entries, accts, false)
	if err != nil {
		return nil, nil, err
	}
	return t, touched, nil
}

// applyEntries appends one entry per input to t, applying each to its account.
// After all entries are applied, the overdraft rule is checked on the final
// state, so a transaction that nets out on an account is judged on the net.
func applyEntries(t *domain.Transaction, inputs []domain.EntryInput, accts map[uuid.UUID]*domain.Account, pending bool) ([]*domain.Account, error) {
	touchedSet := map[uuid.UUID]bool{}
	for i, in := range inputs {
		a := accts[in.AccountID]
		if a.Currency != in.Currency {
			return nil, domain.E(domain.KindUnprocessable, domain.CodeCurrencyMismatch,
				"entries[%d]: account %s is denominated in %s, not %s", i, a.ID, a.Currency, in.Currency)
		}
		if err := a.CheckPostable(); err != nil {
			return nil, err
		}
		if err := a.Apply(in.Direction, in.Amount, pending); err != nil {
			return nil, err
		}
		bal, err := a.PostedBalance()
		if err != nil {
			return nil, err
		}
		t.Entries = append(t.Entries, domain.Entry{
			TransactionID:  t.ID,
			AccountID:      a.ID,
			Direction:      in.Direction,
			Amount:         in.Amount,
			Currency:       in.Currency,
			Pending:        pending,
			AccountVersion: a.Version,
			BalanceAfter:   bal,
		})
		touchedSet[a.ID] = true
	}
	return checkTouched(accts, touchedSet)
}

// checkTouched enforces the overdraft rule on touched accounts and returns
// them in id order.
func checkTouched(accts map[uuid.UUID]*domain.Account, touched map[uuid.UUID]bool) ([]*domain.Account, error) {
	ids := make([]uuid.UUID, 0, len(touched))
	for id := range touched {
		ids = append(ids, id)
	}
	slices.SortFunc(ids, compareUUID)
	out := make([]*domain.Account, 0, len(ids))
	for _, id := range ids {
		a := accts[id]
		if err := a.CheckAvailable(); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

// lockAccounts locks ids (deduplicated) in a ledger and fails if any is missing.
func lockAccounts(ctx context.Context, tx pgx.Tx, ledgerID uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]*domain.Account, error) {
	accts, err := store.LockAccounts(ctx, tx, ledgerID, ids)
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		if _, ok := accts[id]; !ok {
			return nil, domain.NotFound("account", id)
		}
	}
	return accts, nil
}

// entryAccountIDs returns the distinct account ids referenced by entries, sorted.
func entryAccountIDs(entries []domain.EntryInput) []uuid.UUID {
	ids := make([]uuid.UUID, 0, len(entries))
	for _, e := range entries {
		ids = append(ids, e.AccountID)
	}
	slices.SortFunc(ids, compareUUID)
	return slices.Compact(ids)
}

func compareUUID(a, b uuid.UUID) int {
	for i := range a {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}
