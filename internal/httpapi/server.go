// Package httpapi exposes the ledger over a JSON HTTP API.
package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Server holds the dependencies of the HTTP handlers.
type Server struct {
	log  *slog.Logger
	pool *pgxpool.Pool
}

// NewServer builds a Server.
func NewServer(log *slog.Logger, pool *pgxpool.Pool) *Server {
	return &Server{log: log, pool: pool}
}

// Handler returns the fully wired HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /readyz", s.readyz)

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
