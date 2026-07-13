-- 0001_init_schema.up.sql
-- Wallet Management authoritative schema.

CREATE EXTENSION IF NOT EXISTS "pgcrypto";

CREATE TABLE IF NOT EXISTS wallets (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    chain         TEXT NOT NULL,
    type          TEXT NOT NULL CHECK (type IN ('hot','warm','cold')),
    label         TEXT NOT NULL,
    state         TEXT NOT NULL DEFAULT 'active' CHECK (state IN ('active','paused','retired')),
    key_id        TEXT NOT NULL,
    custodian_ref TEXT NOT NULL DEFAULT '',
    rotation_days INT NULL,
    rotation_after_receives INT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (chain, key_id)
);

CREATE TABLE IF NOT EXISTS addresses (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    wallet_id       UUID NOT NULL REFERENCES wallets(id) ON DELETE CASCADE,
    chain           TEXT NOT NULL,
    address         TEXT NOT NULL,
    derivation_path TEXT NOT NULL,
    index           INT NOT NULL,
    change          INT NOT NULL DEFAULT 0,
    state           TEXT NOT NULL DEFAULT 'active' CHECK (state IN ('active','deprecated')),
    receive_count   INT NOT NULL DEFAULT 0,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (wallet_id, index, change),
    UNIQUE (chain, address)
);

-- Only one active address per wallet at a time.
CREATE UNIQUE INDEX IF NOT EXISTS addresses_one_active_per_wallet
    ON addresses (wallet_id) WHERE state = 'active';

CREATE TABLE IF NOT EXISTS balances (
    wallet_id       UUID NOT NULL REFERENCES wallets(id) ON DELETE CASCADE,
    asset           TEXT NOT NULL,
    confirmed       NUMERIC(36,18) NOT NULL DEFAULT 0,
    pending         NUMERIC(36,18) NOT NULL DEFAULT 0,
    locked          NUMERIC(36,18) NOT NULL DEFAULT 0,
    last_block_seen BIGINT NOT NULL DEFAULT 0,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (wallet_id, asset)
);

CREATE TABLE IF NOT EXISTS utxos (
    outpoint      TEXT PRIMARY KEY,
    wallet_id     UUID NOT NULL REFERENCES wallets(id) ON DELETE CASCADE,
    value         NUMERIC(36,18) NOT NULL,
    script_type   TEXT NOT NULL DEFAULT 'p2wpkh',
    confirmations INT NOT NULL DEFAULT 0,
    lock_state    TEXT NOT NULL DEFAULT 'free' CHECK (lock_state IN ('free','locked','spent')),
    locked_at     TIMESTAMPTZ NULL,
    spent_at      TIMESTAMPTZ NULL,
    tx_hash       TEXT NULL,
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS utxos_wallet_state ON utxos (wallet_id, lock_state);

CREATE TABLE IF NOT EXISTS nonces (
    wallet_id       UUID NOT NULL REFERENCES wallets(id) ON DELETE CASCADE,
    chain           TEXT NOT NULL,
    pending_nonce   BIGINT NOT NULL DEFAULT 0,
    broadcast_nonce BIGINT NOT NULL DEFAULT 0,
    version         INT NOT NULL DEFAULT 0,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (wallet_id, chain)
);

CREATE TABLE IF NOT EXISTS withdrawal_requests (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    wallet_id          UUID NOT NULL REFERENCES wallets(id) ON DELETE CASCADE,
    to_address         TEXT NOT NULL,
    asset              TEXT NOT NULL,
    amount             NUMERIC(36,18) NOT NULL,
    state              TEXT NOT NULL DEFAULT 'pending' CHECK (state IN ('pending','whitelisted','signed','broadcast','confirmed','failed')),
    policy_decision_id TEXT NOT NULL DEFAULT '',
    failure_reason     TEXT NOT NULL DEFAULT '',
    tx_hash            TEXT NOT NULL DEFAULT '',
    nonce_value        BIGINT NULL,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS withdrawals_wallet_state ON withdrawal_requests (wallet_id, state);

-- Idempotency: one in-flight withdrawal per (wallet_id, to_address, amount, asset).
CREATE UNIQUE INDEX IF NOT EXISTS withdrawal_inflight_dedup
    ON withdrawal_requests (wallet_id, to_address, amount, asset)
    WHERE state IN ('pending','whitelisted','signed','broadcast');

CREATE TABLE IF NOT EXISTS key_mappings (
    wallet_id       UUID NOT NULL REFERENCES wallets(id) ON DELETE CASCADE,
    key_id          TEXT NOT NULL,
    active_from     TIMESTAMPTZ NOT NULL DEFAULT now(),
    active_to       TIMESTAMPTZ NULL,
    rotation_state  TEXT NOT NULL DEFAULT 'current' CHECK (rotation_state IN ('current','cooling','retired')),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (wallet_id, key_id)
);

CREATE UNIQUE INDEX IF NOT EXISTS key_mappings_one_current
    ON key_mappings (wallet_id) WHERE rotation_state = 'current';

CREATE TABLE IF NOT EXISTS funding_requests (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    wallet_id         UUID NOT NULL REFERENCES wallets(id) ON DELETE CASCADE,
    asset             TEXT NOT NULL,
    amount            NUMERIC(36,18) NOT NULL,
    state             TEXT NOT NULL DEFAULT 'requested' CHECK (state IN ('requested','approved','settled','rejected')),
    treasury_batch_id TEXT NOT NULL DEFAULT '',
    reason            TEXT NOT NULL DEFAULT '',
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS funding_wallet_state ON funding_requests (wallet_id, asset, state);

-- Idempotency: one open 'requested' row per (wallet_id, asset).
CREATE UNIQUE INDEX IF NOT EXISTS funding_open_dedup
    ON funding_requests (wallet_id, asset)
    WHERE state = 'requested';

CREATE TABLE IF NOT EXISTS audit_outbox (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    event_id      UUID NOT NULL UNIQUE,
    wallet_id     UUID NULL,
    event_type    TEXT NOT NULL,
    payload       JSONB NOT NULL,
    seq           BIGINT NOT NULL,
    delivered     BOOLEAN NOT NULL DEFAULT false,
    attempts      INT NOT NULL DEFAULT 0,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    delivered_at  TIMESTAMPTZ NULL
);

CREATE INDEX IF NOT EXISTS audit_outbox_undelivered ON audit_outbox (seq) WHERE delivered = false;
CREATE INDEX IF NOT EXISTS audit_outbox_wallet_seq ON audit_outbox (wallet_id, seq);

CREATE TABLE IF NOT EXISTS balance_events (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    wallet_id    UUID NOT NULL,
    asset        TEXT NOT NULL,
    block_height BIGINT NOT NULL,
    event_id     TEXT NOT NULL,
    applied_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (wallet_id, asset, block_height, event_id)
);