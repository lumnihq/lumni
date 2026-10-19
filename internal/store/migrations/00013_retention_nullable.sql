-- +goose Up
ALTER TABLE runtime_signals ALTER COLUMN payload_ciphertext DROP NOT NULL;
-- +goose Down
-- Expired payloads must not be fabricated when rolling back retention support.
SELECT 1;
