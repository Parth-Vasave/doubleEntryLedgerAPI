-- +goose Up

-- Accounts hold running balance counters that are updated in the same database
-- transaction as the entries that change them. Counters are cumulative and
-- non-negative; signed balances are derived from them using normal_balance.
CREATE TABLE accounts (
    id              UUID PRIMARY KEY,
    ledger_id       UUID NOT NULL REFERENCES ledgers (id),
    code            TEXT NOT NULL CHECK (length(code) BETWEEN 1 AND 100),
    name            TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    type            TEXT NOT NULL CHECK (type IN ('ASSET', 'LIABILITY', 'EQUITY', 'REVENUE', 'EXPENSE')),
    normal_balance  TEXT NOT NULL GENERATED ALWAYS AS (
                        CASE WHEN type IN ('ASSET', 'EXPENSE') THEN 'DEBIT' ELSE 'CREDIT' END
                    ) STORED,
    currency        TEXT NOT NULL REFERENCES currencies (code),
    allow_negative  BOOLEAN NOT NULL DEFAULT false,
    status          TEXT NOT NULL DEFAULT 'ACTIVE' CHECK (status IN ('ACTIVE', 'FROZEN')),
    posted_debits   BIGINT NOT NULL DEFAULT 0 CHECK (posted_debits >= 0),
    posted_credits  BIGINT NOT NULL DEFAULT 0 CHECK (posted_credits >= 0),
    pending_debits  BIGINT NOT NULL DEFAULT 0 CHECK (pending_debits >= 0),
    pending_credits BIGINT NOT NULL DEFAULT 0 CHECK (pending_credits >= 0),
    version         BIGINT NOT NULL DEFAULT 0 CHECK (version >= 0),
    metadata        JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT accounts_ledger_code_key UNIQUE (ledger_id, code),
    -- Target for the composite foreign key from entries: guarantees an entry's
    -- ledger and currency always match its account's.
    CONSTRAINT accounts_id_ledger_currency_key UNIQUE (id, ledger_id, currency),
    -- Last line of defence for invariant 4: an account that may not go negative
    -- never has a negative *available* balance (posted minus outgoing holds).
    CONSTRAINT accounts_non_negative_available CHECK (
        allow_negative OR (
            CASE WHEN type IN ('ASSET', 'EXPENSE')
                 THEN posted_debits - posted_credits - pending_credits
                 ELSE posted_credits - posted_debits - pending_debits
            END
        ) >= 0
    )
);

CREATE INDEX accounts_ledger_id_idx ON accounts (ledger_id, id);

CREATE TABLE transactions (
    id           UUID PRIMARY KEY,
    ledger_id    UUID NOT NULL REFERENCES ledgers (id),
    status       TEXT NOT NULL CHECK (status IN ('PENDING', 'POSTED', 'VOIDED')),
    description  TEXT NOT NULL DEFAULT '' CHECK (length(description) <= 1000),
    external_ref TEXT CHECK (length(external_ref) BETWEEN 1 AND 200),
    effective_at TIMESTAMPTZ NOT NULL,
    expires_at   TIMESTAMPTZ,
    posted_at    TIMESTAMPTZ,
    voided_at    TIMESTAMPTZ,
    reverses_id  UUID REFERENCES transactions (id),
    metadata     JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT transactions_id_ledger_key UNIQUE (id, ledger_id),
    CONSTRAINT transactions_ledger_external_ref_key UNIQUE (ledger_id, external_ref),
    -- A transaction can be reversed at most once.
    CONSTRAINT transactions_reverses_id_key UNIQUE (reverses_id),
    CONSTRAINT transactions_posted_at_matches_status CHECK ((status = 'POSTED') = (posted_at IS NOT NULL)),
    CONSTRAINT transactions_voided_at_matches_status CHECK ((status = 'VOIDED') = (voided_at IS NOT NULL))
);

CREATE INDEX transactions_ledger_id_idx ON transactions (ledger_id, id DESC);
CREATE INDEX transactions_pending_expiry_idx ON transactions (expires_at)
    WHERE status = 'PENDING' AND expires_at IS NOT NULL;

-- Entries are the append-only journal. Each entry carries the account version
-- it produced and the account's posted balance after it, which makes
-- statements and point-in-time balance lookups a single index probe.
CREATE TABLE entries (
    id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    ledger_id       UUID NOT NULL,
    transaction_id  UUID NOT NULL,
    account_id      UUID NOT NULL,
    direction       TEXT NOT NULL CHECK (direction IN ('DEBIT', 'CREDIT')),
    amount          BIGINT NOT NULL CHECK (amount > 0),
    currency        TEXT NOT NULL,
    pending         BOOLEAN NOT NULL,
    account_version BIGINT NOT NULL CHECK (account_version > 0),
    balance_after   BIGINT NOT NULL,
    -- clock_timestamp() (not now()) so that timestamps are monotonic per account:
    -- entries are inserted while the account row lock is held.
    created_at      TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),

    CONSTRAINT entries_transaction_fk FOREIGN KEY (transaction_id, ledger_id)
        REFERENCES transactions (id, ledger_id),
    CONSTRAINT entries_account_fk FOREIGN KEY (account_id, ledger_id, currency)
        REFERENCES accounts (id, ledger_id, currency),
    CONSTRAINT entries_account_version_key UNIQUE (account_id, account_version)
);

CREATE INDEX entries_transaction_id_idx ON entries (transaction_id);
CREATE INDEX entries_account_created_idx ON entries (account_id, created_at);

-- Append-only enforcement --------------------------------------------------

-- +goose StatementBegin
CREATE FUNCTION ledger_forbid_mutation() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION '% on % is not allowed: the ledger is append-only', TG_OP, TG_TABLE_NAME
        USING ERRCODE = 'restrict_violation';
END
$$;
-- +goose StatementEnd

CREATE TRIGGER entries_append_only
    BEFORE UPDATE OR DELETE ON entries
    FOR EACH ROW EXECUTE FUNCTION ledger_forbid_mutation();
CREATE TRIGGER entries_no_truncate
    BEFORE TRUNCATE ON entries
    FOR EACH STATEMENT EXECUTE FUNCTION ledger_forbid_mutation();
CREATE TRIGGER transactions_no_delete
    BEFORE DELETE ON transactions
    FOR EACH ROW EXECUTE FUNCTION ledger_forbid_mutation();
CREATE TRIGGER transactions_no_truncate
    BEFORE TRUNCATE ON transactions
    FOR EACH STATEMENT EXECUTE FUNCTION ledger_forbid_mutation();
CREATE TRIGGER accounts_no_delete
    BEFORE DELETE ON accounts
    FOR EACH ROW EXECUTE FUNCTION ledger_forbid_mutation();

-- The only permitted change to a transaction is a PENDING one being posted or
-- voided. Everything else about it is frozen at creation.
-- +goose StatementBegin
CREATE FUNCTION ledger_guard_transaction_update() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.status <> 'PENDING' THEN
        RAISE EXCEPTION 'transaction % is % and cannot be modified', OLD.id, OLD.status
            USING ERRCODE = 'restrict_violation';
    END IF;
    IF NEW.status NOT IN ('POSTED', 'VOIDED') THEN
        RAISE EXCEPTION 'invalid status transition % -> %', OLD.status, NEW.status
            USING ERRCODE = 'restrict_violation';
    END IF;
    IF (NEW.id, NEW.ledger_id, NEW.description, NEW.external_ref, NEW.effective_at,
        NEW.expires_at, NEW.reverses_id, NEW.metadata, NEW.created_at)
       IS DISTINCT FROM
       (OLD.id, OLD.ledger_id, OLD.description, OLD.external_ref, OLD.effective_at,
        OLD.expires_at, OLD.reverses_id, OLD.metadata, OLD.created_at) THEN
        RAISE EXCEPTION 'only the status of transaction % may change', OLD.id
            USING ERRCODE = 'restrict_violation';
    END IF;
    RETURN NEW;
END
$$;
-- +goose StatementEnd

CREATE TRIGGER transactions_guard_update
    BEFORE UPDATE ON transactions
    FOR EACH ROW EXECUTE FUNCTION ledger_guard_transaction_update();

-- Double-entry enforcement --------------------------------------------------

-- Checked at COMMIT (deferred) so all entries of a transaction can be inserted
-- first. Pending (hold) entries and posted entries must each balance on their
-- own, per currency. Sums are NUMERIC so they cannot overflow.
-- +goose StatementBegin
CREATE FUNCTION ledger_check_transaction_balanced() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    debits  NUMERIC;
    credits NUMERIC;
BEGIN
    SELECT coalesce(sum(amount) FILTER (WHERE direction = 'DEBIT'), 0),
           coalesce(sum(amount) FILTER (WHERE direction = 'CREDIT'), 0)
      INTO debits, credits
      FROM entries
     WHERE transaction_id = NEW.transaction_id
       AND currency = NEW.currency
       AND pending = NEW.pending;

    IF debits <> credits THEN
        RAISE EXCEPTION 'transaction % is unbalanced in %: debits % <> credits %',
            NEW.transaction_id, NEW.currency, debits, credits
            USING ERRCODE = 'check_violation', CONSTRAINT = 'entries_balanced';
    END IF;
    RETURN NULL;
END
$$;
-- +goose StatementEnd

CREATE CONSTRAINT TRIGGER entries_balanced
    AFTER INSERT ON entries
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION ledger_check_transaction_balanced();

-- +goose Down
DROP TABLE entries;
DROP TABLE transactions;
DROP TABLE accounts;
DROP FUNCTION ledger_check_transaction_balanced();
DROP FUNCTION ledger_guard_transaction_update();
DROP FUNCTION ledger_forbid_mutation();
