package httpapi

import (
	"net/http"

	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/ledger"
	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/store"
)

func (s *Server) createWebhook(w http.ResponseWriter, r *http.Request) {
	ledgerID, err := pathUUID(r, "ledger_id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	var req ledger.CreateWebhookRequest
	if err := decodeBody(r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}
	wh, err := s.svc.CreateWebhook(r.Context(), ledgerID, req)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, wh)
}

func (s *Server) listWebhooks(w http.ResponseWriter, r *http.Request) {
	ledgerID, err := pathUUID(r, "ledger_id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	ws, err := s.svc.ListWebhooks(r.Context(), ledgerID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, page[store.Webhook]{Data: ws})
}

func (s *Server) deleteWebhook(w http.ResponseWriter, r *http.Request) {
	ledgerID, err := pathUUID(r, "ledger_id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	id, err := pathUUID(r, "webhook_id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.svc.DeleteWebhook(r.Context(), ledgerID, id); err != nil {
		s.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
