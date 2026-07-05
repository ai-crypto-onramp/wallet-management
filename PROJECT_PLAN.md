# Project Plan — Wallet Management

Wallet Management is the custody-inventory backbone of the on-ramp: it owns the
hot/warm/cold wallet inventory, deterministic per-chain address derivation,
address rotation, per-chain balance tracking with confirmation depth, BTC UTXO
and EVM nonce coordination, treasury funding requests, withdrawal flow with
Policy/Risk Engine whitelisting, and MPC + Blockchain Gateway integration.
This plan decomposes the build into ordered, independently verifiable stages
that mirror the data and call-flow dependencies described in the README.

## Stage 1

## Stage 1 — Database Schema & Migrations

### Goal

Establish the authoritative PostgreSQL schema for all wallet-management state
and a repeatable migration pipeline, so every subsequent stage can persist
against a stable data model.

### Tasks

- [ ] Pick a migration tool (e.g. `golang-migrate` or `goose`) and add it to
      `Makefile` targets (`migrate-up`, `migrate-down`, `migrate-new`).
- [ ] Create `migrations/0001_init_schema.up.sql` and the matching down
      migration with the following tables and constraints:
  - `wallets` — `id UUID PK`, `chain text`, `type text CHECK in
    (hot,warm,cold)`, `label text`, `state text CHECK in
    (active,paused,retired)`, `key_id text`, `custodian_ref text`,
    `created_at timestamptz`, `updated_at timestamptz`; unique `(chain, key_id)`.
  - `addresses` — `id UUID PK`, `wallet_id UUID FK→wallets`, `chain text`,
    `address text`, `derivation_path text`, `index int`, `state text CHECK in
    (active,deprecated)`, `created_at timestamptz`; unique `(wallet_id, index)`,
    unique `(chain, address)`.
  - `balances` — `wallet_id UUID`, `asset text`, `confirmed numeric`,
    `pending numeric`, `locked numeric`, `last_block_seen bigint`,
    `updated_at timestamptz`; PK `(wallet_id, asset)`.
  - `utxos` — `outpoint text PK`, `wallet_id UUID FK→wallets`, `value numeric`,
    `script_type text`, `confirmations int`, `lock_state text CHECK in
    (free,locked,spent)`, `locked_at timestamptz`, `spent_at timestamptz`,
    `updated_at timestamptz`.
  - `nonces` — `wallet_id UUID`, `chain text`, `pending_nonce bigint`,
    `broadcast_nonce bigint`, `version int`, `updated_at timestamptz`;
    PK `(wallet_id, chain)`.
  - `withdrawal_requests` — `id UUID PK`, `wallet_id UUID FK→wallets`,
    `to_address text`, `asset text`, `amount numeric`, `state text CHECK in
    (pending,whitelisted,signed,broadcast,confirmed,failed)`,
    `policy_decision_id text`, `tx_hash text`, `created_at timestamptz`,
    `updated_at timestamptz`.
  - `key_mappings` — `wallet_id UUID`, `key_id text`, `active_from timestamptz`,
    `active_to timestamptz`, `rotation_state text CHECK in
    (current,cooling,retired)`, `created_at timestamptz`;
    PK `(wallet_id, key_id)`.
  - `funding_requests` — `id UUID PK`, `wallet_id UUID FK→wallets`,
    `asset text`, `amount numeric`, `state text CHECK in
    (requested,approved,settled,rejected)`, `treasury_batch_id text`,
    `created_at timestamptz`, `updated_at timestamptz`.
- [ ] Add idempotency / dedup unique indexes:
  - `withdrawal_requests (wallet_id, to_address, amount, asset)` partial where
    `state` IN (pending, whitelisted, signed, broadcast).
  - `funding_requests (wallet_id, asset, state)` partial where state =
    `requested`.
- [ ] Add seed/reference data: a `chains` lookup enum or check list
  (`ethereum`, `polygon`, `arbitrum`, `base`, `optimism`, `solana`, `bitcoin`).
- [ ] Create `internal/storage/postgres/schema.sql` with the same DDL for tests
  and `docker-compose` bootstrap.
- [ ] Write a migration smoke test that runs up→down→up and asserts all tables
  exist / are dropped / exist again.
- [ ] Add `docker-compose.yml` (or extend existing) with PostgreSQL 15 + Redis 7
  for local dev and CI.

### Acceptance criteria

- `make migrate-up` brings an empty database to the full schema; `make
  migrate-down` drops every wallet-management table.
- All eight tables exist with the constraints and indexes specified.
- The migration round-trip test passes on PostgreSQL 15 in CI.
- README "Data model" table is fully covered by the schema (no missing columns).

## Stage 2 — Wallet CRUD & Per-Chain Address Derivation

### Goal

Implement wallet lifecycle (create/read/update state) and deterministic,
chain-specific receive address derivation (EVM BIP-44, Solana ed25519 BIP-44,
BTC BIP-44/49/84 native SegWit). Private key material never enters this service;
only public derivation outputs are persisted.

### Tasks

- [ ] Define `internal/wallet` domain types: `Wallet`, `Address`,
  `WalletState`, `WalletType`, `Chain`.
- [ ] Implement `WalletRepository` (Postgres) with `Create`, `Get`,
  `UpdateState`, `List` (filter by chain/type/state).
- [ ] Implement `WalletService` with:
  - `Create(ctx, {chain, type, label})` — validates chain/type, persists wallet
    in `active` state, allocates a `key_id` placeholder (bound in Stage 7).
  - `Get(ctx, id)` — returns wallet + bound `key_id` from `key_mappings` (or
    `wallets.key_id`).
  - `SetState(ctx, id, state)` — `active`↔`paused`, terminal `retired`.
- [ ] Define a `Deriver` interface:
  ```go
  type Deriver interface {
      DeriveNext(ctx context.Context, walletID uuid.UUID, chain Chain) (Address, error)
  }
  ```
- [ ] Implement `EVMDeriver`:
  - BIP-44 path `m/44'/60'/0'/0/index`, monotonic index from `addresses` table.
  - Address is Keccak-256 of the compressed pubkey, EIP-55 checksummed.
  - Cache derived pubkey in Redis (`DERIVATION_CACHE_TTL`) to hit <10 ms p99.
- [ ] Implement `SolanaDeriver`:
  - ed25519 BIP-44 path `m/44'/501'/0'/0'`, one derived address per wallet.
  - base58 encode.
- [ ] Implement `BTCDeriver`:
  - BIP-44/49/84 with change-chain separation (`/0` receive, `/1` change).
  - bech32 native SegWit (`bc1q...`).
  - Track derivation index per wallet + change-chain; record gap-limit info.
- [ ] Implement `AddressRepository` with atomic `ReserveNextIndex` (row lock on
  per-wallet index counter) and `Insert`.
- [ ] REST handlers under `internal/api/rest`:
  - `POST /v1/wallets`
  - `GET /v1/wallets/:id`
  - `GET /v1/wallets/:id/addresses?derive=true`
  - `POST /v1/wallets/:id/addresses/derive`
- [ ] Add per-deriver unit tests with known BIP-44 test vectors (vectors from
  BIP-44 / SLIP-44 / known tooling for Ethereum, Solana, Bitcoin).

### Acceptance criteria

- Creating a wallet and deriving the first 3 EVM addresses produces
  EIP-55-checksummed addresses with monotonic indexes 0,1,2 persisted.
- Solana derivation yields one base58 address per wallet; re-derivation returns
  the same address.
- BTC derivation produces distinct receive (`/0`) and change (`/1`) bech32
  addresses.
- Address derivation latency p99 < 10 ms with the Redis cache warm (benchmark
  test).
- All public-only: no private key bytes are logged, persisted, or returned.

## Stage 3 — Address Rotation Policy

### Goal

Make receive addresses rotate on a configurable cadence (time- and
receive-count-based), support on-demand derivation, and mark retired addresses
`deprecated` while keeping them collectable.

### Tasks

- [ ] Add config: `DEFAULT_ADDRESS_ROTATION_DAYS`, plus per-wallet override
  fields (`rotation_days int NULL`, `rotation_after_receives int NULL`).
- [ ] Implement `RotationPolicy` service:
  - On `GET /v1/wallets/:id/addresses?derive=true`: if the current active
    address is older than `rotation_days` OR has received
    `rotation_after_receives` deposits (counted from `balances`/`addresses`),
    mark it `deprecated` and derive the next active address.
  - On-demand `POST /v1/wallets/:id/addresses/derive` always rotates.
- [ ] Ensure only one `active` address per wallet at a time (DB partial unique
  index `WHERE state='active'`).
- [ ] Keep `deprecated` addresses queryable and valid for collection; exclude
  them from new share-outs.
- [ ] Emit a `wallet.address.rotated` audit event (handled in Stage 8, stub the
  hook now).
- [ ] Tests: time-based rotation, count-based rotation, on-demand rotation,
  concurrent derive requests (Redis lock on wallet_id).

### Acceptance criteria

- After `DEFAULT_ADDRESS_ROTATION_DAYS` elapses, the next `addresses?derive=true`
  call rotates the active address.
- A retired wallet cannot derive new addresses (returns 409).
- `deprecated` addresses remain readable and accepted for collection but never
  returned as the current receive address.

## Stage 4 — Per-Chain Balance Tracking & Confirmation Depth

### Goal

Track per `(wallet_id, asset)` confirmed, pending, and locked balances with
chain-specific confirmation thresholds, reconciled against Blockchain Gateway
events plus a 60 s polling backstop.

### Tasks

- [ ] Add config: `CONFIRMATIONS_REQUIRED_EVM`, `CONFIRMATIONS_REQUIRED_BTC`,
  `CONFIRMATIONS_REQUIRED_SOL` (finalized slot).
- [ ] Implement `BalanceRepository` with upsert on
  `(wallet_id, asset)`: `confirmed`, `pending`, `locked`, `last_block_seen`.
- [ ] Implement `BalanceService`:
  - `ApplyConfirmationEvent(event)` — for EVM/BTC, move value from `pending` to
    `confirmed` once `confirmations >= threshold`; for Solana, only on finalized
    slot.
  - `ApplyReorgEvent(event)` — demote confirmed back to pending for blocks
    beyond reorg depth; trigger UTXO restore (Stage 5).
  - `GetBalances(walletID)` returns confirmed + pending per asset.
- [ ] Polling reconciler: per-wallet 60 s full pull from Blockchain Gateway
  (Stage 7 integration); for now define the gRPC client interface and stub.
- [ ] REST handler `GET /v1/wallets/:id/balances`.
- [ ] Tests: EVM 12-conf threshold, BTC 6-conf, Solana finalized-only, reorg
  demotion, idempotent event application (dedup on
  `(wallet_id, asset, block_height, event_id)`).

### Acceptance criteria

- A deposit with < threshold confirmations shows in `pending` only; downstream
  `GetBalances` never exposes `pending` as spendable.
- After threshold confirmations, value moves atomically to `confirmed`.
- Reorg event demotes the affected block's confirmed value back to `pending`.
- 60 s backstop poll reconciles drift between events.

## Stage 5 — EVM Nonce Coordination & BTC UTXO Management

### Goal

Spend-path state management: EVM per-wallet nonce counter with Redis-backed
lock + pre-allocation/rollback, and BTC UTXO set tracking with lock-on-construct
and spent-on-broadcast / restore-on-reorg.

### Tasks

- [ ] Implement `NonceRepository` (Postgres `nonces` table, optimistic
  concurrency via `version`).
- [ ] Implement `NonceService`:
  - `ReserveNonce(ctx, walletID, chain)` — acquires Redis lock
    `nonce:lock:{wallet_id}:{chain}`, increments `pending_nonce`,
    returns reserved value; releases lock.
  - `CommitNonce(ctx, walletID, chain, nonce)` — advances `broadcast_nonce`.
  - `RollbackNonce(ctx, walletID, chain, nonce)` — releases the reserved nonce
    without advancing broadcast (gap-safe via monotonically increasing
    `pending_nonce`).
- [ ] Implement `UTXORepository` (Postgres `utxos`).
- [ ] Implement `UTXOService`:
  - `SelectForAmount(walletID, asset, amount)` — greedy/branch-and-bound
    selection over `lock_state='free'`, atomically marks selected UTXOs
    `locked` (within a tx).
  - `MarkSpent(outpoints, txHash)` — `lock_state='spent'`, set `spent_at`,
    `tx_hash`.
  - `RestoreOnReorg(outpoints)` — flip spent→free, reset `spent_at`.
  - `PruneFinalized(outpoints)` — delete or archive once finality is reached.
- [ ] Integration: wire `NonceService` and `UTXOService` into the withdrawal
  construct path (Stage 7) — for now expose service APIs and unit tests.
- [ ] Tests: concurrent nonce reservation (10 goroutines, no gaps/dupes),
  UTXO selection under concurrent withdrawal attempts (no double-spend),
  reorg restore path.

### Acceptance criteria

- 10 concurrent `ReserveNonce` calls return distinct sequential nonces with no
  gaps in the committed sequence.
- A failed broadcast rolls back the reserved nonce without producing a gap.
- UTXO selection never locks the same outpoint for two concurrent withdrawals.
- Reorg event restores previously spent UTXOs to `free`.

## Stage 6 — Funding Requests to Treasury

### Goal

Emit idempotent funding requests to Treasury Orchestration when a hot wallet's
confirmed balance drops below `HOT_WALLET_MIN_BALANCE_USD`, track request state
through `requested → approved → settled | rejected`, and reconcile settlement
callbacks.

### Tasks

- [ ] Implement `FundingRequestRepository` (Postgres `funding_requests`) with
  idempotency: one `requested` row per `(wallet_id, asset)` (partial unique
  index from Stage 1).
- [ ] Implement `FundingService`:
  - `EvaluateAndRequest(ctx, walletID, asset)` — if wallet is `hot`, confirmed
    balance < threshold, and no open `requested` row exists, insert a new
    `funding_requests` row in state `requested` and POST to
    `TREASURY_ORCHESTRATION_URL` with an idempotency key
    (`fr:{wallet_id}:{asset}:{request_uuid}`).
  - `MarkApproved(id, treasuryBatchID)`, `MarkSettled(id)`,
    `MarkRejected(id, reason)` — state machine transitions.
- [ ] Trigger evaluation from the balance update path (Stage 4): whenever a
  confirmed balance decreases, call `EvaluateAndRequest` asynchronously.
- [ ] REST handler `POST /v1/wallets/:id/funding-request` for manual trigger
  with body `{asset, amount, reason}`.
- [ ] Tests: threshold-not-crossed (no request), threshold-crossed (one
  request), duplicate suppression, state transitions, treasury REST client
  retry with idempotency key.

### Acceptance criteria

- Confirmed balance crossing below `HOT_WALLET_MIN_BALANCE_USD` produces exactly
  one `requested` funding request, even under concurrent triggers.
- Manual `POST /v1/wallets/:id/funding-request` creates a request for warm/cold
  wallets too (operator-initiated), still idempotent per `(wallet_id, asset)`
  while a `requested` row exists.
- Treasury callbacks advance the state machine correctly; invalid transitions
  are rejected.

## Stage 7 — Withdrawal Flow & Policy/Risk Whitelist Checks

### Goal

End-to-end withdrawal construction: validate destination against the
Policy/Risk Engine whitelist synchronously, coordinate EVM nonce or BTC UTXO
selection, hand off to MPC signing and Blockchain Gateway broadcast, and track
the withdrawal through `pending → whitelisted → signed → broadcast →
confirmed | failed`.

### Tasks

- [ ] Implement `WithdrawalRepository` (Postgres `withdrawal_requests`) with
  idempotency on `(wallet_id, to_address, amount, asset)` while in a non-final
  state.
- [ ] Implement `WithdrawalService`:
  - `Create(ctx, {wallet_id, to_address, amount, asset})` — insert in state
    `pending`, call Policy Engine `/v1/whitelist/check` synchronously
    (`POLICY_RISK_ENGINE_URL`); on reject → state `failed` with reason
    `not_whitelisted`; on approve → state `whitelisted`, persist
    `policy_decision_id`.
  - `ConstructAndSign(ctx, id)` — for EVM: reserve nonce (Stage 5), build
    unsigned tx, call `mpc-signing-service` gRPC `Sign(key_id, tx)` using the
    wallet's bound `key_id`; for BTC: select UTXOs (Stage 5), build tx, sign.
    On success → state `signed`.
  - `Broadcast(ctx, id)` — submit signed tx to Blockchain Gateway gRPC
    `BroadcastTx`; on ack → state `broadcast`, store `tx_hash`.
  - `Confirm(ctx, id, event)` — on confirmation event from Blockchain Gateway →
    state `confirmed`; on failure → `failed`.
- [ ] REST handlers: `POST /v1/withdrawals`, `GET /v1/withdrawals/:id`.
- [ ] gRPC server methods for Blockchain Gateway confirmation callbacks
  (`OnConfirmation`, `OnReorg`) and for Transaction Orchestrator
  (`ConstructWithdrawal`).
- [ ] Tests: whitelist reject path, whitelist approve + EVM sign + broadcast
  happy path, BTC UTXO selection + sign + broadcast, broadcast failure rolls
  back nonce / unlocks UTXOs, reorg confirmation rollback.

### Acceptance criteria

- A withdrawal to a non-whitelisted address never reaches `whitelisted`
  state and returns the policy decision id for auditability.
- EVM withdrawal happy path drives state through pending → whitelisted →
  signed → broadcast → confirmed with a monotonic nonce and no gaps.
- BTC withdrawal locks UTXOs at construct, marks spent on broadcast, restores
  on reorg.
- Broadcast failure releases the reserved nonce and unlocks UTXOs, leaving
  the withdrawal in `failed`.

## Stage 8 — MPC & Blockchain Gateway Integration

### Goal

Wire the gRPC clients/servers that integrate Wallet Management with
`mpc-signing-service` (key_id lookup + signing), `blockchain-gateway`
(broadcast + confirmation/reorg callbacks), and the wallet-to-`key_id` mapping
with cooling-off key rotation.

### Tasks

- [ ] Define protobufs under `proto/`:
  - `wallet.proto` — `ResolveKeyID(wallet_id) → key_id`, used by MPC Signing.
  - `gateway.proto` — `OnConfirmation(event)`, `OnReorg(event)`,
    `BroadcastTx(signed_tx) → tx_hash`.
- [ ] `buf generate` to produce Go stubs; add `make proto` target.
- [ ] Implement `KeyMappingRepository` (Postgres `key_mappings`) and
  `KeyMappingService`:
  - `Bind(walletID, keyID)` at provisioning.
  - `Rotate(walletID, newKeyID)` — set old mapping `rotation_state=cooling`
    with `active_to = now + cooling_period`; insert new mapping `current`.
    After cooling period, old mapping → `retired`.
  - `ResolveActive(walletID)` — returns the `current` key_id, or during cooling
    allows either (caller decides via MPC policy).
- [ ] Implement gRPC server: `ResolveKeyID`, `OnConfirmation`, `OnReorg`.
- [ ] Implement gRPC clients: `MPCSigningClient.Sign`, `GatewayClient.BroadcastTx`.
- [ ] Wire Stage 4 balance events, Stage 5 nonce/UTXO, and Stage 7 withdrawal
  flows to the gRPC clients.
- [ ] Config: `MPC_SIGNING_URL`, `BLOCKCHAIN_GATEWAY_URL`, cooling period.
- [ ] Integration tests with mock gRPC servers for the full withdrawal saga.

### Acceptance criteria

- MPC Signing Service can resolve `wallet_id → key_id` via gRPC and receive the
  active key, including both keys during cooling-off.
- Blockchain Gateway confirmation callback advances a withdrawal to
  `confirmed`; reorg callback rolls it back.
- Key rotation produces a `cooling` mapping and a new `current` mapping; both
  resolve during cooling, only the new one after.
- All gRPC traffic uses mTLS + service identity (configurable client/server
  TLS).

## Stage 9 — Audit Event Emission

### Goal

Emit durable audit events for every wallet lifecycle, derivation, rotation,
balance, withdrawal, and funding action to the Audit / Event Log, with
at-least-once delivery and ordered per wallet.

### Tasks

- [ ] Define the event schema (versioned JSON): `wallet.created`,
  `wallet.state_changed`, `wallet.address.derived`, `wallet.address.rotated`,
  `wallet.balance.updated`, `wallet.funding.requested`,
  `wallet.funding.settled`, `withdrawal.created`,
  `withdrawal.whitelist_checked`, `withdrawal.signed`,
  `withdrawal.broadcast`, `withdrawal.confirmed`,
  `withdrawal.failed`, `key.rotated`.
- [ ] Implement `AuditEmitter` with batching + async flush to
  `AUDIT_EVENT_LOG_URL`; per-wallet ordering via a Redis stream or sequence
  number.
- [ ] Outbox pattern: persist events in an `audit_outbox` table within the same
  DB tx as the state change; a background worker drains the outbox to the Audit
  Event Log.
- [ ] Add `audit_outbox` table migration.
- [ ] At-least-once delivery with idempotency key `event_id` (UUID) so the
  Audit Log can dedup.
- [ ] Tests: outbox write within tx (rollback aborts event), outbox drain
  retry on failure, ordering per wallet, dedup on redelivery.

### Acceptance criteria

- Every state-changing operation writes an audit event in the same DB
  transaction; a rolled-back operation emits no event.
- Events are delivered at-least-once; redelivery is deduped by `event_id`.
- Events for a given `wallet_id` are delivered in order.
- Audit events contain no private key material and no PII (PII-free service).

## Stage 10 — Tests, Coverage & Docker

### Goal

Harden the service for production: race-tested unit + integration suites,
coverage gate ≥ 80%, golangci-lint clean, reproducible local dev via Docker
Compose, and CI pipeline that runs the full matrix.

### Tasks

- [ ] Integration test harness using `docker-compose` (Postgres + Redis) with
  testcontainers or `docker compose up --wait` in CI.
- [ ] Test suites per stage: wallet, derivation (per chain), rotation, balance,
  nonce, UTXO, funding, withdrawal, key mapping, audit.
- [ ] Coverage gate in `codecov.yml` and CI: fail build if coverage < 80%.
- [ ] `golangci-lint run` clean; enable linters: `govet`, `staticcheck`,
  `errcheck`, `gosec`, `gocyclo`, `misspell`.
- [ ] `Dockerfile` multi-stage build (already present — verify and tighten:
  non-root user, distroless or alpine final, HEALTHCHECK).
- [ ] `docker-compose.yml` with `wallet-management`, `postgres`, `redis`,
  healthchecks, and volume for pg data.
- [ ] CI workflow matrix: Go 1.22 / 1.23, PostgreSQL 15, Redis 7; jobs:
  lint, unit, integration, coverage upload to Codecov.
- [ ] `Makefile` targets: `test`, `test-race`, `test-integration`, `lint`,
  `coverage`, `run`, `docker-up`, `docker-down`.
- [ ] Load test skeleton: derivation 500/s and balance read 2,000/s benchmarks
  (`go test -bench`).

### Acceptance criteria

- `go test ./... -race -cover` passes with ≥ 80% coverage.
- `golangci-lint run` exits 0.
- `docker compose up` brings the full stack healthy; `make run` connects.
- CI runs lint + unit + integration + coverage on every PR and main push.
- Benchmarks demonstrate derivation throughput ≥ 500/s and balance reads ≥
  2,000/s on the CI runner.