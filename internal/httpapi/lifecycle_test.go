package httpapi_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/ledger"
)

type walletFixture struct {
	a              *api
	l, bank, alice string
	merchant       string
}

// newWallets creates a funded wallet (Alice, $100) and a merchant.
func newWallets(t *testing.T) walletFixture {
	a := newAPI(t)
	f := walletFixture{a: a, l: a.ledger("holds")}
	f.bank = a.account(f.l, "bank", "ASSET", "USD", true)
	f.alice = a.account(f.l, "alice", "LIABILITY", "USD", false)
	f.merchant = a.account(f.l, "merchant", "LIABILITY", "USD", false)
	a.transfer(f.l, f.bank, f.alice, "10000").must(t, 201)
	return f
}

func (f walletFixture) hold(t *testing.T, amount string, extra map[string]any) resp {
	t.Helper()
	body := map[string]any{
		"pending": true,
		"entries": []any{entry(f.alice, "DEBIT", amount, "USD"), entry(f.merchant, "CREDIT", amount, "USD")},
	}
	for k, v := range extra {
		body[k] = v
	}
	return f.a.do("POST", "/v1/ledgers/"+f.l+"/transactions", body)
}

func wantBalances(t *testing.T, a *api, account string, posted, available string) {
	t.Helper()
	b := a.balances(account)
	if b["posted"] != posted || b["available"] != available {
		t.Fatalf("balances = posted %v available %v, want %s / %s", b["posted"], b["available"], posted, available)
	}
}

func TestHoldThenPost(t *testing.T) {
	f := newWallets(t)
	h := f.hold(t, "3000", nil).must(t, 201)
	if h.str("status") != "PENDING" {
		t.Fatalf("status = %s", h.str("status"))
	}
	// Funds are reserved: posted unchanged, available reduced.
	wantBalances(t, f.a, f.alice, "10000", "7000")
	wantBalances(t, f.a, f.merchant, "0", "0")
	if p := f.a.balances(f.merchant)["pending"]; p != "3000" {
		t.Fatalf("merchant pending = %v", p)
	}
	// Held funds cannot be double-spent.
	if r := f.a.transfer(f.l, f.alice, f.merchant, "7001"); r.must(t, 422).code() != "insufficient_funds" {
		t.Fatalf("code = %s", r.code())
	}

	posted := f.a.do("POST", "/v1/transactions/"+h.str("id")+"/post", nil).must(t, 200)
	if posted.str("status") != "POSTED" || len(posted.Body["entries"].([]any)) != 4 {
		t.Fatalf("posted transaction: %s", posted.Raw)
	}
	wantBalances(t, f.a, f.alice, "7000", "7000")
	wantBalances(t, f.a, f.merchant, "3000", "3000")

	// A settled transaction cannot be posted or voided again.
	if r := f.a.do("POST", "/v1/transactions/"+h.str("id")+"/post", nil); r.must(t, 409).code() != "invalid_state" {
		t.Fatalf("code = %s", r.code())
	}
	f.a.do("POST", "/v1/transactions/"+h.str("id")+"/void", nil).must(t, 409)
}

func TestHoldPartialCapture(t *testing.T) {
	f := newWallets(t)
	// Card-style authorization for $50, captured for $42.
	h := f.hold(t, "5000", nil).must(t, 201)
	path := "/v1/transactions/" + h.str("id") + "/post"

	over := map[string]any{"entries": []any{entry(f.alice, "DEBIT", "5001", "USD"), entry(f.merchant, "CREDIT", "5001", "USD")}}
	f.a.do("POST", path, over).must(t, 422)
	wrongLeg := map[string]any{"entries": []any{entry(f.merchant, "DEBIT", "1", "USD"), entry(f.alice, "CREDIT", "1", "USD")}}
	f.a.do("POST", path, wrongLeg).must(t, 400)

	partial := map[string]any{"entries": []any{entry(f.alice, "DEBIT", "4200", "USD"), entry(f.merchant, "CREDIT", "4200", "USD")}}
	f.a.do("POST", path, partial).must(t, 200)
	// The uncaptured $8 is released back to Alice.
	wantBalances(t, f.a, f.alice, "5800", "5800")
	wantBalances(t, f.a, f.merchant, "4200", "4200")
}

func TestHoldVoid(t *testing.T) {
	f := newWallets(t)
	h := f.hold(t, "2500", nil).must(t, 201)
	v := f.a.do("POST", "/v1/transactions/"+h.str("id")+"/void", nil).must(t, 200)
	if v.str("status") != "VOIDED" || v.Body["voided_at"] == nil {
		t.Fatalf("void: %s", v.Raw)
	}
	wantBalances(t, f.a, f.alice, "10000", "10000")
	if p := f.a.balances(f.merchant)["pending"]; p != "0" {
		t.Fatalf("merchant pending = %v", p)
	}
}

func TestHoldExpiry(t *testing.T) {
	f := newWallets(t)
	f.hold(t, "100", map[string]any{"expires_at": time.Now().Add(-time.Minute)}).must(t, 400)
	f.hold(t, "100", map[string]any{"expires_at": time.Now().Add(time.Hour), "pending": false}).must(t, 400)

	h := f.hold(t, "4000", map[string]any{"expires_at": time.Now().Add(150 * time.Millisecond)}).must(t, 201)
	wantBalances(t, f.a, f.alice, "10000", "6000")
	time.Sleep(200 * time.Millisecond)

	// Posting an expired hold is refused even before the sweeper runs.
	if r := f.a.do("POST", "/v1/transactions/"+h.str("id")+"/post", nil); r.must(t, 409).code() != "invalid_state" {
		t.Fatalf("code = %s", r.code())
	}
	n, err := ledger.NewService(testPool).ExpireHolds(context.Background(), 1000)
	if err != nil || n < 1 {
		t.Fatalf("ExpireHolds = %d, %v", n, err)
	}
	if st := f.a.do("GET", "/v1/transactions/"+h.str("id"), nil).must(t, 200).str("status"); st != "VOIDED" {
		t.Fatalf("status after expiry = %s", st)
	}
	wantBalances(t, f.a, f.alice, "10000", "10000")
}

func TestReversal(t *testing.T) {
	f := newWallets(t)
	pay := f.a.transfer(f.l, f.alice, f.merchant, "1234").must(t, 201)
	path := "/v1/transactions/" + pay.str("id") + "/reverse"

	rev := f.a.do("POST", path, map[string]any{"description": "refund"}).must(t, 201)
	if rev.str("reverses_id") != pay.str("id") || rev.str("description") != "refund" {
		t.Fatalf("reversal: %s", rev.Raw)
	}
	for _, e := range rev.Body["entries"].([]any) {
		e := e.(map[string]any)
		if (e["account_id"] == f.alice) != (e["direction"] == "CREDIT") {
			t.Fatalf("reversal entry has wrong direction: %v", e)
		}
	}
	wantBalances(t, f.a, f.alice, "10000", "10000")
	wantBalances(t, f.a, f.merchant, "0", "0")

	orig := f.a.do("GET", "/v1/transactions/"+pay.str("id"), nil).must(t, 200)
	if orig.str("reversed_by_id") != rev.str("id") || orig.str("status") != "POSTED" {
		t.Fatalf("original after reversal: %s", orig.Raw)
	}
	if r := f.a.do("POST", path, nil); r.must(t, 409).code() != "already_reversed" {
		t.Fatalf("code = %s", r.code())
	}
	// Pending transactions are voided, not reversed.
	h := f.hold(t, "1", nil).must(t, 201)
	f.a.do("POST", "/v1/transactions/"+h.str("id")+"/reverse", nil).must(t, 409)
}

func TestBatchIsAtomic(t *testing.T) {
	f := newWallets(t)
	bob := f.a.account(f.l, "bob", "LIABILITY", "USD", false)
	path := "/v1/ledgers/" + f.l + "/transactions/batch"

	// Alice pays Bob $60, then Bob pays the merchant $50: the second
	// transaction relies on the balance produced by the first.
	ok := map[string]any{"transactions": []any{
		map[string]any{"entries": []any{entry(f.alice, "DEBIT", "6000", "USD"), entry(bob, "CREDIT", "6000", "USD")}},
		map[string]any{"entries": []any{entry(bob, "DEBIT", "5000", "USD"), entry(f.merchant, "CREDIT", "5000", "USD")}},
	}}
	r := f.a.do("POST", path, ok).must(t, 201)
	if n := len(r.Body["transactions"].([]any)); n != 2 {
		t.Fatalf("batch returned %d transactions", n)
	}
	wantBalances(t, f.a, bob, "1000", "1000")

	// The second leg overdraws Bob, so nothing in the batch is applied.
	bad := map[string]any{"transactions": []any{
		map[string]any{"entries": []any{entry(f.alice, "DEBIT", "100", "USD"), entry(bob, "CREDIT", "100", "USD")}},
		map[string]any{"entries": []any{entry(bob, "DEBIT", "9999", "USD"), entry(f.merchant, "CREDIT", "9999", "USD")}},
	}}
	r = f.a.do("POST", path, bad).must(t, 422)
	if r.code() != "insufficient_funds" || !strings.HasPrefix(r.str("detail"), "transactions[1]") {
		t.Fatalf("batch error: %s", r.Raw)
	}
	wantBalances(t, f.a, f.alice, "4000", "4000")
	wantBalances(t, f.a, bob, "1000", "1000")
}

func TestLifecycleIdempotency(t *testing.T) {
	f := newWallets(t)
	h := f.hold(t, "700", nil).must(t, 201)
	path := "/v1/transactions/" + h.str("id") + "/post"
	f.a.do("POST", path, nil, "Idempotency-Key", "capture-1").must(t, 200)
	again := f.a.do("POST", path, nil, "Idempotency-Key", "capture-1").must(t, 200)
	if again.Header.Get("Idempotent-Replayed") != "true" {
		t.Fatal("retried capture was not replayed")
	}
	wantBalances(t, f.a, f.merchant, "700", "700")
}
