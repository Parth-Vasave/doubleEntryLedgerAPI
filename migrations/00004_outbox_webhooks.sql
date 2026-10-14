-- +goose Up

-- Transactional outbox: events are inserted in the same database transaction
-- as the ledger change they describe, so an event exists if and only if the
-- change committed. A relay later fans them out to webhook deliveries.
CREATE TABLE outbox_events (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    event_id     UUID NOT NULL UNIQUE,
    ledger_id    UUID NOT NULL REFERENCES ledgers (id),
    type         TEXT NOT NULL,
    aggregate_id UUID NOT NULL,
    payload      JSONB NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    published_at TIMESTAMPTZ
);

CREATE INDEX outbox_events_unpublished_idx ON outbox_events (id) WHERE published_at IS NULL;

CREATE TABLE webhooks (
    id          UUID PRIMARY KEY,
    ledger_id   UUID NOT NULL REFERENCES ledgers (id),
    url         TEXT NOT NULL CHECK (url ~ '^https?://'),
    secret      TEXT NOT NULL,
    event_types TEXT[] NOT NULL CHECK (cardinality(event_types) > 0),
    active      BOOLEAN NOT NULL DEFAULT true,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX webhooks_ledger_idx ON webhooks (ledger_id) WHERE active;

CREATE TABLE webhook_deliveries (
    id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    webhook_id      UUID NOT NULL REFERENCES webhooks (id),
    event_id        UUID NOT NULL REFERENCES outbox_events (event_id),
    status          TEXT NOT NULL DEFAULT 'PENDING' CHECK (status IN ('PENDING', 'DELIVERED', 'FAILED')),
    attempts        INT NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    leased_until    TIMESTAMPTZ,
    last_status     INT,
    last_error      TEXT,
    delivered_at    TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (webhook_id, event_id)
);

CREATE INDEX webhook_deliveries_due_idx ON webhook_deliveries (next_attempt_at) WHERE status = 'PENDING';

-- +goose Down
DROP TABLE webhook_deliveries;
DROP TABLE webhooks;
DROP TABLE outbox_events;
