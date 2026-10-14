package httpapi

import (
	"bytes"
	"net/http"

	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/domain"
	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/ledger"
)

// idempotentBody reads the body and Idempotency-Key and decodes the body into
// dst when it is non-empty (lifecycle endpoints accept an empty body).
func idempotentBody(r *http.Request, dst any) (*ledger.Idempotency, error) {
	body, err := readBody(r)
	if err != nil {
		return nil, err
	}
	idem, err := idempotencyFrom(r, body)
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(body)) > 0 {
		if err := decodeBytes(body, dst); err != nil {
			return nil, err
		}
	}
	return idem, nil
}

func (s *Server) postTransaction(w http.ResponseWriter, r *http.Request) {
	t, ok := s.transactionFromPath(w, r)
	if !ok {
		return
	}
	var req domain.PostPendingRequest
	idem, err := idempotentBody(r, &req)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	out, replayed, err := s.svc.PostPending(r.Context(), t.LedgerID, t.ID, req, idem)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeIdempotent(w, http.StatusOK, replayed, out)
}

func (s *Server) voidTransaction(w http.ResponseWriter, r *http.Request) {
	t, ok := s.transactionFromPath(w, r)
	if !ok {
		return
	}
	var req struct{}
	idem, err := idempotentBody(r, &req)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	out, replayed, err := s.svc.VoidPending(r.Context(), t.LedgerID, t.ID, idem)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeIdempotent(w, http.StatusOK, replayed, out)
}

func (s *Server) reverseTransaction(w http.ResponseWriter, r *http.Request) {
	t, ok := s.transactionFromPath(w, r)
	if !ok {
		return
	}
	var req domain.ReverseRequest
	idem, err := idempotentBody(r, &req)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	out, replayed, err := s.svc.Reverse(r.Context(), t.LedgerID, t.ID, req, idem)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeIdempotent(w, http.StatusCreated, replayed, out)
}

func (s *Server) createBatch(w http.ResponseWriter, r *http.Request) {
	ledgerID, err := pathUUID(r, "ledger_id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	body, err := readBody(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	idem, err := idempotencyFrom(r, body)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	var req domain.BatchRequest
	if err := decodeBytes(body, &req); err != nil {
		s.writeError(w, r, err)
		return
	}
	out, replayed, err := s.svc.CreateBatch(r.Context(), ledgerID, req, idem)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeIdempotent(w, http.StatusCreated, replayed, out)
}
