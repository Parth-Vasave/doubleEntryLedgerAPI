# ADR 0007: Enforce ledger invariants in the database as well

**Status:** accepted

## Context
Application checks are the first line of defense, but bugs, manual SQL, or a
future second writer could bypass them. A ledger that can be corrupted by one
UPDATE statement is not trustworthy.

## Decision
Duplicate every critical invariant as a database constraint:
* a deferred constraint trigger checks Σ debits = Σ credits per currency at COMMIT;
* composite foreign keys tie an entry's ledger and currency to its account;
* a `CHECK` on accounts forbids negative available balances unless allowed;
* triggers make entries append-only and freeze transactions after settlement;
* `UNIQUE` constraints guarantee gapless per-account versions and single reversal.

Constraint violations are mapped to the same API error codes as the app-level
checks (`store.MapError`).

## Consequences
* Tests in `internal/store/invariants_test.go` use raw SQL to prove each
  constraint fires even when the application is bypassed.
* The deferred trigger adds one small aggregate query per inserted entry at
  COMMIT. It has not been benchmarked in isolation; the load-test numbers in
  ADR 0006 include it.
