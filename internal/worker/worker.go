// Package worker runs the ledger's background jobs. Every job is safe to run
// on multiple worker instances at once: work is claimed with
// FOR UPDATE SKIP LOCKED (or leases), never by in-process coordination.
package worker

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/ledger"
	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/observability"
	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/store"
	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/webhook"
)

// Worker owns the background jobs.
type Worker struct {
	Pool              *pgxpool.Pool
	Ledger            *ledger.Service
	Dispatcher        *webhook.Dispatcher
	Log               *slog.Logger
	PollInterval      time.Duration
	ReconcileInterval time.Duration
	IdempotencyTTL    time.Duration
}

// Run starts all jobs and blocks until ctx is cancelled.
func (w *Worker) Run(ctx context.Context) {
	jobs := map[string]struct {
		every time.Duration
		fn    func(context.Context) (busy bool, err error)
	}{
		"expire-holds":     {w.PollInterval, w.expireHolds},
		"outbox-relay":     {w.PollInterval, w.relay},
		"webhook-delivery": {w.PollInterval, w.deliver},
		"reconcile":        {w.ReconcileInterval, w.Reconcile},
		"idempotency-gc":   {time.Hour, w.gcIdempotencyKeys},
	}
	var wg sync.WaitGroup
	for name, j := range jobs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w.loop(ctx, name, j.every, j.fn)
		}()
	}
	wg.Wait()
}

// loop runs fn every interval, immediately re-running while it reports more
// work so backlogs drain quickly.
func (w *Worker) loop(ctx context.Context, name string, every time.Duration, fn func(context.Context) (bool, error)) {
	log := w.Log.With("job", name)
	log.Info("job started", "interval", every.String())
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		busy, err := fn(ctx)
		if err != nil && ctx.Err() == nil {
			log.Error("job failed", "error", err)
		}
		if busy && err == nil {
			continue
		}
		select {
		case <-ctx.Done():
			log.Info("job stopped")
			return
		case <-t.C:
		}
	}
}

const batch = 100

func (w *Worker) expireHolds(ctx context.Context) (bool, error) {
	n, err := w.Ledger.ExpireHolds(ctx, batch)
	if n > 0 {
		w.Log.Info("expired pending transactions", "count", n)
	}
	return n == batch, err
}

func (w *Worker) relay(ctx context.Context) (bool, error) {
	n, err := w.Dispatcher.RelayOnce(ctx)
	return n == batch, err
}

func (w *Worker) deliver(ctx context.Context) (bool, error) {
	n, err := w.Dispatcher.DeliverOnce(ctx)
	return n == batch, err
}

// Reconcile recomputes all balances from the journal and reports drift.
// Drift should be impossible; if it ever appears it is logged loudly and
// exported as a metric for alerting.
func (w *Worker) Reconcile(ctx context.Context) (bool, error) {
	start := time.Now()
	drift, err := store.Reconcile(ctx, w.Pool, nil)
	if err != nil {
		return false, err
	}
	observability.ReconcileDrift.Set(float64(len(drift)))
	observability.ReconcileLastSuccess.SetToCurrentTime()
	for _, d := range drift {
		w.Log.Error("BALANCE DRIFT DETECTED", "account_id", d.AccountID, "ledger_id", d.LedgerID, "drift", d)
	}
	w.Log.Info("reconciliation complete", "drifted_accounts", len(drift), "duration", time.Since(start).String())
	return false, nil
}

func (w *Worker) gcIdempotencyKeys(ctx context.Context) (bool, error) {
	n, err := store.DeleteExpiredIdempotencyKeys(ctx, w.Pool, w.IdempotencyTTL, 10_000)
	if n > 0 {
		w.Log.Info("deleted expired idempotency keys", "count", n)
	}
	return n == 10_000, err
}
