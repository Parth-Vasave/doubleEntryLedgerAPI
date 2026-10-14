package httpapi_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/testutil"
)

func TestHealthEndpoints(t *testing.T) {
	testutil.SkipIfShort(t)
	h := testHandler()

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
