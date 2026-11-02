-- +goose Up
-- Watch assertions are independent observations, never blocking native work or
-- replacing an action's authoritative Protect/base verifier.
CREATE TABLE contract_assertions (
 tenant_id UUID NOT NULL, action_id TEXT NOT NULL, contract_id TEXT NOT NULL, rule_id TEXT NOT NULL,
 verifier_id TEXT NOT NULL, expected JSONB NOT NULL, within_seconds INT NOT NULL,
 status TEXT NOT NULL DEFAULT 'PENDING', observed_ciphertext BYTEA, reason TEXT NOT NULL DEFAULT '',
 attempts INT NOT NULL DEFAULT 0, next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 claim_token TEXT, claim_until TIMESTAMPTZ, updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 PRIMARY KEY(tenant_id,action_id,contract_id,rule_id),
 FOREIGN KEY(tenant_id,action_id,contract_id,rule_id) REFERENCES action_contract_rules(tenant_id,action_id,contract_id,rule_id) ON DELETE CASCADE,
 FOREIGN KEY(tenant_id,verifier_id) REFERENCES verifiers(tenant_id,id) ON DELETE CASCADE
);
CREATE TABLE contract_assertion_receipts (
 tenant_id UUID NOT NULL, action_id TEXT NOT NULL, contract_id TEXT NOT NULL, rule_id TEXT NOT NULL,
 id TEXT NOT NULL, input_hash TEXT NOT NULL, PRIMARY KEY(tenant_id,action_id,contract_id,rule_id,id),
 FOREIGN KEY(tenant_id,action_id,contract_id,rule_id) REFERENCES contract_assertions(tenant_id,action_id,contract_id,rule_id) ON DELETE CASCADE
);
CREATE INDEX contract_assertions_ready ON contract_assertions(next_attempt_at) WHERE status IN('PENDING','UNKNOWN');
ALTER TABLE actions ADD COLUMN base_verification JSONB;
ALTER TABLE approvals ADD COLUMN contract_requirements JSONB NOT NULL DEFAULT '[]';
-- +goose Down
ALTER TABLE actions DROP COLUMN base_verification;
ALTER TABLE approvals DROP COLUMN contract_requirements;
DROP TABLE contract_assertion_receipts,contract_assertions;
