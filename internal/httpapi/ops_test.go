package httpapi_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/auth"
	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/httpapi"
	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/ledger"
	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/testutil"
)

func TestRateLimiting(t *testing.T) {
	testutil.SkipIfShort(t)
	h := httpapi.NewServer(httpapi.Deps{
		Log:            slog.New(slog.NewTextHandler(io.Discard, nil)),
		Pool:           testPool,
		Ledger:         ledger.NewService(testPool),
		Auth:           auth.New(testPool, adminKey, time.Minute),
		RateLimitRPS:   1,
		RateLimitBurst: 2,
	}).Handler()
	a := &api{t: t, h: h, headers: map[string]string{"Authorization": "Bearer " + adminKey}}
	a.do("GET", "/v1/ledgers", nil).must(t, 200)
	a.do("GET", "/v1/ledgers", nil).must(t, 200)
	r := a.do("GET", "/v1/ledgers", nil).must(t, 429)
	if r.code() != "rate_limited" || r.Header.Get("Retry-After") == "" {
		t.Fatalf("429 response: %s %v", r.Raw, r.Header)
	}
}

func TestMetricsEndpoint(t *testing.T) {
	a := newAPI(t)
	a.ledger("metrics")
	rec := httptest.NewRecorder()
	a.h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := rec.Body.String()
	for _, want := range []string{
		`ledger_http_requests_total{code="201",route="POST /v1/ledgers"}`,
		`ledger_http_request_duration_seconds_bucket`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics missing %s", want)
		}
	}
}

func TestWebhookManagement(t *testing.T) {
	a := newAPI(t)
	l := a.ledger("hooks")
	path := "/v1/ledgers/" + l + "/webhooks"
	a.do("POST", path, map[string]any{"url": "ftp://x"}).must(t, 400)
	a.do("POST", path, map[string]any{"url": "https://example.com/h", "event_types": []string{"nope"}}).must(t, 400)
	r := a.do("POST", path, map[string]any{"url": "https://example.com/h"}).must(t, 201)
	if !strings.HasPrefix(r.str("secret"), "whsec_") || len(r.Body["event_types"].([]any)) != 4 {
		t.Fatalf("webhook: %s", r.Raw)
	}
	list := a.do("GET", path, nil).must(t, 200)
	if strings.Contains(string(list.Raw), "whsec_") {
		t.Fatal("secret leaked in listing")
	}
	a.do("DELETE", path+"/"+r.str("id"), nil).must(t, 204)
}
