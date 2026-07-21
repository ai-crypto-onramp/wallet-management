-- 0002_withdrawal_tx_columns.up.sql
-- Adds reserved_outpoints and signed_tx_bytes to withdrawal_requests so the
-- service can detect double-spends after a restart and broadcast the real
-- signed transaction bytes (P1.7 remediation).
ALTER TABLE withdrawal_requests ADD COLUMN IF NOT EXISTS reserved_outpoints TEXT[] NOT NULL DEFAULT '{}';
ALTER TABLE withdrawal_requests ADD COLUMN IF NOT EXISTS signed_tx_bytes BYTEA NULL;