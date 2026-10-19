-- +goose Up
ALTER TABLE work ADD COLUMN execution_mode TEXT NOT NULL DEFAULT 'EXTERNAL';
ALTER TABLE work ADD COLUMN handler TEXT NOT NULL DEFAULT '';
ALTER TABLE work ADD COLUMN handler_version TEXT NOT NULL DEFAULT '';
ALTER TABLE work ADD COLUMN input_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE work ADD COLUMN input_ciphertext BYTEA;
ALTER TABLE work ADD COLUMN output_ciphertext BYTEA;
ALTER TABLE work ADD COLUMN claim_hash BYTEA;
ALTER TABLE work ADD COLUMN claim_fence BIGINT NOT NULL DEFAULT 0;
ALTER TABLE work ADD COLUMN claim_expires_at TIMESTAMPTZ;
ALTER TABLE work ADD COLUMN resume_at TIMESTAMPTZ NOT NULL DEFAULT now();
ALTER TABLE work ADD COLUMN runtime_error TEXT NOT NULL DEFAULT '';
ALTER TABLE work ADD COLUMN handle_child_failure BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE work ADD COLUMN payload_expires_at TIMESTAMPTZ;
CREATE INDEX work_ready_idx ON work(status,resume_at) WHERE execution_mode='NATIVE';

CREATE TABLE steps (
 tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE, id TEXT NOT NULL, work_id TEXT NOT NULL, step_key TEXT NOT NULL,
 type TEXT NOT NULL CHECK(type IN ('LLM','TOOL','CUSTOM','WAIT','APPROVAL','AGENT')),
 name TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'PENDING',
 input_hash TEXT NOT NULL, input_ciphertext BYTEA, output_hash TEXT NOT NULL DEFAULT '', output_ciphertext BYTEA,
 attempt INT NOT NULL DEFAULT 0, retry_policy JSONB NOT NULL DEFAULT '{}',
 action_id TEXT, admission_ciphertext BYTEA, error TEXT NOT NULL DEFAULT '', created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT now(), completed_at TIMESTAMPTZ,
 PRIMARY KEY(tenant_id,id), UNIQUE(tenant_id,work_id,step_key),
 FOREIGN KEY(tenant_id,work_id) REFERENCES work(tenant_id,id),
 FOREIGN KEY(tenant_id,action_id) REFERENCES actions(tenant_id,id)
);
CREATE TABLE step_attempts (
 tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE, step_id TEXT NOT NULL, attempt INT NOT NULL, claim_fence BIGINT NOT NULL,
 status TEXT NOT NULL, started_at TIMESTAMPTZ NOT NULL DEFAULT now(), finished_at TIMESTAMPTZ,
 result_hash TEXT, error TEXT NOT NULL DEFAULT '',
 PRIMARY KEY(tenant_id,step_id,attempt), FOREIGN KEY(tenant_id,step_id) REFERENCES steps(tenant_id,id)
);
ALTER TABLE actions ADD COLUMN step_id TEXT;
ALTER TABLE actions ADD COLUMN arguments_ciphertext BYTEA;
ALTER TABLE actions ADD COLUMN verification JSONB;
ALTER TABLE actions ADD COLUMN effect_identity_version TEXT NOT NULL DEFAULT 'legacy';
ALTER TABLE action_results ADD COLUMN output_ciphertext BYTEA;
ALTER TABLE action_results ADD COLUMN output_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE actions ADD CONSTRAINT actions_step_fk FOREIGN KEY(tenant_id,step_id) REFERENCES steps(tenant_id,id);
ALTER TABLE observations ADD COLUMN step_id TEXT;
ALTER TABLE observations ADD CONSTRAINT observations_step_fk FOREIGN KEY(tenant_id,step_id) REFERENCES steps(tenant_id,id);
ALTER TABLE costs ADD COLUMN step_id TEXT;
ALTER TABLE costs ADD CONSTRAINT costs_step_fk FOREIGN KEY(tenant_id,step_id) REFERENCES steps(tenant_id,id);

CREATE TABLE work_journal (
 sequence BIGSERIAL PRIMARY KEY, tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE, work_id TEXT NOT NULL, step_id TEXT,
 type TEXT NOT NULL, identity TEXT NOT NULL, payload JSONB NOT NULL DEFAULT '{}', created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 UNIQUE(tenant_id,work_id,identity), FOREIGN KEY(tenant_id,work_id) REFERENCES work(tenant_id,id)
);
CREATE INDEX journal_work_idx ON work_journal(tenant_id,work_id,sequence);
CREATE TABLE durable_waits (
 tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE, step_id TEXT NOT NULL, work_id TEXT NOT NULL, type TEXT NOT NULL,
 condition JSONB NOT NULL, wake_at TIMESTAMPTZ, status TEXT NOT NULL DEFAULT 'WAITING',
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(), resolved_at TIMESTAMPTZ,
 PRIMARY KEY(tenant_id,step_id), FOREIGN KEY(tenant_id,step_id) REFERENCES steps(tenant_id,id),
 FOREIGN KEY(tenant_id,work_id) REFERENCES work(tenant_id,id)
);
CREATE TABLE runtime_signals (
 tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE, work_id TEXT NOT NULL, id TEXT NOT NULL, name TEXT NOT NULL,
 payload_ciphertext BYTEA NOT NULL, payload_hash TEXT NOT NULL, created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 PRIMARY KEY(tenant_id,work_id,id), FOREIGN KEY(tenant_id,work_id) REFERENCES work(tenant_id,id)
);
CREATE TABLE verifiers (
 tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE, id TEXT NOT NULL, name TEXT NOT NULL,
 kind TEXT NOT NULL, version TEXT NOT NULL, config JSONB NOT NULL DEFAULT '{}', secret_ciphertext BYTEA,
 enabled BOOLEAN NOT NULL DEFAULT true, created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 PRIMARY KEY(tenant_id,id)
);
CREATE TABLE outcomes (
 tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE, id TEXT NOT NULL, action_id TEXT NOT NULL, work_id TEXT NOT NULL,
 verifier_id TEXT, verifier_version TEXT NOT NULL DEFAULT '', expected JSONB NOT NULL DEFAULT '{}',
 status TEXT NOT NULL DEFAULT 'UNKNOWN', observed JSONB NOT NULL DEFAULT '{}',
 effect_absent BOOLEAN NOT NULL DEFAULT false, prior_attempt_terminated BOOLEAN NOT NULL DEFAULT false,
 attempts INT NOT NULL DEFAULT 0, next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 claim_until TIMESTAMPTZ, claim_token TEXT, reason TEXT NOT NULL DEFAULT '',
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(), updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 PRIMARY KEY(tenant_id,id), UNIQUE(tenant_id,action_id),
 FOREIGN KEY(tenant_id,action_id) REFERENCES actions(tenant_id,id),
 FOREIGN KEY(tenant_id,work_id) REFERENCES work(tenant_id,id),
 FOREIGN KEY(tenant_id,verifier_id) REFERENCES verifiers(tenant_id,id)
);
CREATE TABLE verification_attempts (
 tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE, outcome_id TEXT NOT NULL, id TEXT NOT NULL, evidence JSONB NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(), PRIMARY KEY(tenant_id,outcome_id,id),
 FOREIGN KEY(tenant_id,outcome_id) REFERENCES outcomes(tenant_id,id)
);
CREATE TABLE effect_occurrences (
 tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE, id TEXT NOT NULL, action_id TEXT NOT NULL, effect_key TEXT NOT NULL,
 status TEXT NOT NULL, external_id TEXT, evidence_type TEXT NOT NULL DEFAULT 'TOOL_RESULT',
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(), PRIMARY KEY(tenant_id,id), UNIQUE(tenant_id,action_id),
 FOREIGN KEY(tenant_id,action_id) REFERENCES actions(tenant_id,id)
);
INSERT INTO effect_occurrences(tenant_id,id,action_id,effect_key,status,external_id)
 SELECT tenant_id,id,action_id,effect_key,status,external_id FROM effects;
CREATE TABLE effect_holds (
 tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE, action_id TEXT NOT NULL, reason TEXT NOT NULL, created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 PRIMARY KEY(tenant_id,action_id), FOREIGN KEY(tenant_id,action_id) REFERENCES actions(tenant_id,id)
);
INSERT INTO effect_holds(tenant_id,action_id,reason) SELECT tenant_id,id,'UNKNOWN_EFFECT' FROM actions WHERE status='UNKNOWN';
CREATE TABLE action_rechecks (
 tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE, action_id TEXT NOT NULL, id TEXT NOT NULL, receipt_id UUID,
 PRIMARY KEY(tenant_id,action_id,id), FOREIGN KEY(tenant_id,action_id) REFERENCES actions(tenant_id,id)
);
CREATE TABLE dependencies (
 tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE, id TEXT NOT NULL, entity_type TEXT NOT NULL CHECK(entity_type IN ('work','action')),
 source_id TEXT NOT NULL, target_id TEXT NOT NULL, relationship TEXT NOT NULL CHECK(relationship IN ('DEPENDS_ON','SUPERSEDES')),
 PRIMARY KEY(tenant_id,id), UNIQUE(tenant_id,entity_type,source_id,target_id,relationship), CHECK(source_id<>target_id)
);
CREATE TABLE enforcement_rules (
 tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE, operation TEXT NOT NULL, rule TEXT NOT NULL,
 mode TEXT NOT NULL CHECK(mode IN ('SHADOW','WARN','ENFORCE')), PRIMARY KEY(tenant_id,operation,rule)
);
CREATE TABLE incidents (
 tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE, id TEXT NOT NULL, kind TEXT NOT NULL, work_id TEXT,
 action_id TEXT, status TEXT NOT NULL DEFAULT 'OPEN', title TEXT NOT NULL, evidence JSONB NOT NULL DEFAULT '{}',
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(), updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 PRIMARY KEY(tenant_id,id)
);
CREATE TABLE reconciliation_handlers (
 tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE, name TEXT NOT NULL, version TEXT NOT NULL,
 PRIMARY KEY(tenant_id,name,version)
);
CREATE TABLE reconciliation_jobs (
 tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE, id TEXT NOT NULL, outcome_id TEXT NOT NULL, work_id TEXT NOT NULL,
 handler TEXT NOT NULL, handler_version TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'PENDING',
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(), PRIMARY KEY(tenant_id,id),
 FOREIGN KEY(tenant_id,outcome_id) REFERENCES outcomes(tenant_id,id),
 FOREIGN KEY(tenant_id,work_id) REFERENCES work(tenant_id,id)
);
CREATE TABLE discovered_tools (
 tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE, source TEXT NOT NULL, name TEXT NOT NULL,
 schema JSONB NOT NULL, proposal JSONB NOT NULL DEFAULT '{}', confirmed BOOLEAN NOT NULL DEFAULT false,
 last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(), PRIMARY KEY(tenant_id,source,name)
);

-- +goose Down
DROP TABLE discovered_tools, reconciliation_jobs, reconciliation_handlers, incidents, enforcement_rules, dependencies, action_rechecks, effect_holds, effect_occurrences, verification_attempts, outcomes, verifiers, runtime_signals, durable_waits, work_journal;
ALTER TABLE costs DROP COLUMN step_id;
ALTER TABLE observations DROP COLUMN step_id;
ALTER TABLE actions DROP COLUMN step_id, DROP COLUMN arguments_ciphertext, DROP COLUMN verification, DROP COLUMN effect_identity_version;
ALTER TABLE action_results DROP COLUMN output_ciphertext, DROP COLUMN output_hash;
DROP TABLE step_attempts, steps;
ALTER TABLE work DROP COLUMN execution_mode, DROP COLUMN handler, DROP COLUMN handler_version, DROP COLUMN input_hash,
 DROP COLUMN input_ciphertext, DROP COLUMN output_ciphertext, DROP COLUMN claim_hash, DROP COLUMN claim_fence,
 DROP COLUMN claim_expires_at, DROP COLUMN resume_at, DROP COLUMN runtime_error, DROP COLUMN handle_child_failure, DROP COLUMN payload_expires_at;
