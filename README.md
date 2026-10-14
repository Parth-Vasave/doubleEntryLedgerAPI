# Double-Entry Ledger API

A double-entry accounting ledger service in **Go** and **PostgreSQL**, built
along the lines of the internal ledgers behind payment platforms and wallets.
Money is never created or destroyed, history is never edited, and concurrent
writers can neither deadlock nor overdraw an account. Each of these is enforced
by the application and again by the database.

```
POST /v1/ledgers/{id}/transactions          Idempotency-Key: 6f1c…
{ "entries": [
    { "account_id": "…alice", "direction": "DEBIT",  "amount": "2600", "currency": "USD" },
    { "account_id": "…bob",   "direction": "CREDIT", "amount": "2500", "currency": "USD" },
    { "account_id": "…fees",  "direction": "CREDIT", "amount": "100",  "currency": "USD" } ] }
```

## Features

**Accounting**
- Five account types with normal balances (ASSET, LIABILITY, EQUITY, REVENUE, EXPENSE); posted, pending and available balances
- Balanced multi-leg journal entries, checked per currency; multi-currency and FX through clearing accounts
- **Two-phase transfers**: holds, full or partial capture, void, and automatic expiry
- **Reversals** instead of edits; append-only history with at most one reversal per transaction
- **Atomic batches** of up to 100 transactions
- Account statements, **point-in-time balances**, trial balance

**Correctness and concurrency**
- Deadlock-free posting: `SELECT … FOR UPDATE` in a global account-id order
- **Exactly-once idempotency keys**, recorded in the same DB transaction as the money movement
- **Invariants enforced by PostgreSQL**: a deferred balance trigger, append-only triggers, composite FKs and an overdraft CHECK
- Overflow-checked integer minor units; amounts are JSON strings
- Optimistic concurrency for clients via `expected_versions`

**Production engineering**
- Transactional **outbox** and **HMAC-signed webhooks** with retries, backoff and SSRF protection
- Worker for hold expiry, webhook delivery, **continuous reconciliation** and key cleanup, safe to run as multiple replicas (SKIP LOCKED and leases)
- API keys (SHA-256 hashed, ledger-scoped, read/write scopes) and per-key rate limiting
- Prometheus metrics with alert rules, a provisioned Grafana dashboard, OpenTelemetry traces in Jaeger
- OpenAPI 3.1 spec with Swagger UI at `/docs`; RFC 7807 errors with stable codes

## Quick start

Requires Docker. Go 1.27+ is needed only for running tests outside Docker.

```bash
make up        # postgres, api, worker, webhook sink, prometheus, grafana, jaeger
make demo      # scripted tour: deposits, P2P with fee, idempotent retry, overdraft,
               # hold + partial capture, refund, FX, statement, trial balance, webhooks
make loadtest MODE=uniform   # or MODE=hot
make down
```

| Service | URL |
|---|---|
| API and Swagger UI | http://localhost:8080/docs |
| Grafana dashboard ("Ledger") | http://localhost:3001 |
| Prometheus | http://localhost:9090 |
| Jaeger traces | http://localhost:16686 |

The admin key defaults to `dev-admin-key-change-me` (`ADMIN_API_KEY`).
Postgres is exposed on `localhost:55432`.

## Design highlights

The full write-up is in [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) and the
[ADRs](docs/adr).

**Write path.** Validate (Σ debits = Σ credits per currency) → claim the
idempotency key → lock accounts in id order → apply entries in memory,
assigning each the next gapless `account_version` and its `balance_after` →
check frozen and overdraft rules on the final state → one pipelined batch
(transaction, entries, counters, outbox event, idempotency response) →
COMMIT, when the deferred balance trigger re-checks the books.

**Why pessimistic locks?** Under contention, optimistic and SERIALIZABLE
strategies turn into retry storms on popular accounts, while locks queue
writers fairly. A global lock order makes deadlocks impossible, and holding the
lock is what gives every entry an exact running balance
([ADR 0003](docs/adr/0003-pessimistic-ordered-locking.md)).

**Exactly-once.** The idempotency key row is inserted first in the business
transaction. A concurrent duplicate blocks on the primary key and then replays
the committed response. No crash window exists between "money moved" and
"key recorded" ([ADR 0004](docs/adr/0004-idempotency-in-transaction.md)).

**Defense in depth.** Raw-SQL tests show the database itself rejects an
unbalanced transaction, a mismatched currency, an overdraft, and any UPDATE or
DELETE of history ([ADR 0007](docs/adr/0007-database-enforced-invariants.md)).

## Testing

```bash
make test         # unit + integration (real Postgres via testcontainers) with -race
make test-short   # unit tests only, no Docker
```

- **Model-based property tests** ([rapid](https://github.com/flyingmutant/rapid)):
  random sequences of transfers, holds, captures, voids and reversals run
  against the real ledger and an independent model. After every step they
  assert model agreement, zero drift between counters and the journal, gapless
  versions, debits = credits, and no overdrafts. A deliberately injected
  counter bug is caught and shrunk to a minimal counterexample.
- **Concurrency**: 64 goroutines making random three-account transfers for 3s,
  checking for no deadlocks and exact conservation of money. 32 concurrent
  identical idempotent requests must execute exactly once.
- **Database invariants**: raw SQL attempts to corrupt the ledger must fail.
- **Webhooks**: signature verification, retry then permanent failure,
  subscription filtering, no events from rolled-back transactions, SSRF blocking.
- **Contract**: every route is documented in the OpenAPI spec and vice versa.

## Benchmarks

k6 with 50 VUs, on an Apple M1 laptop with Postgres, API, worker,
observability stack and k6 all in Docker on the same machine, so treat these as
relative numbers:

| Scenario | Throughput | p50 | p99 |
|---|---|---|---|
| Uniform transfers across 1,000 wallets | 780 to 1,300 tx/s | 26 to 42 ms | 200 to 260 ms |
| Hot account (every transfer credits one fee account) | ~390 tx/s | 75 ms | 680 ms |

Every run ended with a balanced trial balance and zero reconciliation drift.
The hot-account cost and mitigation options are discussed in
[ADR 0006](docs/adr/0006-hot-accounts.md).

## Project layout

```
cmd/api             HTTP server
cmd/worker          background jobs
cmd/webhook-sink    example receiver that verifies webhook signatures
internal/domain     accounting rules and types (no I/O)
internal/ledger     use cases, transaction boundaries, idempotency, events
internal/store      SQL, locking, reconciliation
internal/httpapi    routing, auth, rate limiting, errors
internal/auth       API keys
internal/webhook    signing and delivery
internal/worker     job loops
internal/money      integer money type
migrations/         goose migrations (embedded)
api/openapi.yaml    API contract
deploy/             compose, Prometheus rules, Grafana dashboard
loadtest/k6/        load tests
docs/               architecture and ADRs
```

## Configuration

| Variable | Default | |
|---|---|---|
| `DATABASE_URL` | (required) | Postgres connection string |
| `ADMIN_API_KEY` | (required) | bootstrap admin key |
| `HTTP_ADDR` | `:8080` | |
| `DB_MAX_CONNS` | `20` | pool size |
| `RATE_LIMIT_RPS` / `RATE_LIMIT_BURST` | `500` / `1000` | per API key; 0 disables |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | (unset) | e.g. `jaeger:4318`; tracing is off when unset |
| `RECONCILE_INTERVAL` | `1m` | worker |
| `IDEMPOTENCY_KEY_TTL` | `24h` | worker |
| `WEBHOOK_MAX_ATTEMPTS` | `10` | worker |
| `WEBHOOK_ALLOW_PRIVATE_NETWORKS` | `false` | local development only |
