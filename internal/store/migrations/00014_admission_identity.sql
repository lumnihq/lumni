-- +goose Up
ALTER TABLE actions ADD COLUMN admission_identity_hash TEXT;
ALTER TABLE actions ADD COLUMN idempotency_identity_hash TEXT;
-- Historical identities remain nullable: do not infer equivalence from resource overlap.
-- +goose Down
ALTER TABLE actions DROP COLUMN admission_identity_hash, DROP COLUMN idempotency_identity_hash;
