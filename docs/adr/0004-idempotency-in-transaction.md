# ADR 0004: Idempotency keys recorded in the same database transaction

**Status:** accepted

## Context
Clients retry on timeouts. If the server records "request done" separately from
the money movement, a crash between the two produces a double post (money moved,
key not recorded) or a lost request (key recorded, money not moved).

## Decision
The first statement of the operation's DB transaction is
`INSERT INTO idempotency_keys … ON CONFLICT DO NOTHING`. The response is written
to the same row just before COMMIT.

* **New key:** this transaction owns it. If the transaction commits, the key
  and the effect commit together. If it rolls back, so does the key.
* **Concurrent duplicate:** Postgres blocks the second INSERT on the primary
  key until the first transaction ends. The duplicate then either replays the
  stored response or, if the first rolled back, executes itself.
* **Same key, different body** (SHA-256 of method, path and body):
  `422 idempotency_key_reused`.
* **Failures are not stored**, so a request rejected for insufficient funds can
  be retried after the account is funded.

## Consequences
* Exactly-once execution without a separate lock service or an "in progress"
  state that needs crash recovery.
* A test fires 32 identical concurrent requests and asserts exactly one execution.
* Keys are ledger-scoped and garbage-collected after 24h.
