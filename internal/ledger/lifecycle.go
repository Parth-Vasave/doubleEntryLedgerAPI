package ledger

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/domain"
	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/money"
	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/observability"
	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/store"
)

// legKey identifies one held leg of a pending transaction.
type legKey struct {
	account   uuid.UUID
	direction domain.Direction
	currency  string
}

// PostPending captures a pending transaction (in full, or partially).
//
// The transaction row is locked first, then its accounts in id order. Every
// outstanding hold is released and the captured amounts are posted as new
// entries; whatever is not captured simply returns to the available balance.
func (s *Service) PostPending(ctx context.Context, ledgerID, txnID uuid.UUID, req domain.PostPendingRequest, idem *Idempotency) (*domain.Transaction, bool, error) {
	if len(req.Entries) > 0 {
		if err := domain.ValidateEntries(req.Entries); err != nil {
			return nil, false, err
		}
	}
	return recordSettled("captured")(s.settle(ctx, ledgerID, txnID, idem, func(t *domain.Transaction, accts map[uuid.UUID]*domain.Account) ([]*domain.Entry, error) {
		held := map[legKey]money.Amount{}
		for _, e := range t.Entries {
			k := legKey{e.AccountID, e.Direction, e.Currency}
			held[k] += e.Amount
		}
		capture := req.Entries
		if len(capture) == 0 {
			for _, e := range t.Entries {
				capture = append(capture, domain.EntryInput{
					AccountID: e.AccountID, Direction: e.Direction, Amount: e.Amount, Currency: e.Currency,
				})
			}
		}
		captured := map[legKey]money.Amount{}
		for i, in := range capture {
			k := legKey{in.AccountID, in.Direction, in.Currency}
			h, ok := held[k]
			if !ok {
				return nil, domain.Invalid("entries[%d] does not match a held leg of the pending transaction", i)
			}
			captured[k] += in.Amount
			if captured[k] > h {
				return nil, domain.E(domain.KindUnprocessable, domain.CodeValidation,
					"entries[%d]: capture of %s exceeds held amount %s", i, captured[k], h)
			}
		}

		for _, e := range t.Entries {
			if err := accts[e.AccountID].ReleaseHold(e.Direction, e.Amount); err != nil {
				return nil, err
			}
		}
		posted := &domain.Transaction{ID: t.ID}
		if _, err := applyEntries(posted, capture, accts, false); err != nil {
			return nil, err
		}
		out := make([]*domain.Entry, len(posted.Entries))
		for i := range posted.Entries {
			out[i] = &posted.Entries[i]
		}
		return out, nil
	}, domain.TxnPosted))
}

// recordSettled returns a pass-through for a settle result that updates
// metrics on success.
func recordSettled(kind string) func(*domain.Transaction, bool, error) (*domain.Transaction, bool, error) {
	return func(t *domain.Transaction, replayed bool, err error) (*domain.Transaction, bool, error) {
		if err == nil {
			recordCommitted(replayed, kind)
		}
		return t, replayed, err
	}
}

// VoidPending cancels a pending transaction, releasing all of its holds.
func (s *Service) VoidPending(ctx context.Context, ledgerID, txnID uuid.UUID, idem *Idempotency) (*domain.Transaction, bool, error) {
	t, replayed, err := s.settle(ctx, ledgerID, txnID, idem, voidHolds, domain.TxnVoided)
	if err == nil {
		recordCommitted(replayed, "voided")
	}
	return t, replayed, err
}

func voidHolds(t *domain.Transaction, accts map[uuid.UUID]*domain.Account) ([]*domain.Entry, error) {
	for _, e := range t.Entries {
		if err := accts[e.AccountID].ReleaseHold(e.Direction, e.Amount); err != nil {
			return nil, err
		}
	}
	return nil, nil
}

// settle is the shared skeleton of post and void.
func (s *Service) settle(ctx context.Context, ledgerID, txnID uuid.UUID, idem *Idempotency,
	apply func(*domain.Transaction, map[uuid.UUID]*domain.Account) ([]*domain.Entry, error),
	to domain.TxnStatus,
) (*domain.Transaction, bool, error) {
	var (
		out      *domain.Transaction
		replayed bool
	)
	err := store.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, replayed, err = idempotent(ctx, tx, ledgerID, idem, 200, func() (*domain.Transaction, error) {
			return s.settleTx(ctx, tx, ledgerID, txnID, apply, to)
		})
		return err
	})
	return out, replayed, err
}

func (s *Service) settleTx(ctx context.Context, tx pgx.Tx, ledgerID, txnID uuid.UUID,
	apply func(*domain.Transaction, map[uuid.UUID]*domain.Account) ([]*domain.Entry, error),
	to domain.TxnStatus,
) (*domain.Transaction, error) {
	t, err := store.GetTransactionForUpdate(ctx, tx, txnID)
	if err != nil || t.LedgerID != ledgerID {
		if err == nil || errors.Is(err, store.ErrNotFound) {
			return nil, domain.NotFound("transaction", txnID)
		}
		return nil, err
	}
	if t.Status != domain.TxnPending {
		return nil, domain.E(domain.KindConflict, domain.CodeInvalidState,
			"transaction %s is %s; only PENDING transactions can be posted or voided", t.ID, t.Status)
	}
	now := s.now()
	if to == domain.TxnPosted && t.ExpiresAt != nil && !now.Before(*t.ExpiresAt) {
		return nil, domain.E(domain.KindConflict, domain.CodeInvalidState, "pending transaction %s has expired", t.ID)
	}

	ids := make([]uuid.UUID, 0, len(t.Entries))
	for _, e := range t.Entries {
		ids = append(ids, e.AccountID)
	}
	ids = sortedUnique(ids)
	accts, err := lockAccounts(ctx, tx, ledgerID, ids)
	if err != nil {
		return nil, err
	}
	newEntries, err := apply(t, accts)
	if err != nil {
		return nil, err
	}
	all := make([]*domain.Account, 0, len(ids))
	for _, id := range ids {
		all = append(all, accts[id])
	}
	if err := store.ApplyWrite(ctx, tx, store.Write{
		LedgerID:     ledgerID,
		StatusChange: &store.StatusChange{ID: t.ID, Status: to, At: now},
		Entries:      newEntries,
		Accounts:     all,
	}); err != nil {
		return nil, err
	}
	t, err = store.GetTransaction(ctx, tx, t.ID)
	if err != nil {
		return nil, err
	}
	return t, store.InsertEvents(ctx, tx, []store.Event{txnEvent(t)})
}

// Reverse posts a new transaction that exactly undoes a POSTED transaction's
// posted entries. History is never edited: the original stays as it was and
// the pair nets to zero. A transaction can be reversed at most once
// (enforced by a unique constraint, which also settles concurrent attempts).
func (s *Service) Reverse(ctx context.Context, ledgerID, txnID uuid.UUID, req domain.ReverseRequest, idem *Idempotency) (*domain.Transaction, bool, error) {
	if err := req.Validate(); err != nil {
		return nil, false, err
	}
	var (
		out      *domain.Transaction
		replayed bool
	)
	err := store.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, replayed, err = idempotent(ctx, tx, ledgerID, idem, 201, func() (*domain.Transaction, error) {
			orig, err := store.GetTransaction(ctx, tx, txnID)
			if err != nil || orig.LedgerID != ledgerID {
				if err == nil || errors.Is(err, store.ErrNotFound) {
					return nil, domain.NotFound("transaction", txnID)
				}
				return nil, err
			}
			if orig.Status != domain.TxnPosted {
				return nil, domain.E(domain.KindConflict, domain.CodeInvalidState,
					"transaction %s is %s; only POSTED transactions can be reversed", orig.ID, orig.Status)
			}
			if orig.ReversedByID != nil {
				return nil, domain.E(domain.KindConflict, domain.CodeAlreadyReversed,
					"transaction %s was already reversed by %s", orig.ID, *orig.ReversedByID)
			}
			var inputs []domain.EntryInput
			for _, e := range orig.Entries {
				if e.Pending {
					continue
				}
				inputs = append(inputs, domain.EntryInput{
					AccountID: e.AccountID, Direction: e.Direction.Opposite(), Amount: e.Amount, Currency: e.Currency,
				})
			}
			desc := req.Description
			if desc == "" {
				desc = "Reversal of " + orig.ID.String()
			}
			rr := domain.CreateTransactionRequest{Description: desc, Metadata: req.Metadata, Entries: inputs}
			accts, err := lockAccounts(ctx, tx, ledgerID, entryAccountIDs(inputs))
			if err != nil {
				return nil, err
			}
			t, touched, err := s.buildTransaction(ledgerID, &rr, accts)
			if err != nil {
				return nil, err
			}
			t.ReversesID = &orig.ID
			err = store.ApplyWrite(ctx, tx, store.Write{LedgerID: ledgerID, NewTxns: []*domain.Transaction{t}, Accounts: touched})
			if err != nil {
				return nil, err
			}
			return t, store.InsertEvents(ctx, tx, []store.Event{txnEvent(t)})
		})
		return err
	})
	if err == nil {
		recordCommitted(replayed, "reversed")
	}
	return out, replayed, err
}

// BatchResult is the response to a batch posting.
type BatchResult struct {
	Transactions []*domain.Transaction `json:"transactions"`
}

// CreateBatch posts several transactions atomically. All accounts referenced
// anywhere in the batch are locked up front in id order; transactions are then
// applied in request order, so later ones see the balances left by earlier
// ones. Any failure rolls back the whole batch.
func (s *Service) CreateBatch(ctx context.Context, ledgerID uuid.UUID, req domain.BatchRequest, idem *Idempotency) (*BatchResult, bool, error) {
	if err := req.Validate(); err != nil {
		return nil, false, err
	}
	var (
		out      *BatchResult
		replayed bool
	)
	err := store.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, replayed, err = idempotent(ctx, tx, ledgerID, idem, 201, func() (*BatchResult, error) {
			var ids []uuid.UUID
			for _, r := range req.Transactions {
				for _, e := range r.Entries {
					ids = append(ids, e.AccountID)
				}
			}
			accts, err := lockAccounts(ctx, tx, ledgerID, sortedUnique(ids))
			if err != nil {
				return nil, err
			}
			res := &BatchResult{}
			touched := map[uuid.UUID]*domain.Account{}
			for i := range req.Transactions {
				t, ta, err := s.buildTransaction(ledgerID, &req.Transactions[i], accts)
				if err != nil {
					return nil, domain.Prefix(err, "transactions[%d]: ", i)
				}
				for _, a := range ta {
					touched[a.ID] = a
				}
				res.Transactions = append(res.Transactions, t)
			}
			var tids []uuid.UUID
			for id := range touched {
				tids = append(tids, id)
			}
			write := store.Write{LedgerID: ledgerID, NewTxns: res.Transactions}
			for _, id := range sortedUnique(tids) {
				write.Accounts = append(write.Accounts, touched[id])
			}
			if err := store.ApplyWrite(ctx, tx, write); err != nil {
				return nil, err
			}
			events := make([]store.Event, len(res.Transactions))
			for i, t := range res.Transactions {
				events[i] = txnEvent(t)
			}
			return res, store.InsertEvents(ctx, tx, events)
		})
		return err
	})
	if err == nil {
		if replayed {
			observability.IdempotentReplays.Inc()
		} else {
			for _, t := range out.Transactions {
				recordCommitted(false, kindOf(t))
			}
		}
	}
	return out, replayed, err
}

// ExpireHolds voids up to limit pending transactions whose expires_at has
// passed, each in its own database transaction. Candidates are claimed with
// FOR UPDATE SKIP LOCKED so several workers can run concurrently without
// contending for the same rows. It returns how many were voided.
func (s *Service) ExpireHolds(ctx context.Context, limit int) (int, error) {
	n := 0
	for n < limit {
		var done bool
		err := store.InTx(ctx, s.pool, func(tx pgx.Tx) error {
			id, ledgerID, err := store.ClaimExpiredPending(ctx, tx, s.now())
			if errors.Is(err, store.ErrNotFound) {
				done = true
				return nil
			}
			if err != nil {
				return err
			}
			_, err = s.settleTx(ctx, tx, ledgerID, id, voidHolds, domain.TxnVoided)
			return err
		})
		if err != nil {
			return n, err
		}
		if done {
			return n, nil
		}
		n++
		observability.HoldsExpired.Inc()
		observability.Transactions.WithLabelValues("voided").Inc()
	}
	return n, nil
}
