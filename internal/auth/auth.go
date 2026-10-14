// Package auth implements API-key authentication and ledger-scoped authorization.
//
// Keys are random 192-bit secrets shown to the client once and stored only as
// SHA-256 hashes. Because the secrets are high-entropy, a fast hash is
// appropriate (unlike passwords) and allows indexed lookup by hash.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/domain"
	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/store"
)

// Scope is a permission granted to an API key.
type Scope string

// Scopes.
const (
	ScopeRead  Scope = "read"
	ScopeWrite Scope = "write"
)

const keyPrefix = "lk_"

// Principal is the authenticated caller.
type Principal struct {
	KeyID    uuid.UUID  // zero for the admin key
	Name     string     // key name, for logs
	Admin    bool       // the bootstrap admin key: all ledgers, all scopes, key management
	LedgerID *uuid.UUID // nil = all ledgers
	Scopes   []Scope
}

// Can reports whether the principal holds scope.
func (p *Principal) Can(s Scope) bool { return p.Admin || slices.Contains(p.Scopes, s) }

// CanAccessLedger reports whether the principal may act on ledgerID.
func (p *Principal) CanAccessLedger(ledgerID uuid.UUID) bool {
	return p.Admin || p.LedgerID == nil || *p.LedgerID == ledgerID
}

// ID returns a stable identifier for rate limiting and logging.
func (p *Principal) ID() string {
	if p.Admin {
		return "admin"
	}
	return p.KeyID.String()
}

// APIKey is the public view of a stored key.
type APIKey struct {
	ID        uuid.UUID  `json:"id"`
	Name      string     `json:"name"`
	Prefix    string     `json:"prefix"`
	LedgerID  *uuid.UUID `json:"ledger_id,omitempty"`
	Scopes    []Scope    `json:"scopes"`
	CreatedAt time.Time  `json:"created_at"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
	// Secret is only populated in the response that creates the key.
	Secret string `json:"secret,omitempty"`
}

// CreateKeyRequest mints a new API key.
type CreateKeyRequest struct {
	Name     string     `json:"name"`
	LedgerID *uuid.UUID `json:"ledger_id,omitempty"`
	Scopes   []Scope    `json:"scopes"`
}

// ErrUnauthenticated is returned for missing, unknown or revoked keys.
var ErrUnauthenticated = domain.E(domain.KindUnauthorized, domain.CodeUnauthorized, "missing or invalid API key")

// Authenticator verifies bearer tokens.
type Authenticator struct {
	pool      *pgxpool.Pool
	adminHash [32]byte
	ttl       time.Duration

	mu    sync.Mutex
	cache map[[32]byte]cached
}

type cached struct {
	p   *Principal
	exp time.Time
}

// New returns an Authenticator. adminKey is the bootstrap key from config.
// Successful lookups are cached for cacheTTL, which bounds how long a revoked
// key may keep working on other instances.
func New(pool *pgxpool.Pool, adminKey string, cacheTTL time.Duration) *Authenticator {
	return &Authenticator{
		pool:      pool,
		adminHash: sha256.Sum256([]byte(adminKey)),
		ttl:       cacheTTL,
		cache:     map[[32]byte]cached{},
	}
}

// Authenticate resolves a bearer token to a principal.
func (a *Authenticator) Authenticate(ctx context.Context, token string) (*Principal, error) {
	if token == "" {
		return nil, ErrUnauthenticated
	}
	h := sha256.Sum256([]byte(token))
	if subtle.ConstantTimeCompare(h[:], a.adminHash[:]) == 1 {
		return &Principal{Admin: true, Name: "admin"}, nil
	}
	if !strings.HasPrefix(token, keyPrefix) {
		return nil, ErrUnauthenticated
	}

	now := time.Now()
	a.mu.Lock()
	if c, ok := a.cache[h]; ok && now.Before(c.exp) {
		a.mu.Unlock()
		return c.p, nil
	}
	a.mu.Unlock()

	var (
		p      Principal
		scopes []string
	)
	err := a.pool.QueryRow(ctx, `
		SELECT id, name, ledger_id, scopes FROM api_keys
		 WHERE key_hash = $1 AND revoked_at IS NULL`, h[:]).Scan(&p.KeyID, &p.Name, &p.LedgerID, &scopes)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrUnauthenticated
	}
	if err != nil {
		return nil, err
	}
	for _, s := range scopes {
		p.Scopes = append(p.Scopes, Scope(s))
	}
	a.mu.Lock()
	a.cache[h] = cached{p: &p, exp: now.Add(a.ttl)}
	a.mu.Unlock()
	return &p, nil
}

// CreateKey mints a key and returns it with its secret.
func (a *Authenticator) CreateKey(ctx context.Context, req CreateKeyRequest) (*APIKey, error) {
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || len(req.Name) > 200 {
		return nil, domain.Invalid("name is required and must be at most 200 characters")
	}
	if len(req.Scopes) == 0 {
		return nil, domain.Invalid("scopes must include read and/or write")
	}
	for _, s := range req.Scopes {
		if s != ScopeRead && s != ScopeWrite {
			return nil, domain.Invalid("unknown scope %q", s)
		}
	}
	var raw [24]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return nil, err
	}
	secret := keyPrefix + base64.RawURLEncoding.EncodeToString(raw[:])
	h := sha256.Sum256([]byte(secret))
	k := &APIKey{
		ID:       uuid.Must(uuid.NewV7()),
		Name:     req.Name,
		Prefix:   secret[:len(keyPrefix)+6],
		LedgerID: req.LedgerID,
		Scopes:   req.Scopes,
		Secret:   secret,
	}
	scopes := make([]string, len(k.Scopes))
	for i, s := range k.Scopes {
		scopes[i] = string(s)
	}
	err := a.pool.QueryRow(ctx, `
		INSERT INTO api_keys (id, name, prefix, key_hash, ledger_id, scopes)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING created_at`,
		k.ID, k.Name, k.Prefix, h[:], k.LedgerID, scopes).Scan(&k.CreatedAt)
	if err != nil {
		return nil, store.MapError(err)
	}
	return k, nil
}

// ListKeys returns all keys, newest first (never their secrets).
func (a *Authenticator) ListKeys(ctx context.Context) ([]APIKey, error) {
	rows, err := a.pool.Query(ctx, `
		SELECT id, name, prefix, ledger_id, scopes, created_at, revoked_at
		  FROM api_keys ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []APIKey{}
	for rows.Next() {
		var k APIKey
		var scopes []string
		if err := rows.Scan(&k.ID, &k.Name, &k.Prefix, &k.LedgerID, &scopes, &k.CreatedAt, &k.RevokedAt); err != nil {
			return nil, err
		}
		for _, s := range scopes {
			k.Scopes = append(k.Scopes, Scope(s))
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// RevokeKey revokes a key and evicts it from this instance's cache.
func (a *Authenticator) RevokeKey(ctx context.Context, id uuid.UUID) error {
	var hash []byte
	err := a.pool.QueryRow(ctx, `
		UPDATE api_keys SET revoked_at = coalesce(revoked_at, now())
		 WHERE id = $1 RETURNING key_hash`, id).Scan(&hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.NotFound("api key", id)
	}
	if err != nil {
		return err
	}
	var h [32]byte
	copy(h[:], hash)
	a.mu.Lock()
	delete(a.cache, h)
	a.mu.Unlock()
	return nil
}

type ctxKey struct{}

// WithPrincipal stores p on the context.
func WithPrincipal(ctx context.Context, p *Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

// FromContext returns the principal stored on ctx, if any.
func FromContext(ctx context.Context) *Principal {
	p, _ := ctx.Value(ctxKey{}).(*Principal)
	return p
}
