# Double-Entry Ledger API

A double-entry accounting ledger service written in Go and backed by PostgreSQL.

> Work in progress. See the milestones below.

## Quick start

```bash
make up            # postgres + api via docker compose
curl localhost:8080/healthz
make test          # integration tests spin up Postgres with testcontainers (Docker required)
```

## Milestones

- [x] M1 Foundation: project layout, config, structured logging, migrations, health checks, CI
- [x] M2 Core ledger: accounts, balanced journal entries, deterministic locking, DB invariants
- [x] M3 Safety: idempotency keys, retries, property-based and concurrency tests, API keys
- [ ] M4 Advanced accounting: holds, reversals, batch, multi-currency, point-in-time balances
- [ ] M5 Async and ops: worker, outbox and webhooks, reconciliation, metrics and tracing
- [ ] M6 Polish: load tests, OpenAPI, ADRs
