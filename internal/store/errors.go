package store

import (
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/domain"
)

// ErrNotFound is returned when a single-row lookup matches nothing.
var ErrNotFound = errors.New("store: not found")

// MapError converts known constraint violations into domain errors so that
// invariants enforced only by the database still surface as clean API errors.
func MapError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}
	switch pgErr.ConstraintName {
	case "accounts_ledger_code_key":
		return domain.E(domain.KindConflict, domain.CodeDuplicateAccountCode, "an account with this code already exists in the ledger")
	case "transactions_ledger_external_ref_key":
		return domain.E(domain.KindConflict, domain.CodeDuplicateExternalRef, "a transaction with this external_ref already exists in the ledger")
	case "transactions_reverses_id_key":
		return domain.E(domain.KindConflict, domain.CodeAlreadyReversed, "transaction has already been reversed")
	case "accounts_ledger_id_fkey", "transactions_ledger_id_fkey", "idempotency_keys_ledger_id_fkey", "api_keys_ledger_id_fkey":
		return domain.E(domain.KindNotFound, domain.CodeNotFound, "ledger not found")
	case "accounts_currency_fkey":
		return domain.Invalid("unsupported currency")
	case "accounts_non_negative_available":
		return domain.E(domain.KindUnprocessable, domain.CodeInsufficientFunds, "insufficient available balance")
	case "entries_balanced":
		return domain.E(domain.KindUnprocessable, domain.CodeUnbalanced, "%s", pgErr.Message)
	case "entries_account_fk":
		return domain.E(domain.KindUnprocessable, domain.CodeCurrencyMismatch, "entry currency or ledger does not match its account")
	}
	return err
}
