-- +goose Up
CREATE TABLE runtime_receipts (
 tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
 work_id TEXT NOT NULL, identity TEXT NOT NULL, owner_hash BYTEA NOT NULL,
 input_hash TEXT NOT NULL, response_ciphertext BYTEA,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 PRIMARY KEY(tenant_id,work_id,identity),
 FOREIGN KEY(tenant_id,work_id) REFERENCES work(tenant_id,id)
);
-- +goose Down
DROP TABLE runtime_receipts;
