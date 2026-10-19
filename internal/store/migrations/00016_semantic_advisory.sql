-- +goose Up
CREATE TABLE semantic_jobs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    kind TEXT NOT NULL CHECK (kind IN ('DUPLICATE_WORK','RISK_REVIEW','TRACE_TRIAGE')),
    subject_type TEXT NOT NULL CHECK (subject_type IN ('work','action')),
    subject_id TEXT NOT NULL,
    question_set_version TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'PENDING' CHECK (status IN ('PENDING','RUNNING','COMPLETED','FAILED')),
    attempts INT NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    claim_until TIMESTAMPTZ NULL,
    last_error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    completed_at TIMESTAMPTZ NULL,
    UNIQUE (tenant_id, kind, subject_type, subject_id, question_set_version)
);
CREATE INDEX semantic_jobs_ready_idx ON semantic_jobs (next_attempt_at, created_at)
    WHERE status = 'PENDING';

CREATE TABLE semantic_evaluations (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    job_id UUID NOT NULL UNIQUE REFERENCES semantic_jobs(id) ON DELETE CASCADE,
    purpose TEXT NOT NULL,
    work_id TEXT NULL,
    action_id TEXT NULL,
    provider TEXT NOT NULL,
    requested_model TEXT NOT NULL,
    returned_model TEXT NOT NULL DEFAULT '',
    question_set_version TEXT NOT NULL,
    input_hash TEXT NOT NULL,
    answers JSONB NOT NULL DEFAULT '{}'::jsonb,
    usage JSONB NOT NULL DEFAULT '{}'::jsonb,
    cost_micros_usd BIGINT NOT NULL DEFAULT 0,
    latency_ms BIGINT NOT NULL DEFAULT 0,
    status TEXT NOT NULL CHECK (status IN ('SUCCEEDED','FAILED')),
    error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    completed_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    FOREIGN KEY (tenant_id, work_id) REFERENCES work(tenant_id, id),
    FOREIGN KEY (tenant_id, action_id) REFERENCES actions(tenant_id, id)
);
CREATE INDEX semantic_evaluations_subject_idx ON semantic_evaluations
    (tenant_id, work_id, action_id, created_at DESC);

-- +goose Down
DROP TABLE semantic_evaluations;
DROP TABLE semantic_jobs;
