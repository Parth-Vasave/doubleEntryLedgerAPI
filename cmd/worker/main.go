// Command worker runs the ledger's background jobs: hold expiry, outbox
// relay, webhook delivery, reconciliation and idempotency-key cleanup.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/config"
	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/ledger"
	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/observability"
	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/store"
	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/webhook"
	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/worker"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := observability.NewLogger(cfg.LogLevel).With("service", "worker")
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	shutdownTracing, err := observability.InitTracing(ctx, cfg.OTLPEndpoint, cfg.ServiceName+"-worker")
	if err != nil {
		return err
	}
	defer func() { _ = shutdownTracing(context.Background()) }()

	pool, err := store.Connect(ctx, cfg.DatabaseURL, cfg.DBMaxConns, store.WithTracing())
	if err != nil {
		return err
	}
	defer pool.Close()

	waitCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	log.Info("waiting for database schema")
	if err := store.WaitForSchema(waitCtx, pool, time.Second); err != nil {
		return err
	}

	// Expose metrics and a liveness probe.
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.Handler())
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	metricsSrv := &http.Server{Addr: cfg.WorkerMetricsAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := metricsSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("metrics server", "error", err)
		}
	}()

	w := &worker.Worker{
		Pool:   pool,
		Ledger: ledger.NewService(pool),
		Dispatcher: &webhook.Dispatcher{
			Pool:        pool,
			Client:      webhook.NewHTTPClient(cfg.WebhookTimeout, cfg.WebhookAllowPrivate),
			Log:         log,
			MaxAttempts: cfg.WebhookMaxAttempts,
			BatchSize:   100,
			Concurrency: 16,
		},
		Log:               log,
		PollInterval:      cfg.WorkerPollInterval,
		ReconcileInterval: cfg.ReconcileInterval,
		IdempotencyTTL:    cfg.IdempotencyKeyTTL,
	}
	log.Info("worker started")
	w.Run(ctx)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	return metricsSrv.Shutdown(shutdownCtx)
}
