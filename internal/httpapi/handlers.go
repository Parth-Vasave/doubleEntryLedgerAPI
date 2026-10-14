package httpapi

import (
	"net/http"
	"strconv"

	"github.com/google/uuid"

	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/auth"
	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/domain"
	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/store"
)

// ---- ledgers ---------------------------------------------------------------

func (s *Server) createLedger(w http.ResponseWriter, r *http.Request) {
	var req domain.CreateLedgerRequest
	if err := decodeBody(r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}
	l, err := s.svc.CreateLedger(r.Context(), req)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, l)
}

func (s *Server) getLedger(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "ledger_id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	l, err := s.svc.GetLedger(r.Context(), id)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, l)
}

func (s *Server) listLedgers(w http.ResponseWriter, r *http.Request) {
	limit, err := queryLimit(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	before, err := uuidCursor(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	var only []uuid.UUID
	if p := auth.FromContext(r.Context()); p != nil && !p.Admin && p.LedgerID != nil {
		only = []uuid.UUID{*p.LedgerID}
	}
	ls, err := s.svc.ListLedgers(r.Context(), only, before, limit+1)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, newPage(ls, limit, func(l domain.Ledger) string { return l.ID.String() }))
}

func (s *Server) trialBalance(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "ledger_id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	rows, err := s.svc.TrialBalance(r.Context(), id)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	balanced := true
	for _, row := range rows {
		balanced = balanced && row.Balanced
	}
	writeJSON(w, http.StatusOK, map[string]any{"ledger_id": id, "balanced": balanced, "currencies": rows})
}

// ---- accounts --------------------------------------------------------------

func (s *Server) createAccount(w http.ResponseWriter, r *http.Request) {
	ledgerID, err := pathUUID(r, "ledger_id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	var req domain.CreateAccountRequest
	if err := decodeBody(r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}
	a, err := s.svc.CreateAccount(r.Context(), ledgerID, req)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, a)
}

func (s *Server) listAccounts(w http.ResponseWriter, r *http.Request) {
	ledgerID, err := pathUUID(r, "ledger_id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	limit, err := queryLimit(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	before, err := uuidCursor(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	as, err := s.svc.ListAccounts(r.Context(), ledgerID, before, limit+1)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, newPage(as, limit, func(a *domain.Account) string { return a.ID.String() }))
}

func (s *Server) getAccount(w http.ResponseWriter, r *http.Request) {
	a, ok := s.accountFromPath(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, a)
}

func (s *Server) updateAccount(w http.ResponseWriter, r *http.Request) {
	a, ok := s.accountFromPath(w, r)
	if !ok {
		return
	}
	var req domain.UpdateAccountRequest
	if err := decodeBody(r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}
	a, err := s.svc.UpdateAccount(r.Context(), a.ID, req)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, a)
}

func (s *Server) getBalance(w http.ResponseWriter, r *http.Request) {
	a, ok := s.accountFromPath(w, r)
	if !ok {
		return
	}
	asOf, err := queryTime(r, "as_of")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	b, err := s.svc.GetBalance(r.Context(), a.ID, asOf)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, b)
}

func (s *Server) listEntries(w http.ResponseWriter, r *http.Request) {
	a, ok := s.accountFromPath(w, r)
	if !ok {
		return
	}
	var f store.EntryFilter
	var err error
	if f.Limit, err = queryLimit(r); err != nil {
		s.writeError(w, r, err)
		return
	}
	if f.BeforeVersion, err = int64Cursor(r); err != nil {
		s.writeError(w, r, err)
		return
	}
	if f.From, err = queryTime(r, "from"); err != nil {
		s.writeError(w, r, err)
		return
	}
	if f.To, err = queryTime(r, "to"); err != nil {
		s.writeError(w, r, err)
		return
	}
	limit := f.Limit
	f.Limit++
	es, err := s.svc.ListEntries(r.Context(), a.ID, f)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, newPage(es, limit, func(e domain.Entry) string {
		return strconv.FormatInt(e.AccountVersion, 10)
	}))
}

// accountFromPath loads the {account_id} path account and checks the caller
// may access its ledger.
func (s *Server) accountFromPath(w http.ResponseWriter, r *http.Request) (*domain.Account, bool) {
	id, err := pathUUID(r, "account_id")
	if err != nil {
		s.writeError(w, r, err)
		return nil, false
	}
	a, err := s.svc.GetAccount(r.Context(), id)
	if err != nil {
		s.writeError(w, r, err)
		return nil, false
	}
	if err := s.authorizeLedger(r, a.LedgerID); err != nil {
		// Hide existence of resources in other ledgers.
		s.writeError(w, r, domain.NotFound("account", id))
		return nil, false
	}
	return a, true
}

// ---- transactions ----------------------------------------------------------

func (s *Server) createTransaction(w http.ResponseWriter, r *http.Request) {
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
	var req domain.CreateTransactionRequest
	if err := decodeBytes(body, &req); err != nil {
		s.writeError(w, r, err)
		return
	}
	t, replayed, err := s.svc.CreateTransaction(r.Context(), ledgerID, req, idem)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeIdempotent(w, http.StatusCreated, replayed, t)
}

func (s *Server) getTransaction(w http.ResponseWriter, r *http.Request) {
	t, ok := s.transactionFromPath(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) listTransactions(w http.ResponseWriter, r *http.Request) {
	ledgerID, err := pathUUID(r, "ledger_id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	var f store.TxnFilter
	if f.Limit, err = queryLimit(r); err != nil {
		s.writeError(w, r, err)
		return
	}
	if f.Before, err = uuidCursor(r); err != nil {
		s.writeError(w, r, err)
		return
	}
	if st := r.URL.Query().Get("status"); st != "" {
		status := domain.TxnStatus(st)
		switch status {
		case domain.TxnPending, domain.TxnPosted, domain.TxnVoided:
		default:
			s.writeError(w, r, domain.Invalid("status must be PENDING, POSTED or VOIDED"))
			return
		}
		f.Status = &status
	}
	limit := f.Limit
	f.Limit++
	ts, err := s.svc.ListTransactions(r.Context(), ledgerID, f)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, newPage(ts, limit, func(t *domain.Transaction) string { return t.ID.String() }))
}

// transactionFromPath loads the {transaction_id} path transaction and checks
// the caller may access its ledger.
func (s *Server) transactionFromPath(w http.ResponseWriter, r *http.Request) (*domain.Transaction, bool) {
	id, err := pathUUID(r, "transaction_id")
	if err != nil {
		s.writeError(w, r, err)
		return nil, false
	}
	t, err := s.svc.GetTransaction(r.Context(), id)
	if err != nil {
		s.writeError(w, r, err)
		return nil, false
	}
	if err := s.authorizeLedger(r, t.LedgerID); err != nil {
		s.writeError(w, r, domain.NotFound("transaction", id))
		return nil, false
	}
	return t, true
}
