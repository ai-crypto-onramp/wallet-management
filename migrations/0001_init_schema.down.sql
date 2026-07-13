-- 0001_init_schema.down.sql
DROP TABLE IF EXISTS balance_events;
DROP TABLE IF EXISTS audit_outbox;
DROP TABLE IF EXISTS funding_requests;
DROP TABLE IF EXISTS key_mappings;
DROP TABLE IF EXISTS withdrawal_requests;
DROP TABLE IF EXISTS nonces;
DROP TABLE IF EXISTS utxos;
DROP TABLE IF EXISTS balances;
DROP TABLE IF EXISTS addresses;
DROP TABLE IF EXISTS wallets;