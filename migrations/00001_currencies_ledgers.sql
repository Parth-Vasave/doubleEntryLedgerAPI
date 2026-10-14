-- +goose Up
CREATE TABLE currencies (
    code     TEXT PRIMARY KEY CHECK (code ~ '^[A-Z][A-Z0-9]{2,9}$'),
    exponent SMALLINT NOT NULL CHECK (exponent BETWEEN 0 AND 18)
);

INSERT INTO currencies (code, exponent) VALUES
    ('USD', 2), ('EUR', 2), ('GBP', 2), ('INR', 2), ('JPY', 0), ('BTC', 8);

CREATE TABLE ledgers (
    id         UUID PRIMARY KEY,
    name       TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    metadata   JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE ledgers;
DROP TABLE currencies;
