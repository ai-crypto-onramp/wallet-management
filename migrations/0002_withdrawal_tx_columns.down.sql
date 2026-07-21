-- 0002_withdrawal_tx_columns.down.sql
ALTER TABLE withdrawal_requests DROP COLUMN IF EXISTS signed_tx_bytes;
ALTER TABLE withdrawal_requests DROP COLUMN IF EXISTS reserved_outpoints;