-- +goose Up
CREATE TABLE model_calls(
 tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE, id TEXT NOT NULL,
 work_id TEXT NOT NULL, step_id TEXT, provider TEXT NOT NULL, model TEXT NOT NULL,
 status TEXT NOT NULL DEFAULT 'PENDING', input_hash TEXT NOT NULL, input_ciphertext BYTEA,
 output_hash TEXT NOT NULL DEFAULT '', output_ciphertext BYTEA, usage JSONB NOT NULL DEFAULT '{}',
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(), completed_at TIMESTAMPTZ,
 PRIMARY KEY(tenant_id,id),FOREIGN KEY(tenant_id,work_id)REFERENCES work(tenant_id,id),
 FOREIGN KEY(tenant_id,step_id)REFERENCES steps(tenant_id,id)
);
-- +goose Down
DROP TABLE model_calls;
