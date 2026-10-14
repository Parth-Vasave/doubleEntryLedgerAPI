package httpapi

import (
	"encoding/base64"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/domain"
)

const (
	defaultPageSize = 50
	maxPageSize     = 200
)

// page is the envelope for list responses. Pagination is keyset-based: the
// opaque next_cursor encodes the last item's sort key, so pages stay stable
// and O(log n) no matter how deep the client pages.
type page[T any] struct {
	Data       []T    `json:"data"`
	NextCursor string `json:"next_cursor,omitempty"`
}

// newPage trims a result fetched with limit+1 rows and computes the cursor.
func newPage[T any](items []T, limit int, key func(T) string) page[T] {
	p := page[T]{Data: items}
	if p.Data == nil {
		p.Data = []T{}
	}
	if len(items) > limit {
		p.Data = items[:limit]
		p.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(key(items[limit-1])))
	}
	return p
}

func pathUUID(r *http.Request, name string) (uuid.UUID, error) {
	id, err := uuid.Parse(r.PathValue(name))
	if err != nil {
		return uuid.Nil, domain.Invalid("%s must be a UUID", name)
	}
	return id, nil
}

func queryLimit(r *http.Request) (int, error) {
	s := r.URL.Query().Get("limit")
	if s == "" {
		return defaultPageSize, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > maxPageSize {
		return 0, domain.Invalid("limit must be between 1 and %d", maxPageSize)
	}
	return n, nil
}

func decodeCursor(r *http.Request) (string, bool, error) {
	c := r.URL.Query().Get("cursor")
	if c == "" {
		return "", false, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return "", false, domain.Invalid("invalid cursor")
	}
	return string(b), true, nil
}

func uuidCursor(r *http.Request) (*uuid.UUID, error) {
	s, ok, err := decodeCursor(r)
	if err != nil || !ok {
		return nil, err
	}
	id, err := uuid.Parse(s)
	if err != nil {
		return nil, domain.Invalid("invalid cursor")
	}
	return &id, nil
}

func int64Cursor(r *http.Request) (*int64, error) {
	s, ok, err := decodeCursor(r)
	if err != nil || !ok {
		return nil, err
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return nil, domain.Invalid("invalid cursor")
	}
	return &n, nil
}

func queryTime(r *http.Request, name string) (*time.Time, error) {
	s := r.URL.Query().Get(name)
	if s == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return nil, domain.Invalid("%s must be an RFC 3339 timestamp", name)
	}
	return &t, nil
}
