package domain

import (
	"errors"
	"fmt"
)

// Kind classifies an error so the transport layer can map it to a status code.
type Kind int

// Error kinds.
const (
	KindInvalid      Kind = iota + 1 // malformed request
	KindUnprocessable                // well-formed but violates a business rule
	KindNotFound
	KindConflict
	KindUnauthorized
	KindForbidden
)

// Stable, documented error codes returned to API clients.
const (
	CodeValidation           = "validation_error"
	CodeUnbalanced           = "unbalanced_transaction"
	CodeCurrencyMismatch     = "currency_mismatch"
	CodeInsufficientFunds    = "insufficient_funds"
	CodeAccountFrozen        = "account_frozen"
	CodeNotFound             = "not_found"
	CodeDuplicateAccountCode = "duplicate_account_code"
	CodeDuplicateExternalRef = "duplicate_external_ref"
	CodeInvalidState         = "invalid_state"
	CodeAlreadyReversed      = "already_reversed"
	CodeIdempotencyReused    = "idempotency_key_reused"
	CodeIdempotencyMissing   = "idempotency_key_required"
	CodeVersionConflict      = "version_conflict"
	CodeAmountOverflow       = "amount_overflow"
	CodeUnauthorized         = "unauthorized"
	CodeForbidden            = "forbidden"
)

// Error is a typed domain error.
type Error struct {
	Kind    Kind
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// E builds a domain error.
func E(kind Kind, code, format string, args ...any) *Error {
	return &Error{Kind: kind, Code: code, Message: fmt.Sprintf(format, args...)}
}

// Invalid is shorthand for a validation error.
func Invalid(format string, args ...any) *Error {
	return E(KindInvalid, CodeValidation, format, args...)
}

// NotFound is shorthand for a not-found error.
func NotFound(what string, id any) *Error {
	return E(KindNotFound, CodeNotFound, "%s %v not found", what, id)
}

// AsError extracts a *Error from err.
func AsError(err error) (*Error, bool) {
	var de *Error
	ok := errors.As(err, &de)
	return de, ok
}

// IsCode reports whether err is a domain error with the given code.
func IsCode(err error, code string) bool {
	de, ok := AsError(err)
	return ok && de.Code == code
}
