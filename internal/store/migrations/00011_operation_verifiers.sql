-- +goose Up
CREATE TABLE operation_verifiers(
 tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,operation TEXT NOT NULL,verifier_id TEXT NOT NULL,
 expected_template JSONB NOT NULL,updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 PRIMARY KEY(tenant_id,operation),FOREIGN KEY(tenant_id,verifier_id)REFERENCES verifiers(tenant_id,id)
);
ALTER TABLE work ADD COLUMN expected_effects JSONB NOT NULL DEFAULT '{}';
CREATE TABLE effect_aliases(
 tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,legacy_key TEXT NOT NULL,current_key TEXT NOT NULL,
 evidence JSONB NOT NULL,created_at TIMESTAMPTZ NOT NULL DEFAULT now(),PRIMARY KEY(tenant_id,legacy_key)
);
-- +goose Down
DROP TABLE effect_aliases,operation_verifiers;
ALTER TABLE work DROP COLUMN expected_effects;
