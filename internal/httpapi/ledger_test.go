package httpapi_test

import (
	"fmt"
	"net/url"
	"testing"
)

// TestPaymentFlow walks a typical wallet scenario end to end and checks every
// balance against hand-computed accounting.
func TestPaymentFlow(t *testing.T) {
	a := newAPI(t)
	l := a.ledger("payments")
	cash := a.account(l, "assets:cash", "ASSET", "USD", false)
	alice := a.account(l, "liabilities:wallet:alice", "LIABILITY", "USD", false)
	bob := a.account(l, "liabilities:wallet:bob", "LIABILITY", "USD", false)
	fees := a.account(l, "revenue:fees", "REVENUE", "USD", false)

	// Deposit $100 into Alice's wallet: cash (asset) up, wallet (liability) up.
	a.do("POST", "/v1/ledgers/"+l+"/transactions", map[string]any{
		"description": "deposit",
		"entries":     []any{entry(cash, "DEBIT", "10000", "USD"), entry(alice, "CREDIT", "10000", "USD")},
	}).must(t, 201)

	// Alice pays Bob $25 with a $1 fee: a three-legged split transaction.
	tx := a.do("POST", "/v1/ledgers/"+l+"/transactions", map[string]any{
		"description": "p2p",
		"metadata":    map[string]string{"order": "42"},
		"entries": []any{
			entry(alice, "DEBIT", "2600", "USD"),
			entry(bob, "CREDIT", "2500", "USD"),
			entry(fees, "CREDIT", "100", "USD"),
		},
	}).must(t, 201)
	if tx.str("status") != "POSTED" || len(tx.Body["entries"].([]any)) != 3 {
		t.Fatalf("unexpected transaction: %s", tx.Raw)
	}

	for id, want := range map[string]string{cash: "10000", alice: "7400", bob: "2500", fees: "100"} {
		if got := a.balances(id)["posted"]; got != want {
			t.Errorf("account %s posted = %v, want %s", id, got, want)
		}
	}

	// Alice cannot spend more than she has.
	r := a.transfer(l, alice, bob, "7401").must(t, 422)
	if r.code() != "insufficient_funds" {
		t.Fatalf("code = %s", r.code())
	}
	// ...but can spend exactly all of it.
	a.transfer(l, alice, bob, "7400").must(t, 201)

	// The ledger as a whole still balances.
	tb := a.do("GET", "/v1/ledgers/"+l+"/trial-balance", nil).must(t, 200)
	if tb.Body["balanced"] != true {
		t.Fatalf("trial balance not balanced: %s", tb.Raw)
	}
	usd := tb.Body["currencies"].([]any)[0].(map[string]any)
	if usd["total_debits"] != "20000" || usd["total_credits"] != "20000" {
		t.Fatalf("trial balance totals: %v", usd)
	}
}

func TestTransactionValidationErrors(t *testing.T) {
	a := newAPI(t)
	l := a.ledger("validation")
	usd1 := a.account(l, "usd1", "ASSET", "USD", true)
	usd2 := a.account(l, "usd2", "ASSET", "USD", true)
	eur := a.account(l, "eur", "ASSET", "EUR", true)
	other := a.account(a.ledger("other"), "x", "ASSET", "USD", true)

	post := func(entries ...any) resp {
		return a.do("POST", "/v1/ledgers/"+l+"/transactions", map[string]any{"entries": entries})
	}
	cases := []struct {
		name   string
		r      resp
		status int
		code   string
	}{
		{"unbalanced", post(entry(usd1, "DEBIT", "100", "USD"), entry(usd2, "CREDIT", "99", "USD")), 422, "unbalanced_transaction"},
		{"currency mismatch", post(entry(usd1, "DEBIT", "100", "EUR"), entry(eur, "CREDIT", "100", "EUR")), 422, "currency_mismatch"},
		{"unknown account", post(entry(usd1, "DEBIT", "100", "USD"), entry("0190a0a0-0000-7000-8000-000000000000", "CREDIT", "100", "USD")), 404, "not_found"},
		{"account in other ledger", post(entry(usd1, "DEBIT", "100", "USD"), entry(other, "CREDIT", "100", "USD")), 404, "not_found"},
		{"single entry", post(entry(usd1, "DEBIT", "100", "USD")), 400, "validation_error"},
		{"numeric amount", post(map[string]any{"account_id": usd1, "direction": "DEBIT", "amount": 100, "currency": "USD"}, entry(usd2, "CREDIT", "100", "USD")), 400, "validation_error"},
		{"fractional amount", post(entry(usd1, "DEBIT", "1.5", "USD"), entry(usd2, "CREDIT", "1.5", "USD")), 400, "validation_error"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.r.must(t, c.status)
			if c.r.code() != c.code {
				t.Fatalf("code = %s, want %s (%s)", c.r.code(), c.code, c.r.Raw)
			}
			if ct := c.r.Header.Get("Content-Type"); ct != "application/problem+json" {
				t.Fatalf("content-type = %s", ct)
			}
		})
	}

	// Unknown JSON fields are rejected rather than silently ignored.
	a.do("POST", "/v1/ledgers/"+l+"/transactions", map[string]any{"entries": []any{}, "amout": "1"}).must(t, 400)
}

func TestMultiCurrencyFXTransaction(t *testing.T) {
	a := newAPI(t)
	l := a.ledger("fx")
	userUSD := a.account(l, "user:usd", "LIABILITY", "USD", false)
	userEUR := a.account(l, "user:eur", "LIABILITY", "EUR", false)
	fxUSD := a.account(l, "fx:usd", "ASSET", "USD", true)
	fxEUR := a.account(l, "fx:eur", "ASSET", "EUR", true)
	bankUSD := a.account(l, "bank:usd", "ASSET", "USD", true)

	a.transfer(l, bankUSD, userUSD, "10000").must(t, 201)

	// Convert $50 to €46 at 0.92 through per-currency FX clearing accounts:
	// each currency leg balances independently.
	a.do("POST", "/v1/ledgers/"+l+"/transactions", map[string]any{
		"description": "convert USD->EUR",
		"metadata":    map[string]string{"rate": "0.92"},
		"entries": []any{
			entry(userUSD, "DEBIT", "5000", "USD"), entry(fxUSD, "CREDIT", "5000", "USD"),
			entry(fxEUR, "DEBIT", "4600", "EUR"), entry(userEUR, "CREDIT", "4600", "EUR"),
		},
	}).must(t, 201)

	if got := a.balances(userUSD)["posted"]; got != "5000" {
		t.Fatalf("user USD = %v", got)
	}
	if got := a.balances(userEUR)["posted"]; got != "4600" {
		t.Fatalf("user EUR = %v", got)
	}
	tb := a.do("GET", "/v1/ledgers/"+l+"/trial-balance", nil).must(t, 200)
	if tb.Body["balanced"] != true || len(tb.Body["currencies"].([]any)) != 2 {
		t.Fatalf("trial balance: %s", tb.Raw)
	}
}

func TestAccountRules(t *testing.T) {
	a := newAPI(t)
	l := a.ledger("rules")
	src := a.account(l, "src", "ASSET", "USD", true)
	dst := a.account(l, "dst", "LIABILITY", "USD", false)

	// Duplicate code in the same ledger.
	r := a.do("POST", "/v1/ledgers/"+l+"/accounts", map[string]any{"code": "src", "name": "x", "type": "ASSET", "currency": "USD"})
	if r.must(t, 409).code() != "duplicate_account_code" {
		t.Fatalf("code = %s", r.code())
	}
	// Unsupported currency.
	a.do("POST", "/v1/ledgers/"+l+"/accounts", map[string]any{"code": "x", "name": "x", "type": "ASSET", "currency": "XYZ"}).must(t, 400)

	// Frozen accounts reject postings, and unfreezing restores them.
	a.do("PATCH", "/v1/accounts/"+dst, map[string]any{"status": "FROZEN"}).must(t, 200)
	if r := a.transfer(l, src, dst, "100"); r.must(t, 422).code() != "account_frozen" {
		t.Fatalf("code = %s", r.code())
	}
	a.do("PATCH", "/v1/accounts/"+dst, map[string]any{"status": "ACTIVE"}).must(t, 200)
	a.transfer(l, src, dst, "100").must(t, 201)

	// Duplicate external_ref is rejected.
	body := map[string]any{"external_ref": "inv-1", "entries": []any{entry(src, "DEBIT", "1", "USD"), entry(dst, "CREDIT", "1", "USD")}}
	a.do("POST", "/v1/ledgers/"+l+"/transactions", body).must(t, 201)
	if r := a.do("POST", "/v1/ledgers/"+l+"/transactions", body); r.must(t, 409).code() != "duplicate_external_ref" {
		t.Fatalf("code = %s", r.code())
	}

	// Optimistic concurrency: a stale expected version is rejected.
	ver := a.do("GET", "/v1/accounts/"+dst, nil).must(t, 200).Body["version"]
	stale := map[string]any{
		"expected_versions": map[string]any{dst: 1},
		"entries":           []any{entry(src, "DEBIT", "1", "USD"), entry(dst, "CREDIT", "1", "USD")},
	}
	if r := a.do("POST", "/v1/ledgers/"+l+"/transactions", stale); r.must(t, 409).code() != "version_conflict" {
		t.Fatalf("code = %s", r.code())
	}
	stale["expected_versions"] = map[string]any{dst: ver}
	a.do("POST", "/v1/ledgers/"+l+"/transactions", stale).must(t, 201)
}

func TestStatementPaginationAndHistoricalBalance(t *testing.T) {
	a := newAPI(t)
	l := a.ledger("statements")
	bank := a.account(l, "bank", "ASSET", "USD", true)
	w := a.account(l, "wallet", "LIABILITY", "USD", false)

	var midpoint string
	for i := 1; i <= 7; i++ {
		r := a.transfer(l, bank, w, "100").must(t, 201)
		if i == 3 {
			// Use the database's own timestamp: the DB clock (in a container)
			// may be skewed relative to the test process.
			for _, e := range r.Body["entries"].([]any) {
				if e := e.(map[string]any); e["account_id"] == w {
					midpoint = e["created_at"].(string)
				}
			}
		}
	}

	// Walk the statement 3 entries at a time.
	var versions []float64
	cursor := ""
	for pages := 0; ; pages++ {
		if pages > 5 {
			t.Fatal("pagination did not terminate")
		}
		r := a.do("GET", "/v1/accounts/"+w+"/entries?limit=3&cursor="+cursor, nil).must(t, 200)
		for _, e := range r.Body["data"].([]any) {
			versions = append(versions, e.(map[string]any)["account_version"].(float64))
		}
		next, _ := r.Body["next_cursor"].(string)
		if next == "" {
			break
		}
		cursor = next
	}
	if fmt.Sprint(versions) != "[7 6 5 4 3 2 1]" {
		t.Fatalf("versions = %v", versions)
	}

	// Point-in-time balance after the third deposit.
	r := a.do("GET", "/v1/accounts/"+w+"/balance?as_of="+url.QueryEscape(midpoint), nil).must(t, 200)
	if r.Body["posted"] != "300" || r.Body["version"] != float64(3) {
		t.Fatalf("historical balance: %s", r.Raw)
	}
	r = a.do("GET", "/v1/accounts/"+w+"/balance", nil).must(t, 200)
	if r.Body["balances"].(map[string]any)["posted"] != "700" {
		t.Fatalf("live balance: %s", r.Raw)
	}
}

func TestListTransactionsPaginates(t *testing.T) {
	a := newAPI(t)
	l := a.ledger("list")
	x := a.account(l, "x", "ASSET", "USD", true)
	y := a.account(l, "y", "ASSET", "USD", true)
	for range 5 {
		a.transfer(l, x, y, "1").must(t, 201)
	}
	seen := map[string]bool{}
	cursor := ""
	for {
		r := a.do("GET", "/v1/ledgers/"+l+"/transactions?limit=2&status=POSTED&cursor="+cursor, nil).must(t, 200)
		for _, tx := range r.Body["data"].([]any) {
			seen[tx.(map[string]any)["id"].(string)] = true
		}
		next, _ := r.Body["next_cursor"].(string)
		if next == "" {
			break
		}
		cursor = next
	}
	if len(seen) != 5 {
		t.Fatalf("saw %d transactions, want 5", len(seen))
	}
}
