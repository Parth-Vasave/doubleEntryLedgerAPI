# ADR 0005: Transactional outbox for webhooks

**Status:** accepted

## Context
Subscribers need to learn about postings. Calling them, or a message broker,
from inside the request has the dual-write problem: the DB commit and the
notification can't be atomic.

## Decision
* Insert an `outbox_events` row in the same DB transaction as the ledger
  change. The event exists if and only if the change committed.
* The worker relays events in one SQL statement: it claims them with
  `FOR UPDATE SKIP LOCKED`, inserts one `webhook_deliveries` row per subscribed
  webhook, and marks the events published.
* Deliveries are leased (so other workers skip them), POSTed outside any DB
  transaction, and retried with exponential backoff and full jitter. They
  fail permanently after N attempts.
* Each delivery carries `Ledger-Signature: t=…,v1=HMAC-SHA256(secret, t.body)`.
  The timestamp lets receivers reject replays. `Ledger-Event-Id` is for
  deduplication, because delivery is at-least-once.
* Retry times are computed with the **database** clock, because worker and DB
  clocks can differ. A flaky test caught this.
* The HTTP client refuses private and loopback addresses at dial time (SSRF and
  DNS-rebinding protection) and does not follow redirects.

## Consequences
* There is no broker to operate. Moving to Kafka later only means swapping the
  relay's sink.
* Ordering between events is not guaranteed across concurrent deliveries.
