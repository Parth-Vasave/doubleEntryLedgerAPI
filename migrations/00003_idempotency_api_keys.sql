-- +goose Up

-- Idempotency keys are written in the same database transaction as the
-- operation they protect. A concurrent duplicate blocks on the primary key
-- until the first request commits (then replays its stored response) or rolls
-- back (then proceeds itself), giving exactly-once execution.
CREATE TABLE idempotency_keys (
    ledger_id       UUID NOT NULL REFERENCES ledgers (id),
    key             TEXT NOT NULL CHECK (length(key) BETWEEN 1 AND 255),
    request_hash    BYTEA NOT NULL,
    response_status SMALLINT,
    response_body   JSONB,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (ledger_id, key)
);

CREATE INDEX idempotency_keys_created_at_idx ON idempotency_keys (created_at);

-- API keys are stored only as SHA-256 hashes. A NULL ledger_id grants access
-- to every ledger.
CREATE TABLE api_keys (
    id           UUID PRIMARY KEY,
    name         TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    prefix       TEXT NOT NULL,
    key_hash     BYTEA NOT NULL UNIQUE,
    ledger_id    UUID REFERENCES ledgers (id),
    scopes       TEXT[] NOT NULL CHECK (scopes <@ ARRAY['read', 'write']::text[] AND cardinality(scopes) > 0),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at   TIMESTAMPTZ
);

-- +goose Down
DROP TABLE api_keys;
DROP TABLE idempotency_keys;
