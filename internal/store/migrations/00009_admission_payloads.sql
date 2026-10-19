-- +goose Up
CREATE UNIQUE INDEX decisions_tenant_id_idx ON decisions(tenant_id,id);
CREATE TABLE admission_payloads(
 tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
 receipt_id UUID NOT NULL, response_ciphertext BYTEA,
 PRIMARY KEY(tenant_id,receipt_id),
 FOREIGN KEY(tenant_id,receipt_id) REFERENCES decisions(tenant_id,id)
);
-- +goose Down
DROP TABLE admission_payloads;
DROP INDEX decisions_tenant_id_idx;
