package httpapi_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/httpapi"
	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/testutil"
)

func TestHealthEndpoints(t *testing.T) {
	testutil.SkipIfShort(t)
	h := httpapi.NewServer(slog.New(slog.NewTextHandler(io.Discard, nil)), testPool, nil).Handler()

	for _, path := range []string{"/healthz", "/readyz"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d, body %s", path, rec.Code, rec.Body)
		}
		if rec.Header().Get("X-Request-ID") == "" {
			t.Fatalf("%s: missing X-Request-ID", path)
		}
	}
}
