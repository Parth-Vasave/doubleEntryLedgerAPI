package httpapi

import (
	"crypto/sha256"
	"io"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/auth"
	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/domain"
	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/ledger"
	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/observability"
)

// access is the permission a route requires.
type access int

const (
	accessPublic access = iota
	accessRead
	accessWrite
	accessAdmin
)

// handle registers h on mux behind authentication and authorization.
func (s *Server) handle(mux *http.ServeMux, pattern string, acc access, h http.HandlerFunc) {
	mux.Handle(pattern, s.authorize(acc, h))
}

// authorize authenticates the bearer token, checks the route's required
// access, and checks any {ledger_id} path parameter against the key's ledger.
func (s *Server) authorize(acc access, next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if acc == accessPublic {
			next(w, r)
			return
		}
		p, err := s.auth.Authenticate(r.Context(), bearerToken(r))
		if err != nil {
			w.Header().Set("WWW-Authenticate", `Bearer realm="ledger"`)
			s.writeError(w, r, err)
			return
		}
		if ok, retryAfter := s.rl.allow(p.ID()); !ok {
			observability.RateLimited.Inc()
			w.Header().Set("Retry-After", retryAfter)
			writeProblem(w, r, http.StatusTooManyRequests, "rate_limited", "Too Many Requests",
				"rate limit exceeded for this API key")
			return
		}
		var allowed bool
		switch acc {
		case accessAdmin:
			allowed = p.Admin
		case accessRead:
			allowed = p.Can(auth.ScopeRead)
		case accessWrite:
			allowed = p.Can(auth.ScopeWrite)
		}
		if !allowed {
			s.writeError(w, r, domain.E(domain.KindForbidden, domain.CodeForbidden, "API key lacks permission for this operation"))
			return
		}
		if raw := r.PathValue("ledger_id"); raw != "" {
			id, err := uuid.Parse(raw)
			if err != nil {
				s.writeError(w, r, domain.Invalid("ledger_id must be a UUID"))
				return
			}
			if !p.CanAccessLedger(id) {
				// Do not reveal whether ledgers outside the key's scope exist.
				s.writeError(w, r, domain.NotFound("ledger", id))
				return
			}
		}
		next(w, r.WithContext(auth.WithPrincipal(r.Context(), p)))
	})
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "Bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

// authorizeLedger checks the caller may access a resource's ledger.
func (s *Server) authorizeLedger(r *http.Request, ledgerID uuid.UUID) error {
	p := auth.FromContext(r.Context())
	if p == nil || !p.CanAccessLedger(ledgerID) {
		return domain.E(domain.KindForbidden, domain.CodeForbidden, "no access to ledger")
	}
	return nil
}

// readBody reads the raw request body (bounded by the maxBody middleware).
func readBody(r *http.Request) ([]byte, error) {
	b, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, domain.Invalid("could not read request body: %v", err)
	}
	return b, nil
}

// idempotencyFrom extracts the Idempotency-Key header and fingerprints the
// request so that reusing a key for a different request is detected.
func idempotencyFrom(r *http.Request, body []byte) (*ledger.Idempotency, error) {
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		return nil, domain.E(domain.KindInvalid, domain.CodeIdempotencyMissing,
			"the Idempotency-Key header is required for requests that move money")
	}
	if len(key) > 255 {
		return nil, domain.Invalid("Idempotency-Key must be at most 255 characters")
	}
	h := sha256.New()
	h.Write([]byte(r.Method))
	h.Write([]byte{0})
	h.Write([]byte(r.URL.Path))
	h.Write([]byte{0})
	h.Write(body)
	return &ledger.Idempotency{Key: key, Hash: h.Sum(nil)}, nil
}

// writeIdempotent writes a (possibly replayed) idempotent response.
func writeIdempotent(w http.ResponseWriter, status int, replayed bool, v any) {
	if replayed {
		w.Header().Set("Idempotent-Replayed", "true")
	}
	writeJSON(w, status, v)
}

// ---- API key management (admin) ---------------------------------------------

func (s *Server) createAPIKey(w http.ResponseWriter, r *http.Request) {
	var req auth.CreateKeyRequest
	if err := decodeBody(r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}
	if req.LedgerID != nil {
		if _, err := s.svc.GetLedger(r.Context(), *req.LedgerID); err != nil {
			s.writeError(w, r, err)
			return
		}
	}
	k, err := s.auth.CreateKey(r.Context(), req)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, k)
}

func (s *Server) listAPIKeys(w http.ResponseWriter, r *http.Request) {
	ks, err := s.auth.ListKeys(r.Context())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, page[auth.APIKey]{Data: ks})
}

func (s *Server) revokeAPIKey(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "key_id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.auth.RevokeKey(r.Context(), id); err != nil {
		s.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
