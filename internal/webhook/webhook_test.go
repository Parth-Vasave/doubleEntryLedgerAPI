package webhook_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/domain"
	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/ledger"
	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/money"
	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/testutil"
	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/webhook"
)

var testPool *pgxpool.Pool

func TestMain(m *testing.M) { testutil.RunMain(m, &testPool) }

func TestSignVerify(t *testing.T) {
	body := []byte(`{"id":"evt"}`)
	now := time.Unix(1_700_000_000, 0)
	sig := webhook.Sign("whsec_x", now, body)

	if err := webhook.Verify("whsec_x", sig, body, 5*time.Minute, now.Add(time.Minute)); err != nil {
		t.Fatalf("valid signature rejected: %v", err)
	}
	if err := webhook.Verify("whsec_y", sig, body, 5*time.Minute, now); err == nil {
		t.Fatal("wrong secret accepted")
	}
	if err := webhook.Verify("whsec_x", sig, []byte(`{"id":"evt2"}`), 5*time.Minute, now); err == nil {
		t.Fatal("tampered body accepted")
	}
	if err := webhook.Verify("whsec_x", sig, body, 5*time.Minute, now.Add(time.Hour)); err == nil {
		t.Fatal("stale signature accepted")
	}
}

func TestPrivateAddressesBlocked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	if resp, err := webhook.NewHTTPClient(time.Second, false).Get(srv.URL); err == nil {
		_ = resp.Body.Close()
		t.Fatal("delivery to loopback should be blocked")
	}
	resp, err := webhook.NewHTTPClient(time.Second, true).Get(srv.URL)
	if err != nil {
		t.Fatalf("allowPrivate client: %v", err)
	}
	_ = resp.Body.Close()
}

type fixture struct {
	svc      *ledger.Service
	ledgerID uuid.UUID
	src, dst uuid.UUID
	disp     *webhook.Dispatcher
}

func newFixture(t *testing.T, endpoint string, events ...string) (*fixture, string) {
	t.Helper()
	testutil.SkipIfShort(t)
	ctx := context.Background()
	f := &fixture{svc: ledger.NewService(testPool)}
	l, err := f.svc.CreateLedger(ctx, domain.CreateLedgerRequest{Name: "webhooks"})
	if err != nil {
		t.Fatal(err)
	}
	f.ledgerID = l.ID
	for i, code := range []string{"src", "dst"} {
		a, err := f.svc.CreateAccount(ctx, l.ID, domain.CreateAccountRequest{Code: code, Name: code, Type: domain.Asset, Currency: "USD", AllowNegative: true})
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			f.src = a.ID
		} else {
			f.dst = a.ID
		}
	}
	wh, err := f.svc.CreateWebhook(ctx, l.ID, ledger.CreateWebhookRequest{URL: endpoint, EventTypes: events})
	if err != nil {
		t.Fatal(err)
	}
	f.disp = &webhook.Dispatcher{
		Pool:        testPool,
		Client:      webhook.NewHTTPClient(2*time.Second, true),
		Log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		MaxAttempts: 3,
		Concurrency: 4,
		Backoff:     func(int) time.Duration { return 0 },
	}
	return f, wh.Secret
}

func (f *fixture) pay(t *testing.T, amount int64, pending bool) *domain.Transaction {
	t.Helper()
	tx, _, err := f.svc.CreateTransaction(context.Background(), f.ledgerID, domain.CreateTransactionRequest{
		Pending: pending,
		Entries: []domain.EntryInput{
			{AccountID: f.src, Direction: domain.Debit, Amount: money.Amount(amount), Currency: "USD"},
			{AccountID: f.dst, Direction: domain.Credit, Amount: money.Amount(amount), Currency: "USD"},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return tx
}

func (f *fixture) pump(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	if _, err := f.disp.RelayOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := f.disp.DeliverOnce(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestOutboxDeliversSignedEvents(t *testing.T) {
	var (
		mu       sync.Mutex
		received []webhook.Envelope
		secret   string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		if err := webhook.Verify(secret, r.Header.Get(webhook.SignatureHeader), body, time.Minute, time.Now()); err != nil {
			t.Errorf("bad signature: %v", err)
		}
		var env webhook.Envelope
		_ = json.Unmarshal(body, &env)
		received = append(received, env)
	}))
	defer srv.Close()

	// Subscribe to pending and voided only.
	f, s := newFixture(t, srv.URL, domain.EventTransactionPending, domain.EventTransactionVoided)
	mu.Lock()
	secret = s
	mu.Unlock()

	f.pay(t, 100, false) // transaction.posted: not subscribed
	hold := f.pay(t, 50, true)
	if _, _, err := f.svc.VoidPending(context.Background(), f.ledgerID, hold.ID, nil); err != nil {
		t.Fatal(err)
	}
	f.pump(t)

	mu.Lock()
	defer mu.Unlock()
	if len(received) != 2 {
		t.Fatalf("received %d events, want 2: %+v", len(received), received)
	}
	// Deliveries run concurrently, so arrival order is not guaranteed.
	byType := map[string]webhook.Envelope{}
	for _, e := range received {
		byType[e.Type] = e
	}
	voided, ok := byType[domain.EventTransactionVoided]
	if _, okPending := byType[domain.EventTransactionPending]; !ok || !okPending {
		t.Fatalf("event types received: %v", byType)
	}
	var data domain.Transaction
	if err := json.Unmarshal(voided.Data, &data); err != nil || data.ID != hold.ID || data.Status != domain.TxnVoided {
		t.Fatalf("payload: %s (%v)", voided.Data, err)
	}
}

func TestDeliveryRetriesThenFails(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	f, _ := newFixture(t, srv.URL)
	f.pay(t, 1, false)

	for range 5 {
		f.pump(t)
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("endpoint called %d times, want MaxAttempts=3", got)
	}
	var status string
	var attempts, lastStatus int
	if err := testPool.QueryRow(context.Background(), `
		SELECT d.status, d.attempts, d.last_status FROM webhook_deliveries d
		  JOIN webhooks w ON w.id = d.webhook_id WHERE w.ledger_id = $1`, f.ledgerID).
		Scan(&status, &attempts, &lastStatus); err != nil {
		t.Fatal(err)
	}
	if status != "FAILED" || attempts != 3 || lastStatus != 503 {
		t.Fatalf("delivery: status=%s attempts=%d last_status=%d", status, attempts, lastStatus)
	}
}

func TestRolledBackTransactionEmitsNoEvent(t *testing.T) {
	f, _ := newFixture(t, "https://example.com/hook")
	ctx := context.Background()
	w, err := f.svc.CreateAccount(ctx, f.ledgerID, domain.CreateAccountRequest{Code: "w", Name: "w", Type: domain.Liability, Currency: "USD"})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = f.svc.CreateTransaction(ctx, f.ledgerID, domain.CreateTransactionRequest{Entries: []domain.EntryInput{
		{AccountID: w.ID, Direction: domain.Debit, Amount: 10, Currency: "USD"},
		{AccountID: f.dst, Direction: domain.Credit, Amount: 10, Currency: "USD"},
	}}, nil)
	if !domain.IsCode(err, domain.CodeInsufficientFunds) {
		t.Fatalf("want insufficient_funds, got %v", err)
	}
	var n int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE ledger_id = $1`, f.ledgerID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("%d events recorded for a rolled-back transaction", n)
	}
}
