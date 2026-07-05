# Wallet Management
![CI](https://github.com/ai-crypto-onramp/wallet-management/actions/workflows/ci.yml/badge.svg)

Hot/warm wallet inventory, address derivation/rotation, and per-chain balance tracking for the crypto on-ramp custody layer.

## Overview / Responsibilities

Wallet Management is the custody-inventory backbone of the on-ramp. It owns the
lifecycle and metadata of every hot, warm, and cold wallet, derives and rotates
on-chain deposit/receive addresses per chain, tracks confirmed balances, manages
UTXO and nonce state, and maps each wallet to the MPC `key_id` used for signing.

It is called synchronously by the **MPC Signing Service** (key_id lookup) and the
**Blockchain Gateway** (broadcast + confirmation feedback), and asynchronously by
**Treasury Orchestration** for hot-wallet funding. Withdrawals pass through
address whitelisting via the **Policy / Risk Engine** before being released.

Core responsibilities:

- Maintain hot/warm/cold wallet inventory and labels.
- Derive deterministic addresses per chain (EVM BIP-44, Solana ed25519, BTC UTXO).
- Rotate receive addresses on a configurable cadence and on demand.
- Track per-chain, per-asset balances with chain-appropriate confirmation depth.
- Manage BTC UTXO set (selection, locking, marking spent) and EVM nonces.
- Map each wallet to its MPC `key_id` for threshold signing.
- Issue funding requests to Treasury Orchestration when hot-wallet balances drop.
- Enforce withdrawal destination whitelisting in concert with the Policy Engine.
- Emit wallet lifecycle and balance audit events to the Audit / Event Log.

## Language & Tech Stack

- **Language:** Go (module path `github.com/onramp/wallet-management`).
- **Address derivation:** BIP-32/44 deterministic derivation per chain:
  - EVM chains (Ethereum, Polygon, Arbitrum, Base, Optimism): BIP-44 path
    `m/44'/60'/0'/0/x`, Keccak-256 / EIP-55 checksummed addresses.
  - Solana: ed25519 BIP-44 path `m/44'/501'/0'/0'`, base58 addresses.
  - Bitcoin (UTXO): BIP-44 / BIP-49 / BIP-84 paths, native SegWit bech32 addresses,
    full UTXO tracking and selection.
- **Balance sync:** polling + webhook hybrid driven by Blockchain Gateway
  confirmation events; per-chain confirmation depth thresholds.
- **Storage:** PostgreSQL (wallets, addresses, balances, UTXOs, nonces,
  withdrawal requests, key mappings) + Redis (nonce lock, idempotency keys,
  derivation cache).
- **RPC:** REST for control plane + gRPC for internal service-to-service calls
  from MPC Signing and Blockchain Gateway.

## System Requirements

### Wallet inventory

- Support wallet types `hot`, `warm`, `cold` with distinct policy and access
  profiles. Hot wallets are spendable via MPC signing; warm require a quorum
  policy uplift; cold are receive-only / require out-of-band movement.
- Each wallet is scoped to a single `chain` and carries a human-readable `label`,
  an operational state (`active`, `paused`, `retired`), and a custody provider
  reference (in-house MPC or external custodian).
- Wallet-to-`key_id` mapping is authoritative: the MPC Signing Service reads the
  mapping to resolve which threshold key signs for a given wallet/address.

### Deterministic address derivation

- EVM: BIP-44 `m/44'/60'/0'/0/index`; addresses are EIP-55 checksummed.
- Solana: ed25519 BIP-44 `m/44'/501'/0'/0'`; one derived address per wallet.
- BTC (UTXO): BIP-44/49/84 with change-chain separation; bech32 native SegWit.
- Derivation index is monotonic and persisted; gaps are tracked for gap-limit
  scanning. Derived public addresses only — private material never leaves the
  MPC layer.

### Address rotation policy

- Default rotation cadence configurable per chain (e.g. rotate receive address
  every N days or after M receives). Overridable per wallet.
- On-demand derivation endpoint for one-off address generation (deposits,
  internal transfers).
- Retired addresses remain valid for collection but are flagged
  `deprecated` and excluded from new share-outs.

### Per-chain balance tracking with confirmation depth

- Balances are tracked per `(wallet_id, asset)` and reconciled against
  Blockchain Gateway confirmation events.
- Confirmation thresholds are chain-specific:
  - EVM: configurable (default 12-32 blocks depending on chain reorg risk).
  - BTC: 6 confirmations default, configurable.
  - Solana: finalized slot only.
- Expose both `confirmed` and `pending` balances; downstream services must not
  spend `pending` balances.

### UTXO management (BTC)

- Track the full UTXO set per BTC wallet: outpoint, value, script type,
  confirmation count, lock status.
- Support UTXO locking during withdrawal construction to prevent double-spend.
- Mark UTXOs spent on broadcast and prune on finality; restore on reorg.

### Nonce management (EVM)

- Maintain a per-wallet, per-chain pending nonce counter in PostgreSQL.
- Redis-backed nonce lock to serialize concurrent withdrawal construction and
  prevent nonce gaps / collisions.
- Support parallelizable transactions via nonce pre-allocation with rollback on
  broadcast failure.

### Wallet-to-`key_id` mapping for MPC

- Each wallet is bound to exactly one MPC `key_id` at provisioning time.
- Key rotation is supported via a new `key_id` binding with a cooling-off period
  during which both keys may sign.

### Funding requests to treasury

- When a hot wallet's confirmed balance drops below a configurable threshold,
  Wallet Management emits a funding request to Treasury Orchestration.
- Funding requests are idempotent and tracked in `funding_requests` with state
  `requested`, `approved`, `settled`, `rejected`.

### Withdrawal address whitelisting

- Outbound withdrawals may only be sent to addresses whitelisted by the
  Policy / Risk Engine. Wallet Management performs the whitelist lookup
  synchronously before constructing a withdrawal.
- Supports per-user and per-partner whitelist scopes; whitelist entries carry
  an expiry and a confidence score from KYT.

## Non-Functional Requirements

| Requirement | Target |
|---|---|
| Address derivation latency (p99) | < 10 ms (cached public-key derivation) |
| Balance consistency | Strongly consistent with Blockchain Gateway event log; eventual between polling and webhook. |
| Balance refresh cadence | On-chain events drive immediate refresh; full reconciliation poll every 60 s per wallet as backstop. |
| Availability | 99.99% — service is on the withdrawal hot path; outage blocks user withdrawals. |
| Durability | All wallet/derivation state persisted before acknowledgement; no in-memory-only authoritative state. |
| Throughput | Sustain 500 derivation/s and 2,000 balance reads/s per region. |
| Security | All write endpoints require mTLS + service identity; PII-free service. |

## Technical Specifications

### API surface

- **REST** (control plane, JSON over HTTPS) — wallet lifecycle, address
  derivation, balance queries, withdrawals, funding requests.
- **gRPC** (internal service-to-service) — used by MPC Signing Service for
  `key_id` lookup and by Blockchain Gateway for confirmation callbacks.

### Endpoints

| Method & Path | Description |
|---|---|
| `POST /v1/wallets` | Create a wallet. Body: `{chain, type: hot\|warm\|cold, label}`. |
| `GET /v1/wallets/:id` | Fetch wallet metadata, state, and bound `key_id`. |
| `GET /v1/wallets/:id/addresses?derive=true` | List addresses; if `derive=true`, auto-derives the next address per rotation policy. |
| `POST /v1/wallets/:id/addresses/derive` | Explicitly derive a new receive address. Returns the derived address and index. |
| `GET /v1/wallets/:id/balances` | Return confirmed + pending balances per asset for the wallet. |
| `POST /v1/wallets/:id/funding-request` | Request treasury funding for a hot wallet. Body: `{asset, amount, reason}`. |
| `POST /v1/withdrawals` | Create a withdrawal. Body: `{wallet_id, to_address, amount, asset}`. Subject to whitelist + policy check. |
| `GET /v1/withdrawals/:id` | Fetch withdrawal status (`pending`, `whitelisted`, `signed`, `broadcast`, `confirmed`, `failed`). |

### Data model

| Table | Purpose |
|---|---|
| `wallets` | Wallet inventory: `id`, `chain`, `type`, `label`, `state`, `key_id`, `custodian_ref`, `created_at`, `updated_at`. |
| `addresses` | Derived addresses: `id`, `wallet_id`, `chain`, `address`, `derivation_path`, `index`, `state` (`active`, `deprecated`), `created_at`. |
| `balances` | Per `(wallet_id, asset)` confirmed + pending + locked amounts; last-block seen. |
| `utxos` | BTC UTXO set: `outpoint`, `wallet_id`, `value`, `script_type`, `confirmations`, `lock_state`, `spent_at`. |
| `nonces` | EVM per-wallet nonce counter: `wallet_id`, `chain`, `pending_nonce`, `broadcast_nonce`, `version`. |
| `withdrawal_requests` | Outbound withdrawals: `id`, `wallet_id`, `to_address`, `asset`, `amount`, `state`, `policy_decision_id`, `tx_hash`. |
| `key_mappings` | Wallet-to-MPC mapping: `wallet_id`, `key_id`, `active_from`, `active_to`, `rotation_state`. |
| `funding_requests` | Treasury funding requests: `id`, `wallet_id`, `asset`, `amount`, `state`, `treasury_batch_id`. |

### Integrations

| Consumer / Producer | Direction | Purpose |
|---|---|---|
| `mpc-signing-service` | inbound (gRPC) | Resolves `wallet_id` → `key_id` before threshold signing. |
| `blockchain-gateway` | bidirectional | Receives confirmation/reorg events; sends broadcast tx for withdrawals. |
| `treasury-orchestration` | outbound (async + REST) | Funding requests for hot wallets; receives settlement confirmations. |
| `policy-risk-engine` | outbound (sync REST) | Whitelist checks for withdrawal `to_address`; velocity/cap decisions. |
| `audit-event-log` | outbound (async events) | Wallet lifecycle, derivation, balance, withdrawal, funding events. |
| `transaction-orchestrator` | inbound (gRPC) | Withdrawal construction on the spend path of the saga. |

## Dependencies

- **PostgreSQL** — authoritative wallet, address, balance, UTXO, nonce, and
  withdrawal state.
- **Redis** — nonce lock, derivation index cache, idempotency keys.
- **blockchain-gateway** — confirmation/reorg events and broadcast of
  withdrawal transactions.
- **mpc-signing-service** — signs withdrawal transactions using the wallet's
  bound `key_id`.
- **treasury-orchestration** — funds hot wallets and reconciles settlement.
- **audit-event-log** — durable record of all wallet/withdrawal events.
- **policy-risk-engine** — whitelist and risk gating for withdrawals.

## Configuration

Environment variables:

| Variable | Description | Example |
|---|---|---|
| `PORT` | HTTP REST listen port. | `8080` |
| `GRPC_PORT` | gRPC internal listen port. | `9090` |
| `DB_URL` | PostgreSQL DSN. | `postgres://wallet:***@db:5432/wallet?sslmode=verify-full` |
| `REDIS_URL` | Redis address for nonce lock + cache. | `redis://redis:6379/0` |
| `DEFAULT_ADDRESS_ROTATION_DAYS` | Default cadence for receive address rotation. | `7` |
| `CONFIRMATIONS_REQUIRED_EVM` | EVM confirmation depth threshold. | `12` |
| `CONFIRMATIONS_REQUIRED_BTC` | BTC confirmation depth threshold. | `6` |
| `CONFIRMATIONS_REQUIRED_SOL` | Solana confirmation (finalized slot). | `finalized` |
| `MPC_SIGNING_URL` | gRPC endpoint of the MPC Signing Service. | `dns:///mpc-signing:9090` |
| `BLOCKCHAIN_GATEWAY_URL` | gRPC endpoint of the Blockchain Gateway. | `dns:///blockchain-gateway:9090` |
| `TREASURY_ORCHESTRATION_URL` | REST endpoint for funding requests. | `http://treasury-orchestration:8080` |
| `POLICY_RISK_ENGINE_URL` | REST endpoint for whitelist checks. | `http://policy-risk-engine:8080` |
| `AUDIT_EVENT_LOG_URL` | REST/eventbus endpoint for audit events. | `http://audit-event-log:8080` |
| `HOT_WALLET_MIN_BALANCE_USD` | Threshold below which a funding request fires. | `50000` |
| `DERIVATION_CACHE_TTL` | TTL for derived public-key cache in Redis. | `300s` |
| `LOG_LEVEL` | Structured log level. | `info` |

## Local Development

```bash
# Build
go build ./...

# Run (requires PostgreSQL + Redis reachable)
go run ./cmd/wallet-management

# Test
go test ./... -race -cover

# Lint
golangci-lint run

# Generate protos
buf generate
```