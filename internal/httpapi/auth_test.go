package httpapi_test

import (
	"testing"

	"github.com/google/uuid"
)

func TestAuthentication(t *testing.T) {
	a := newAPI(t)
	if r := a.as("").do("GET", "/v1/ledgers", nil); r.must(t, 401).code() != "unauthorized" {
		t.Fatalf("code = %s", r.code())
	}
	if r := a.as("not-a-key").do("GET", "/v1/ledgers", nil); r.Status != 401 || r.Header.Get("WWW-Authenticate") == "" {
		t.Fatalf("status %d, headers %v", r.Status, r.Header)
	}
	// Health checks stay public.
	a.as("").do("GET", "/healthz", nil).must(t, 200)
}

func TestScopedAPIKeys(t *testing.T) {
	a := newAPI(t)
	mine := a.ledger("mine")
	theirs := a.ledger("theirs")
	theirAcct := a.account(theirs, "x", "ASSET", "USD", true)

	mint := func(ledger string, scopes ...string) (id, secret string) {
		r := a.do("POST", "/v1/api-keys", map[string]any{"name": "k", "ledger_id": ledger, "scopes": scopes}).must(t, 201)
		return r.str("id"), r.str("secret")
	}
	_, rw := mint(mine, "read", "write")
	_, ro := mint(mine, "read")

	writer := a.as(rw)
	src := writer.account(mine, "src", "ASSET", "USD", true)
	dst := writer.account(mine, "dst", "LIABILITY", "USD", false)
	writer.transfer(mine, src, dst, "100").must(t, 201)

	reader := a.as(ro)
	reader.do("GET", "/v1/accounts/"+dst, nil).must(t, 200)
	reader.do("GET", "/v1/ledgers/"+mine+"/transactions", nil).must(t, 200)
	if r := reader.transfer(mine, src, dst, "1"); r.must(t, 403).code() != "forbidden" {
		t.Fatalf("code = %s", r.code())
	}

	// A ledger-scoped key cannot see another ledger, its accounts, or admin APIs.
	writer.do("GET", "/v1/ledgers/"+theirs, nil).must(t, 404)
	writer.do("GET", "/v1/accounts/"+theirAcct, nil).must(t, 404)
	writer.do("POST", "/v1/ledgers/"+theirs+"/accounts", map[string]any{"code": "y", "name": "y", "type": "ASSET", "currency": "USD"}).must(t, 404)
	writer.do("POST", "/v1/ledgers", map[string]any{"name": "nope"}).must(t, 403)
	writer.do("GET", "/v1/api-keys", nil).must(t, 403)

	// Listing ledgers only shows the key's own ledger.
	r := writer.do("GET", "/v1/ledgers", nil).must(t, 200)
	if data := r.Body["data"].([]any); len(data) != 1 || data[0].(map[string]any)["id"] != mine {
		t.Fatalf("scoped ledger list: %s", r.Raw)
	}

	// Secrets are never returned again after creation.
	list := a.do("GET", "/v1/api-keys", nil).must(t, 200)
	for _, k := range list.Body["data"].([]any) {
		if _, ok := k.(map[string]any)["secret"]; ok {
			t.Fatal("secret leaked in key listing")
		}
	}
}

func TestRevokedKeyIsRejected(t *testing.T) {
	a := newAPI(t)
	r := a.do("POST", "/v1/api-keys", map[string]any{"name": "temp", "scopes": []string{"read"}}).must(t, 201)
	key := a.as(r.str("secret"))
	key.do("GET", "/v1/ledgers", nil).must(t, 200)
	a.do("DELETE", "/v1/api-keys/"+r.str("id"), nil).must(t, 204)
	key.do("GET", "/v1/ledgers", nil).must(t, 401)
	a.do("DELETE", "/v1/api-keys/"+uuid.NewString(), nil).must(t, 404)
}

func TestIdempotencyOverHTTP(t *testing.T) {
	a := newAPI(t)
	l := a.ledger("idem")
	src := a.account(l, "src", "ASSET", "USD", true)
	dst := a.account(l, "dst", "LIABILITY", "USD", false)
	body := map[string]any{"entries": []any{entry(src, "DEBIT", "250", "USD"), entry(dst, "CREDIT", "250", "USD")}}
	path := "/v1/ledgers/" + l + "/transactions"

	first := a.do("POST", path, body, "Idempotency-Key", "key-1").must(t, 201)
	if first.Header.Get("Idempotent-Replayed") != "" {
		t.Fatal("first response marked as replay")
	}
	second := a.do("POST", path, body, "Idempotency-Key", "key-1").must(t, 201)
	if second.Header.Get("Idempotent-Replayed") != "true" || second.str("id") != first.str("id") {
		t.Fatalf("replay mismatch: %s vs %s", second.Raw, first.Raw)
	}
	if got := a.balances(dst)["posted"]; got != "250" {
		t.Fatalf("balance %v: request executed twice", got)
	}

	body["description"] = "changed"
	if r := a.do("POST", path, body, "Idempotency-Key", "key-1"); r.must(t, 422).code() != "idempotency_key_reused" {
		t.Fatalf("code = %s", r.code())
	}
	if r := a.do("POST", path, body, "Idempotency-Key", ""); r.must(t, 400).code() != "idempotency_key_required" {
		t.Fatalf("code = %s", r.code())
	}
}
