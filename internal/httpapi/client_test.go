package httpapi_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/auth"
	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/httpapi"
	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/ledger"
	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/testutil"
)

// api is a tiny in-process client for exercising the full HTTP stack.
type api struct {
	t       *testing.T
	h       http.Handler
	headers map[string]string
}

func newAPI(t *testing.T) *api {
	t.Helper()
	testutil.SkipIfShort(t)
	return &api{t: t, h: testHandler(), headers: map[string]string{"Authorization": "Bearer " + adminKey}}
}

const adminKey = "test-admin-key"

func testHandler() http.Handler {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return httpapi.NewServer(httpapi.Deps{
		Log:    log,
		Pool:   testPool,
		Ledger: ledger.NewService(testPool),
		Auth:   auth.New(testPool, adminKey, time.Minute),
	}).Handler()
}

// as returns a copy of the client authenticating with a different token.
func (a *api) as(token string) *api {
	c := *a
	c.headers = map[string]string{"Authorization": "Bearer " + token}
	return &c
}

type resp struct {
	Status int
	Header http.Header
	Body   map[string]any
	Raw    []byte
}

func (a *api) do(method, path string, body any, hdr ...string) resp {
	a.t.Helper()
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			a.t.Fatal(err)
		}
		rdr = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range a.headers {
		req.Header.Set(k, v)
	}
	// Money-moving requests require an Idempotency-Key; default to a fresh one.
	if method == http.MethodPost && strings.Contains(path, "/transactions") {
		req.Header.Set("Idempotency-Key", uuid.NewString())
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		if hdr[i+1] == "" {
			req.Header.Del(hdr[i])
			continue
		}
		req.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	a.h.ServeHTTP(rec, req)
	out := resp{Status: rec.Code, Header: rec.Header(), Raw: rec.Body.Bytes()}
	_ = json.Unmarshal(out.Raw, &out.Body)
	return out
}

// must asserts the status code and returns the response.
func (r resp) must(t *testing.T, status int) resp {
	t.Helper()
	if r.Status != status {
		t.Fatalf("status %d, want %d: %s", r.Status, status, r.Raw)
	}
	return r
}

func (r resp) code() string { s, _ := r.Body["code"].(string); return s }

func (r resp) str(key string) string { return fmt.Sprint(r.Body[key]) }

// fixture creates a ledger and returns its id.
func (a *api) ledger(name string) string {
	a.t.Helper()
	return a.do("POST", "/v1/ledgers", map[string]any{"name": name}).must(a.t, 201).str("id")
}

// account creates an account and returns its id.
func (a *api) account(ledgerID, code, typ, currency string, allowNegative bool) string {
	a.t.Helper()
	return a.do("POST", "/v1/ledgers/"+ledgerID+"/accounts", map[string]any{
		"code": code, "name": code, "type": typ, "currency": currency, "allow_negative": allowNegative,
	}).must(a.t, 201).str("id")
}

func entry(account, dir, amount, currency string) map[string]any {
	return map[string]any{"account_id": account, "direction": dir, "amount": amount, "currency": currency}
}

// transfer posts a simple two-legged USD transaction.
func (a *api) transfer(ledgerID, from, to, amount string) resp {
	a.t.Helper()
	return a.do("POST", "/v1/ledgers/"+ledgerID+"/transactions", map[string]any{
		"entries": []any{entry(from, "DEBIT", amount, "USD"), entry(to, "CREDIT", amount, "USD")},
	})
}

// balances returns the live balances object of an account.
func (a *api) balances(accountID string) map[string]any {
	a.t.Helper()
	r := a.do("GET", "/v1/accounts/"+accountID, nil).must(a.t, 200)
	return r.Body["balances"].(map[string]any)
}
