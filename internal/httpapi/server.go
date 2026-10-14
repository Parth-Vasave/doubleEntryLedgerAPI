// Package httpapi exposes the ledger over a JSON HTTP API.
package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/auth"
	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/ledger"
)

// Deps are the Server's collaborators.
type Deps struct {
	Log    *slog.Logger
	Pool   *pgxpool.Pool
	Ledger *ledger.Service
	Auth   *auth.Authenticator
}

// Server holds the dependencies of the HTTP handlers.
type Server struct {
	log  *slog.Logger
	pool *pgxpool.Pool
	svc  *ledger.Service
	auth *auth.Authenticator
}

// NewServer builds a Server.
func NewServer(d Deps) *Server {
	return &Server{log: d.Log, pool: d.Pool, svc: d.Ledger, auth: d.Auth}
}

// Handler returns the fully wired HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	s.handle(mux, "GET /healthz", accessPublic, s.healthz)
	s.handle(mux, "GET /readyz", accessPublic, s.readyz)

	s.handle(mux, "POST /v1/api-keys", accessAdmin, s.createAPIKey)
	s.handle(mux, "GET /v1/api-keys", accessAdmin, s.listAPIKeys)
	s.handle(mux, "DELETE /v1/api-keys/{key_id}", accessAdmin, s.revokeAPIKey)

	s.handle(mux, "POST /v1/ledgers", accessAdmin, s.createLedger)
	s.handle(mux, "GET /v1/ledgers", accessRead, s.listLedgers)
	s.handle(mux, "GET /v1/ledgers/{ledger_id}", accessRead, s.getLedger)
	s.handle(mux, "GET /v1/ledgers/{ledger_id}/trial-balance", accessRead, s.trialBalance)

	s.handle(mux, "POST /v1/ledgers/{ledger_id}/accounts", accessWrite, s.createAccount)
	s.handle(mux, "GET /v1/ledgers/{ledger_id}/accounts", accessRead, s.listAccounts)
	s.handle(mux, "GET /v1/accounts/{account_id}", accessRead, s.getAccount)
	s.handle(mux, "PATCH /v1/accounts/{account_id}", accessWrite, s.updateAccount)
	s.handle(mux, "GET /v1/accounts/{account_id}/balance", accessRead, s.getBalance)
	s.handle(mux, "GET /v1/accounts/{account_id}/entries", accessRead, s.listEntries)

	s.handle(mux, "POST /v1/ledgers/{ledger_id}/transactions", accessWrite, s.createTransaction)
	s.handle(mux, "GET /v1/ledgers/{ledger_id}/transactions", accessRead, s.listTransactions)
	s.handle(mux, "GET /v1/transactions/{transaction_id}", accessRead, s.getTransaction)

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
