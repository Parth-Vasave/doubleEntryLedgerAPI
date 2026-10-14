package httpapi

import (
	"net/http"

	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/domain"
	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/observability"
)

var kindStatus = map[domain.Kind]int{
	domain.KindInvalid:       http.StatusBadRequest,
	domain.KindUnprocessable: http.StatusUnprocessableEntity,
	domain.KindNotFound:      http.StatusNotFound,
	domain.KindConflict:      http.StatusConflict,
	domain.KindUnauthorized:  http.StatusUnauthorized,
	domain.KindForbidden:     http.StatusForbidden,
}

var kindTitle = map[domain.Kind]string{
	domain.KindInvalid:       "Invalid Request",
	domain.KindUnprocessable: "Unprocessable Request",
	domain.KindNotFound:      "Not Found",
	domain.KindConflict:      "Conflict",
	domain.KindUnauthorized:  "Unauthorized",
	domain.KindForbidden:     "Forbidden",
}

// writeError renders err as a problem response. Domain errors keep their
// code; anything else is logged and hidden behind a generic 500.
func (s *Server) writeError(w http.ResponseWriter, r *http.Request, err error) {
	if de, ok := domain.AsError(err); ok {
		status, ok := kindStatus[de.Kind]
		if !ok {
			status = http.StatusBadRequest
		}
		writeProblem(w, r, status, de.Code, kindTitle[de.Kind], de.Message)
		return
	}
	if r.Context().Err() != nil {
		writeProblem(w, r, 499, "client_closed_request", "Client Closed Request", "")
		return
	}
	s.log.ErrorContext(r.Context(), "internal error", "error", err,
		"request_id", observability.RequestID(r.Context()), "path", r.URL.Path)
	writeProblem(w, r, http.StatusInternalServerError, "internal_error", "Internal Server Error", "")
}

// decodeBody decodes the JSON body and reports a validation error on failure.
func decodeBody(r *http.Request, dst any) error {
	if err := decodeJSON(r, dst); err != nil {
		return domain.Invalid("invalid JSON body: %v", err)
	}
	return nil
}
