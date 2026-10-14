# ADR 0003: Pessimistic row locks acquired in a global order

**Status:** accepted

## Context
Concurrent transfers touching the same accounts must not lose updates or
overdraw an account. The options were:
1. `SERIALIZABLE` isolation with retry on serialization failure.
2. Optimistic concurrency (a version column, compare-and-swap, retry).
3. `SELECT … FOR UPDATE` row locks.

## Decision
Use `READ COMMITTED` plus `SELECT … FOR UPDATE` on every account a transaction
touches, **always acquired in ascending account-id order** (`ORDER BY id` runs
before the lock step in the plan). Overdraft checks happen after the locks, on
the final in-memory state.

## Why
* Under contention, optimistic and SERIALIZABLE approaches turn into retry
  storms on hot accounts. Locks queue writers fairly instead.
* A global lock order removes the cycle needed for a deadlock. The
  64-goroutine test of random three-account transfers commits thousands of
  transfers without a single deadlock.
* Holding the lock is what lets each entry get the next gapless
  `account_version` and an exact `balance_after`.

Post/void locks the transaction row first, then its accounts (in order). The
create path never locks transaction rows, so no cycle exists between the two
kinds of lock.

## Consequences
* Throughput on one account is bounded by single-row lock hand-off; see ADR 0006.
* Clients can still opt into optimistic checks with `expected_versions`.
* Serialization and deadlock errors are retried anyway, as defense in depth.
