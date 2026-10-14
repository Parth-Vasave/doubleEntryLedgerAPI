package ledger_test

import (
	"context"
	"fmt"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/domain"
	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/ledger"
	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/money"
	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/testutil"
)

// TestConcurrentTransfersConserveMoney hammers a small set of wallets from
// many goroutines. Overlapping account sets maximise lock contention; the
// test asserts there are no deadlocks or unexpected errors, no overdrafts,
// and that the total in the closed system is exactly conserved.
func TestConcurrentTransfersConserveMoney(t *testing.T) {
	testutil.SkipIfShort(t)
	ctx := context.Background()
	svc := ledger.NewService(testPool)
	l := newLedger(t, svc)

	const (
		wallets    = 10
		workers    = 64
		perWallet  = 10_000
		duration   = 3 * time.Second
		maxPayment = 3_000
	)
	bank := newAccount(t, svc, l, "bank", domain.Asset, "USD", true)
	ws := make([]*domain.Account, wallets)
	for i := range ws {
		ws[i] = newAccount(t, svc, l, fmt.Sprintf("wallet:%d", i), domain.Liability, "USD", false)
		if _, _, err := svc.CreateTransaction(ctx, l, transferReq(bank.ID, ws[i].ID, perWallet, "USD"), nil); err != nil {
			t.Fatal(err)
		}
	}

	var ok, insufficient atomic.Int64
	deadline := time.Now().Add(duration)
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := rand.New(rand.NewPCG(uint64(w), 42))
			for time.Now().Before(deadline) {
				// Three-legged transfers touch 3 accounts in random order,
				// which would deadlock without ordered locking.
				i, j, k := r.IntN(wallets), r.IntN(wallets), r.IntN(wallets)
				amt := money.Amount(r.IntN(maxPayment) + 2)
				req := domain.CreateTransactionRequest{Entries: []domain.EntryInput{
					{AccountID: ws[i].ID, Direction: domain.Debit, Amount: amt, Currency: "USD"},
					{AccountID: ws[j].ID, Direction: domain.Credit, Amount: amt / 2, Currency: "USD"},
					{AccountID: ws[k].ID, Direction: domain.Credit, Amount: amt - amt/2, Currency: "USD"},
				}}
				_, _, err := svc.CreateTransaction(ctx, l, req, nil)
				switch {
				case err == nil:
					ok.Add(1)
				case domain.IsCode(err, domain.CodeInsufficientFunds):
					insufficient.Add(1)
				default:
					errs <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("unexpected error under contention: %v", err)
	}
	t.Logf("%d transfers committed, %d rejected for insufficient funds", ok.Load(), insufficient.Load())
	if ok.Load() == 0 {
		t.Fatal("no transfers committed")
	}

	var total money.Amount
	for _, w := range ws {
		a, err := svc.GetAccount(ctx, w.ID)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := a.Balances()
		total += b.Posted
	}
	if total != wallets*perWallet {
		t.Fatalf("money not conserved: total %d, want %d", total, wallets*perWallet)
	}
	assertLedgerInvariants(t, l)
}

// TestConcurrentIdempotentRequests fires the same idempotent request from
// many goroutines at once: exactly one must execute, the rest must replay
// its result.
func TestConcurrentIdempotentRequests(t *testing.T) {
	testutil.SkipIfShort(t)
	ctx := context.Background()
	svc := ledger.NewService(testPool)
	l := newLedger(t, svc)
	src := newAccount(t, svc, l, "src", domain.Asset, "USD", true)
	dst := newAccount(t, svc, l, "dst", domain.Liability, "USD", false)

	idem := &ledger.Idempotency{Key: "pay-once", Hash: []byte("same-request")}
	const n = 32
	var wg sync.WaitGroup
	ids := make([]string, n)
	replays := make([]bool, n)
	errs := make([]error, n)
	start := make(chan struct{})
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			tx, replayed, err := svc.CreateTransaction(ctx, l, transferReq(src.ID, dst.ID, 500, "USD"), idem)
			if err == nil {
				ids[i], replays[i] = tx.ID.String(), replayed
			}
			errs[i] = err
		}()
	}
	close(start)
	wg.Wait()

	executed := 0
	for i := range n {
		if errs[i] != nil {
			t.Fatalf("request %d failed: %v", i, errs[i])
		}
		if ids[i] != ids[0] {
			t.Fatalf("request %d got transaction %s, want %s", i, ids[i], ids[0])
		}
		if !replays[i] {
			executed++
		}
	}
	if executed != 1 {
		t.Fatalf("%d requests executed, want exactly 1", executed)
	}
	a, _ := svc.GetAccount(ctx, dst.ID)
	if b, _ := a.Balances(); b.Posted != 500 {
		t.Fatalf("destination balance %d, want 500 (money moved more than once)", b.Posted)
	}

	// Reusing the key for a different request is an error.
	other := &ledger.Idempotency{Key: "pay-once", Hash: []byte("different-request")}
	if _, _, err := svc.CreateTransaction(ctx, l, transferReq(src.ID, dst.ID, 1, "USD"), other); !domain.IsCode(err, domain.CodeIdempotencyReused) {
		t.Fatalf("want idempotency_key_reused, got %v", err)
	}
	assertLedgerInvariants(t, l)
}

// TestFailedIdempotentRequestIsRetryable checks failures are not cached.
func TestFailedIdempotentRequestIsRetryable(t *testing.T) {
	testutil.SkipIfShort(t)
	ctx := context.Background()
	svc := ledger.NewService(testPool)
	l := newLedger(t, svc)
	bank := newAccount(t, svc, l, "bank", domain.Asset, "USD", true)
	w := newAccount(t, svc, l, "w", domain.Liability, "USD", false)
	out := newAccount(t, svc, l, "out", domain.Asset, "USD", true)

	idem := &ledger.Idempotency{Key: "withdraw-1", Hash: []byte("h")}
	if _, _, err := svc.CreateTransaction(ctx, l, transferReq(w.ID, out.ID, 100, "USD"), idem); !domain.IsCode(err, domain.CodeInsufficientFunds) {
		t.Fatalf("want insufficient_funds, got %v", err)
	}
	if _, _, err := svc.CreateTransaction(ctx, l, transferReq(bank.ID, w.ID, 100, "USD"), nil); err != nil {
		t.Fatal(err)
	}
	if _, replayed, err := svc.CreateTransaction(ctx, l, transferReq(w.ID, out.ID, 100, "USD"), idem); err != nil || replayed {
		t.Fatalf("retry after funding: replayed=%v err=%v", replayed, err)
	}
}
