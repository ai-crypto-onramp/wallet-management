-- Stage 1: initial wallet-management schema.
-- Reversible: see 0001_init_schema.down.sql.

BEGIN;

-- Chain reference data ------------------------------------------------------
CREATE TABLE chains (
    name text PRIMARY KEY
);

INSERT INTO chains (name) VALUES
    ('ethereum'),
    ('polygon'),
    ('arbitrum'),
    ('base'),
    ('optimism'),
    ('solana'),
    ('bitcoin');

-- Wallets -------------------------------------------------------------------
CREATE TABLE wallets (
    id            uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    chain         text        NOT NULL REFERENCES chains(name),
    type          text        NOT NULL CHECK (type IN ('hot', 'warm', 'cold')),
    label         text        NOT NULL,
    state         text        NOT NULL CHECK (state IN ('active', 'paused', 'retired')),
    key_id        text        NOT NULL,
    custodian_ref text,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    UNIQUE (chain, key_id)
);

-- Addresses -----------------------------------------------------------------
CREATE TABLE addresses (
    id              uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    wallet_id       uuid        NOT NULL REFERENCES wallets(id) ON DELETE CASCADE,
    chain           text        NOT NULL REFERENCES chains(name),
    address         text        NOT NULL,
    derivation_path text,
    index           integer     NOT NULL,
    state           text        NOT NULL CHECK (state IN ('active', 'deprecated')),
    created_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (wallet_id, index),
    UNIQUE (chain, address)
);

-- Balances ------------------------------------------------------------------
CREATE TABLE balances (
    wallet_id       uuid        NOT NULL REFERENCES wallets(id) ON DELETE CASCADE,
    asset           text        NOT NULL,
    confirmed       numeric     NOT NULL DEFAULT 0,
    pending         numeric     NOT NULL DEFAULT 0,
    locked          numeric     NOT NULL DEFAULT 0,
    last_block_seen bigint      NOT NULL DEFAULT 0,
    updated_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (wallet_id, asset)
);

-- UTXOs ---------------------------------------------------------------------
CREATE TABLE utxos (
    outpoint      text        PRIMARY KEY,
    wallet_id     uuid        NOT NULL REFERENCES wallets(id) ON DELETE CASCADE,
    value         numeric     NOT NULL,
    script_type   text        NOT NULL,
    confirmations integer     NOT NULL DEFAULT 0,
    lock_state    text        NOT NULL CHECK (lock_state IN ('free', 'locked', 'spent')),
    locked_at     timestamptz,
    spent_at      timestamptz,
    updated_at    timestamptz NOT NULL DEFAULT now()
);

-- Nonces --------------------------------------------------------------------
CREATE TABLE nonces (
    wallet_id       uuid        NOT NULL REFERENCES wallets(id) ON DELETE CASCADE,
    chain           text        NOT NULL REFERENCES chains(name),
    pending_nonce   bigint      NOT NULL DEFAULT 0,
    broadcast_nonce bigint      NOT NULL DEFAULT 0,
    version         integer     NOT NULL DEFAULT 1,
    updated_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (wallet_id, chain)
);

-- Withdrawal requests -------------------------------------------------------
CREATE TABLE withdrawal_requests (
    id                 uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    wallet_id          uuid        NOT NULL REFERENCES wallets(id) ON DELETE CASCADE,
    to_address         text        NOT NULL,
    asset              text        NOT NULL,
    amount             numeric     NOT NULL,
    state              text        NOT NULL CHECK (state IN
                        ('pending', 'whitelisted', 'signed', 'broadcast', 'confirmed', 'failed')),
    policy_decision_id text,
    tx_hash            text,
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX withdrawal_requests_dedup_idx
    ON withdrawal_requests (wallet_id, to_address, amount, asset)
    WHERE state IN ('pending', 'whitelisted', 'signed', 'broadcast');

-- Key mappings --------------------------------------------------------------
CREATE TABLE key_mappings (
    wallet_id      uuid        NOT NULL REFERENCES wallets(id) ON DELETE CASCADE,
    key_id         text        NOT NULL,
    active_from    timestamptz NOT NULL DEFAULT now(),
    active_to      timestamptz,
    rotation_state text        NOT NULL CHECK (rotation_state IN ('current', 'cooling', 'retired')),
    created_at     timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (wallet_id, key_id)
);

-- Funding requests ----------------------------------------------------------
CREATE TABLE funding_requests (
    id                 uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    wallet_id          uuid        NOT NULL REFERENCES wallets(id) ON DELETE CASCADE,
    asset              text        NOT NULL,
    amount             numeric     NOT NULL,
    state              text        NOT NULL CHECK (state IN
                        ('requested', 'approved', 'settled', 'rejected')),
    treasury_batch_id  text,
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX funding_requests_dedup_idx
    ON funding_requests (wallet_id, asset, state)
    WHERE state = 'requested';

COMMIT;