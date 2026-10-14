# ADR 0002: Append-only journal plus cached balance counters

**Status:** accepted

## Context
Balances can be computed on read (Σ entries), which is always correct but costs
O(entries), or cached, which is O(1) but can drift. Statements and historical
("as of") balances also need to be fast.

## Decision
* `entries` is the append-only source of truth. Database triggers forbid
  UPDATE, DELETE and TRUNCATE.
* Each account caches `posted_debits`, `posted_credits`, `pending_debits`,
  `pending_credits` and `version`, updated **in the same DB transaction** as
  the entries.
* Each entry stores `account_version` and `balance_after`. That makes a
  point-in-time balance one index probe (the last entry at or before T) and a
  statement a range scan on `(account_id, account_version)`.
* A reconciliation job recomputes everything from the journal and alerts on
  any drift.

## Alternatives considered
* **Periodic balance snapshots** (the original plan): unnecessary, because
  `balance_after` already gives O(log n) historical lookups without a second
  structure to keep in sync.
* **Compute on read**: too slow for hot accounts with millions of entries.

## Consequences
* Writes serialize per account (see ADR 0003), which is what makes
  `balance_after` and gapless versions possible.
* Historical queries cover the posted balance only, because holds are transient.
