# ADR 0006: Hot accounts: measured limit and future mitigation

**Status:** accepted (mitigation deferred)

## Context
With per-account row locks (ADR 0003), every transaction touching one popular
account (a platform fee or settlement account, for example) is serialized on
that row.

## Measurement
k6, 50 virtual users, everything (Postgres, API, worker, observability, k6)
running in Docker on one Apple M1 laptop:

| Scenario | Throughput | p50 | p99 |
|---|---|---|---|
| uniform: random transfers between 1,000 wallets | ~780 to 1,300 tx/s | ~26 to 42 ms | ~200 to 260 ms |
| hot: every transfer also credits one fee account | ~390 tx/s | ~75 ms | ~680 ms |

Contention costs about 2 to 3.4× in throughput. The
`ledger_account_lock_wait_seconds` histogram makes it visible in Grafana.

## Options for later
1. **Bucketed balances:** split a hot, credit-only account into N sub-rows
   chosen at random and sum them on read. This removes the single row but
   gives up a single gapless version sequence for that account.
2. **Deferred aggregation:** write hot-account legs to a queue table and apply
   them in periodic batches (one lock acquisition per batch).
3. Keep the hot account out of the synchronous path: settle fees in batches.

Option 1 is the most general. It is deferred until real traffic needs it,
because it complicates the strong per-account guarantees that the rest of the
design relies on.
