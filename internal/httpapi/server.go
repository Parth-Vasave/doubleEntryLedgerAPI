// Package httpapi exposes the ledger over a JSON HTTP API.
package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/ledger"
)

// Server holds the dependencies of the HTTP handlers.
type Server struct {
	log  *slog.Logger
	pool *pgxpool.Pool
	svc  *ledger.Service
}

// NewServer builds a Server.
func NewServer(log *slog.Logger, pool *pgxpool.Pool, svc *ledger.Service) *Server {
	return &Server{log: log, pool: pool, svc: svc}
}

// Handler returns the fully wired HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /readyz", s.readyz)

	mux.HandleFunc("POST /v1/ledgers", s.createLedger)
	mux.HandleFunc("GET /v1/ledgers", s.listLedgers)
	mux.HandleFunc("GET /v1/ledgers/{ledger_id}", s.getLedger)
	mux.HandleFunc("GET /v1/ledgers/{ledger_id}/trial-balance", s.trialBalance)

	mux.HandleFunc("POST /v1/ledgers/{ledger_id}/accounts", s.createAccount)
	mux.HandleFunc("GET /v1/ledgers/{ledger_id}/accounts", s.listAccounts)
	mux.HandleFunc("GET /v1/accounts/{account_id}", s.getAccount)
	mux.HandleFunc("PATCH /v1/accounts/{account_id}", s.updateAccount)
	mux.HandleFunc("GET /v1/accounts/{account_id}/balance", s.getBalance)
	mux.HandleFunc("GET /v1/accounts/{account_id}/entries", s.listEntries)

	mux.HandleFunc("POST /v1/ledgers/{ledger_id}/transactions", s.createTransaction)
	mux.HandleFunc("GET /v1/ledgers/{ledger_id}/transactions", s.listTransactions)
	mux.HandleFunc("GET /v1/transactions/{transaction_id}", s.getTransaction)

	return chain(mux,
		requestID,
		accessLog(s.log),
		recoverer(s.log),
		maxBody(1<<20),
	)
}

func (s *Server) healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.pool.Ping(ctx); err != nil {
		writeProblem(w, r, http.StatusServiceUnavailable, "not_ready", "Database Unavailable", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}
