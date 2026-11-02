-- +goose Up
CREATE TABLE contracts (
 tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE, id TEXT NOT NULL,
 title TEXT NOT NULL, text TEXT NOT NULL, rules JSONB NOT NULL, revision INT NOT NULL DEFAULT 1,
 mode TEXT NOT NULL DEFAULT 'draft' CHECK(mode IN('draft','watch','protect','paused')),
 was TEXT NOT NULL DEFAULT 'watch' CHECK(was IN('watch','protect')), deleted_at TIMESTAMPTZ,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(), updated_at TIMESTAMPTZ NOT NULL DEFAULT now(), PRIMARY KEY(tenant_id,id)
);
CREATE TABLE contract_revisions (
 tenant_id UUID NOT NULL, contract_id TEXT NOT NULL, revision INT NOT NULL, document JSONB NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(), PRIMARY KEY(tenant_id,contract_id,revision),
 FOREIGN KEY(tenant_id,contract_id) REFERENCES contracts(tenant_id,id) ON DELETE CASCADE
);
CREATE TABLE tool_bindings (
 tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE, id TEXT NOT NULL, version INT NOT NULL,
 document JSONB NOT NULL, secret_ciphertext BYTEA, enabled BOOLEAN NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(), updated_at TIMESTAMPTZ NOT NULL DEFAULT now(), PRIMARY KEY(tenant_id,id)
);
CREATE TABLE tool_binding_versions (
 tenant_id UUID NOT NULL, binding_id TEXT NOT NULL, version INT NOT NULL, document JSONB NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(), PRIMARY KEY(tenant_id,binding_id,version),
 FOREIGN KEY(tenant_id,binding_id) REFERENCES tool_bindings(tenant_id,id) ON DELETE CASCADE
);
CREATE TABLE integration_read_grants (
 tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE, id TEXT NOT NULL, app TEXT NOT NULL,
 binding_id TEXT NOT NULL, binding_version INT NOT NULL, objects JSONB NOT NULL, provenance TEXT NOT NULL DEFAULT 'operator_configured',
 revoked_at TIMESTAMPTZ, created_at TIMESTAMPTZ NOT NULL DEFAULT now(), PRIMARY KEY(tenant_id,id),
 FOREIGN KEY(tenant_id,binding_id,binding_version) REFERENCES tool_binding_versions(tenant_id,binding_id,version)
);
CREATE TABLE action_contract_rules (
 tenant_id UUID NOT NULL, action_id TEXT NOT NULL, contract_id TEXT NOT NULL, revision INT NOT NULL, rule_id TEXT NOT NULL,
 rule JSONB NOT NULL, mode TEXT NOT NULL, key TEXT NOT NULL DEFAULT '', decision TEXT NOT NULL DEFAULT 'ALLOW',
 reason TEXT NOT NULL DEFAULT '', approval_id UUID, facts_ciphertext BYTEA, fact_hash TEXT NOT NULL DEFAULT '',
 evaluated_at TIMESTAMPTZ NOT NULL DEFAULT now(), PRIMARY KEY(tenant_id,action_id,contract_id,rule_id),
 FOREIGN KEY(tenant_id,action_id) REFERENCES actions(tenant_id,id) ON DELETE CASCADE,
 FOREIGN KEY(tenant_id,contract_id,revision) REFERENCES contract_revisions(tenant_id,contract_id,revision) ON DELETE CASCADE
);
CREATE INDEX contract_activity ON action_contract_rules(tenant_id,contract_id,evaluated_at DESC,action_id DESC);
CREATE TABLE contract_key_claims (
 tenant_id UUID NOT NULL, contract_id TEXT NOT NULL, rule_id TEXT NOT NULL, key TEXT NOT NULL, action_id TEXT NOT NULL,
 amount BIGINT NOT NULL DEFAULT 0, admitted_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 PRIMARY KEY(tenant_id,contract_id,rule_id,key,action_id),
 FOREIGN KEY(tenant_id,contract_id) REFERENCES contracts(tenant_id,id) ON DELETE CASCADE,
 FOREIGN KEY(tenant_id,action_id) REFERENCES actions(tenant_id,id) ON DELETE CASCADE
);
CREATE TABLE contract_verification_deadlines (
 tenant_id UUID NOT NULL, action_id TEXT NOT NULL, verifier_id TEXT NOT NULL, within_seconds INT NOT NULL CHECK(within_seconds>0), verified_at TIMESTAMPTZ,
 PRIMARY KEY(tenant_id,action_id,verifier_id), FOREIGN KEY(tenant_id,action_id) REFERENCES actions(tenant_id,id) ON DELETE CASCADE
);
CREATE TABLE path_traces (
 tenant_id UUID NOT NULL, id TEXT NOT NULL, work_id TEXT NOT NULL, actor_id TEXT NOT NULL, operation TEXT NOT NULL,
 handler_version TEXT NOT NULL, signature TEXT NOT NULL, metadata JSONB NOT NULL, payload_ciphertext BYTEA,
 payload_hash TEXT NOT NULL, input_hash TEXT NOT NULL, completed_at TIMESTAMPTZ NOT NULL, created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 PRIMARY KEY(tenant_id,id), FOREIGN KEY(tenant_id,work_id) REFERENCES work(tenant_id,id) ON DELETE CASCADE,
 FOREIGN KEY(tenant_id,actor_id) REFERENCES actors(tenant_id,id) ON DELETE CASCADE
);
CREATE INDEX path_trace_discovery ON path_traces(tenant_id,signature,completed_at DESC,id);
CREATE INDEX path_trace_logical ON path_traces(tenant_id,(metadata->>'logical_signature'),completed_at DESC,id);
CREATE TABLE compiled_paths (
 tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE, id TEXT NOT NULL, name TEXT NOT NULL,
 actor_id TEXT NOT NULL, operation TEXT NOT NULL, signature TEXT NOT NULL, revision INT NOT NULL DEFAULT 1,
 paused BOOLEAN NOT NULL DEFAULT false, paused_by TEXT, pause_reason TEXT, paused_at TIMESTAMPTZ,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(), updated_at TIMESTAMPTZ NOT NULL DEFAULT now(), PRIMARY KEY(tenant_id,id),
 UNIQUE(tenant_id,signature), FOREIGN KEY(tenant_id,actor_id) REFERENCES actors(tenant_id,id) ON DELETE CASCADE
);
CREATE TABLE compiled_versions (
 tenant_id UUID NOT NULL, path_id TEXT NOT NULL, v INT NOT NULL,
 status TEXT NOT NULL CHECK(status IN('candidate','shadow','live','retired','rolled-back')),
 program JSONB NOT NULL, program_hash TEXT NOT NULL, bindings JSONB NOT NULL, synthesis_ids TEXT[] NOT NULL,
 replay_ids TEXT[] NOT NULL, replay_complete BOOLEAN NOT NULL DEFAULT false, note TEXT NOT NULL DEFAULT '',
 promoted_by TEXT, created_at TIMESTAMPTZ NOT NULL DEFAULT now(), PRIMARY KEY(tenant_id,path_id,v),
 FOREIGN KEY(tenant_id,path_id) REFERENCES compiled_paths(tenant_id,id) ON DELETE CASCADE
);
CREATE UNIQUE INDEX one_live_path ON compiled_versions(tenant_id,path_id) WHERE status='live';
CREATE UNIQUE INDEX one_pending_path ON compiled_versions(tenant_id,path_id) WHERE status IN('candidate','shadow');
CREATE TABLE compiled_replay_cases (
 tenant_id UUID NOT NULL, path_id TEXT NOT NULL, version INT NOT NULL, id TEXT NOT NULL, trace_id TEXT NOT NULL,
 kind TEXT NOT NULL CHECK(kind IN('replay','shadow','regression')), result TEXT NOT NULL CHECK(result IN('agree','disagree','out')),
 output_ciphertext BYTEA, label TEXT CHECK(label IN('llm','code')), reviewed_by TEXT,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(), PRIMARY KEY(tenant_id,id), UNIQUE(tenant_id,path_id,version,trace_id,kind),
 FOREIGN KEY(tenant_id,path_id,version) REFERENCES compiled_versions(tenant_id,path_id,v) ON DELETE CASCADE,
 FOREIGN KEY(tenant_id,trace_id) REFERENCES path_traces(tenant_id,id) ON DELETE CASCADE
);
CREATE TABLE compiled_drift (
 tenant_id UUID NOT NULL, path_id TEXT NOT NULL, id TEXT NOT NULL, kind TEXT NOT NULL,
 binding_id TEXT NOT NULL DEFAULT '', detail TEXT NOT NULL, open BOOLEAN NOT NULL DEFAULT true, fixed_by INT,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(), PRIMARY KEY(tenant_id,path_id,id),
 FOREIGN KEY(tenant_id,path_id) REFERENCES compiled_paths(tenant_id,id) ON DELETE CASCADE
);
CREATE TABLE automation_jobs (
 tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE, id TEXT NOT NULL, kind TEXT NOT NULL, subject_id TEXT NOT NULL,
 status TEXT NOT NULL DEFAULT 'PENDING' CHECK(status IN('PENDING','RUNNING','COMPLETED','FAILED')),
 input JSONB NOT NULL DEFAULT '{}', result JSONB, error TEXT, by TEXT NOT NULL, attempts INT NOT NULL DEFAULT 0,
 claim_token TEXT, claim_until TIMESTAMPTZ, created_at TIMESTAMPTZ NOT NULL DEFAULT now(), updated_at TIMESTAMPTZ NOT NULL DEFAULT now(), PRIMARY KEY(tenant_id,id)
);
CREATE UNIQUE INDEX one_path_compile_job ON automation_jobs(tenant_id,subject_id) WHERE kind IN('compile','recompile') AND status IN('PENDING','RUNNING');
CREATE TABLE compiled_runs (
 tenant_id UUID NOT NULL, id TEXT NOT NULL, path_id TEXT NOT NULL, version INT, actor_id TEXT NOT NULL, work_id TEXT,
 input_hash TEXT NOT NULL, request_hash TEXT NOT NULL DEFAULT '', input_ciphertext BYTEA, output_ciphertext BYTEA,
 status TEXT NOT NULL DEFAULT 'PENDING', served_by TEXT NOT NULL DEFAULT 'code', reason TEXT,
 duration_ms BIGINT, cost_micros_usd BIGINT, reported BOOLEAN NOT NULL DEFAULT false,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(), completed_at TIMESTAMPTZ, PRIMARY KEY(tenant_id,id),
 FOREIGN KEY(tenant_id,path_id) REFERENCES compiled_paths(tenant_id,id),
 FOREIGN KEY(tenant_id,path_id,version) REFERENCES compiled_versions(tenant_id,path_id,v),
 FOREIGN KEY(tenant_id,actor_id) REFERENCES actors(tenant_id,id) ON DELETE CASCADE, FOREIGN KEY(tenant_id,work_id) REFERENCES work(tenant_id,id) ON DELETE CASCADE
);
CREATE INDEX compiled_run_stats ON compiled_runs(tenant_id,path_id,created_at DESC,id DESC);
ALTER TABLE compiled_versions ADD COLUMN compilation_job TEXT;
CREATE UNIQUE INDEX version_compilation_job ON compiled_versions(tenant_id,compilation_job);
ALTER TABLE semantic_jobs DROP CONSTRAINT semantic_jobs_kind_check, DROP CONSTRAINT semantic_jobs_subject_type_check;
ALTER TABLE semantic_jobs ADD CHECK(kind IN('DUPLICATE_WORK','RISK_REVIEW','TRACE_TRIAGE','CONTRACT_CLASSIFY')),
 ADD CHECK(subject_type IN('work','action','contract'));
-- +goose StatementBegin
CREATE FUNCTION automation_immutable_revision() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 RAISE EXCEPTION 'automation revision is immutable';
END $$;
CREATE FUNCTION automation_immutable_program() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF ROW(NEW.program,NEW.program_hash,NEW.bindings,NEW.synthesis_ids,NEW.replay_ids) IS DISTINCT FROM
    ROW(OLD.program,OLD.program_hash,OLD.bindings,OLD.synthesis_ids,OLD.replay_ids) THEN
  RAISE EXCEPTION 'compiled artifact is immutable';
 END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER immutable_contract_revision BEFORE UPDATE ON contract_revisions FOR EACH ROW EXECUTE FUNCTION automation_immutable_revision();
CREATE TRIGGER immutable_binding_revision BEFORE UPDATE ON tool_binding_versions FOR EACH ROW EXECUTE FUNCTION automation_immutable_revision();
CREATE TRIGGER immutable_compiled_artifact BEFORE UPDATE ON compiled_versions FOR EACH ROW EXECUTE FUNCTION automation_immutable_program();
-- +goose Down
DROP FUNCTION automation_immutable_revision() CASCADE;
DROP FUNCTION automation_immutable_program() CASCADE;
DELETE FROM semantic_jobs WHERE kind='CONTRACT_CLASSIFY';
ALTER TABLE semantic_jobs DROP CONSTRAINT semantic_jobs_kind_check, DROP CONSTRAINT semantic_jobs_subject_type_check;
ALTER TABLE semantic_jobs ADD CHECK(kind IN('DUPLICATE_WORK','RISK_REVIEW','TRACE_TRIAGE')), ADD CHECK(subject_type IN('work','action'));

DROP TABLE compiled_runs,automation_jobs,compiled_drift,compiled_replay_cases,compiled_versions,compiled_paths,path_traces,
 contract_verification_deadlines,contract_key_claims,action_contract_rules,integration_read_grants,tool_binding_versions,tool_bindings,contract_revisions,contracts;
