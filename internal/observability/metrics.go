package observability

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Prometheus metrics. Route labels use the mux pattern (e.g.
// "POST /v1/ledgers/{ledger_id}/transactions"), never the raw path, to keep
// cardinality bounded.
var (
	HTTPRequests = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ledger_http_requests_total",
		Help: "HTTP requests by route and status code.",
	}, []string{"route", "code"})

	HTTPDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "ledger_http_request_duration_seconds",
		Help:    "HTTP request latency by route.",
		Buckets: []float64{.001, .0025, .005, .01, .025, .05, .1, .25, .5, 1, 2.5},
	}, []string{"route"})

	Transactions = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ledger_transactions_total",
		Help: "Ledger operations committed, by kind (posted, pending, captured, voided, reversed).",
	}, []string{"kind"})

	LockWait = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "ledger_account_lock_wait_seconds",
		Help:    "Time spent acquiring account row locks; rises with hot-account contention.",
		Buckets: []float64{.0001, .0005, .001, .0025, .005, .01, .025, .05, .1, .25, .5, 1},
	})

	TxRetries = promauto.NewCounter(prometheus.CounterOpts{
		Name: "ledger_db_tx_retries_total",
		Help: "Database transactions retried after serialization failures or deadlocks.",
	})

	IdempotentReplays = promauto.NewCounter(prometheus.CounterOpts{
		Name: "ledger_idempotent_replays_total",
		Help: "Requests answered from a stored idempotent response.",
	})

	RateLimited = promauto.NewCounter(prometheus.CounterOpts{
		Name: "ledger_rate_limited_total",
		Help: "Requests rejected by the per-key rate limiter.",
	})

	ReconcileDrift = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "ledger_reconciliation_drift_accounts",
		Help: "Accounts whose cached balances disagree with the journal at the last reconciliation. Must be 0.",
	})

	ReconcileLastSuccess = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "ledger_reconciliation_last_success_timestamp_seconds",
		Help: "Unix time of the last completed reconciliation run.",
	})

	HoldsExpired = promauto.NewCounter(prometheus.CounterOpts{
		Name: "ledger_holds_expired_total",
		Help: "Pending transactions voided because they expired.",
	})

	OutboxRelayed = promauto.NewCounter(prometheus.CounterOpts{
		Name: "ledger_outbox_events_relayed_total",
		Help: "Outbox events fanned out to webhook deliveries.",
	})

	WebhookDeliveries = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ledger_webhook_deliveries_total",
		Help: "Webhook delivery attempts by result (delivered, retry, failed).",
	}, []string{"result"})
)
