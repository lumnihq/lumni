-- +goose Up
ALTER TABLE action_results ADD COLUMN result_identity_hash TEXT;
ALTER TABLE durable_waits ADD COLUMN last_checked_at TIMESTAMPTZ;
ALTER TABLE runtime_signals ADD COLUMN consumed_by TEXT;
ALTER TABLE runtime_signals ALTER COLUMN payload_ciphertext DROP NOT NULL;
CREATE INDEX runtime_signal_delivery ON runtime_signals(tenant_id,work_id,name,created_at) WHERE consumed_by IS NULL;
-- +goose Down
DROP INDEX runtime_signal_delivery;
ALTER TABLE runtime_signals DROP COLUMN consumed_by;
ALTER TABLE durable_waits DROP COLUMN last_checked_at;
ALTER TABLE action_results DROP COLUMN result_identity_hash;
