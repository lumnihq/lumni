-- +goose Up
CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE tenants (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name TEXT NOT NULL,
    slug TEXT NOT NULL UNIQUE,
    settings JSONB NOT NULL DEFAULT '{}'::jsonb,
    enforcement_mode TEXT NOT NULL DEFAULT 'SHADOW' CHECK (enforcement_mode IN ('SHADOW','WARN','ENFORCE')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);

CREATE TABLE api_keys (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    prefix TEXT NOT NULL,
    secret_hash BYTEA NOT NULL UNIQUE,
    scopes TEXT[] NOT NULL,
    expires_at TIMESTAMPTZ NULL,
    revoked_at TIMESTAMPTZ NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    last_used_at TIMESTAMPTZ NULL
);
CREATE INDEX api_keys_tenant_idx ON api_keys (tenant_id, created_at DESC);

CREATE TABLE actors (
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    id TEXT NOT NULL,
    type TEXT NOT NULL,
    runtime TEXT NOT NULL DEFAULT '',
    display_name TEXT NOT NULL DEFAULT '',
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, id)
);

CREATE TABLE agents (
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    id TEXT NOT NULL,
    actor_id TEXT NOT NULL,
    name TEXT NOT NULL,
    framework TEXT NOT NULL DEFAULT '',
    model_provider TEXT NOT NULL DEFAULT '',
    model TEXT NOT NULL DEFAULT '',
    version TEXT NOT NULL DEFAULT '',
    repository_id UUID NULL,
    deployment_id TEXT NULL,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, actor_id) REFERENCES actors(tenant_id, id)
);

CREATE TABLE sessions (
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    id TEXT NOT NULL,
    agent_id TEXT NULL,
    actor_id TEXT NOT NULL,
    runtime TEXT NOT NULL DEFAULT '',
    external_id TEXT NULL,
    status TEXT NOT NULL DEFAULT 'RUNNING',
    started_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    finished_at TIMESTAMPTZ NULL,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, actor_id) REFERENCES actors(tenant_id, id),
    FOREIGN KEY (tenant_id, agent_id) REFERENCES agents(tenant_id, id)
);

CREATE TABLE repositories (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    provider TEXT NOT NULL,
    external_id TEXT NOT NULL,
    owner TEXT NOT NULL DEFAULT '',
    name TEXT NOT NULL,
    default_branch TEXT NOT NULL DEFAULT '',
    url TEXT NULL,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (tenant_id, provider, external_id)
);

CREATE TABLE pull_requests (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    repository_id UUID NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    external_number TEXT NOT NULL,
    state TEXT NOT NULL,
    head_sha TEXT NOT NULL DEFAULT '',
    base_sha TEXT NOT NULL DEFAULT '',
    title TEXT NOT NULL DEFAULT '',
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (tenant_id, repository_id, external_number)
);

CREATE TABLE work (
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    id TEXT NOT NULL,
    actor_id TEXT NOT NULL,
    agent_id TEXT NULL,
    session_id TEXT NULL,
    parent_work_id TEXT NULL,
    goal TEXT NOT NULL,
    normalized_goal TEXT NULL,
    status TEXT NOT NULL DEFAULT 'RUNNING',
    priority INT NOT NULL DEFAULT 50,
    risk TEXT NOT NULL DEFAULT 'MEDIUM',
    repository_id UUID NULL REFERENCES repositories(id),
    issue_ref TEXT NULL,
    pr_ref TEXT NULL,
    base_commit TEXT NULL,
    started_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    completed_at TIMESTAMPTZ NULL,
    cancelled_at TIMESTAMPTZ NULL,
    superseded_at TIMESTAMPTZ NULL,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, actor_id) REFERENCES actors(tenant_id, id),
    FOREIGN KEY (tenant_id, agent_id) REFERENCES agents(tenant_id, id),
    FOREIGN KEY (tenant_id, session_id) REFERENCES sessions(tenant_id, id),
    FOREIGN KEY (tenant_id, parent_work_id) REFERENCES work(tenant_id, id)
);
CREATE INDEX work_tenant_status_idx ON work (tenant_id, status, started_at DESC);

CREATE TABLE work_edges (
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    source_work_id TEXT NOT NULL,
    relationship TEXT NOT NULL,
    target_work_id TEXT NOT NULL,
    confidence DOUBLE PRECISION NOT NULL DEFAULT 1,
    evidence_type TEXT NOT NULL DEFAULT 'EXPLICIT',
    evidence JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, source_work_id, relationship, target_work_id),
    FOREIGN KEY (tenant_id, source_work_id) REFERENCES work(tenant_id, id),
    FOREIGN KEY (tenant_id, target_work_id) REFERENCES work(tenant_id, id)
);

CREATE TABLE resources (
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    id TEXT NOT NULL,
    type TEXT NOT NULL,
    external_id TEXT NOT NULL,
    namespace TEXT NOT NULL DEFAULT 'default',
    parent_resource_id TEXT NULL,
    current_version TEXT NULL,
    version_source TEXT NULL,
    version_confidence DOUBLE PRECISION NOT NULL DEFAULT 0,
    version_observed_at TIMESTAMPTZ NULL,
    version_sequence BIGINT NULL,
    fencing_token BIGINT NOT NULL DEFAULT 0,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, id),
    UNIQUE (tenant_id, namespace, type, external_id),
    FOREIGN KEY (tenant_id, parent_resource_id) REFERENCES resources(tenant_id, id)
);
CREATE INDEX resources_identity_idx ON resources (tenant_id, type, external_id);

CREATE TABLE resource_edges (
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    source_resource_id TEXT NOT NULL,
    relationship TEXT NOT NULL,
    target_resource_id TEXT NOT NULL,
    confidence DOUBLE PRECISION NOT NULL DEFAULT 1,
    evidence_type TEXT NOT NULL DEFAULT 'EXPLICIT',
    source TEXT NOT NULL DEFAULT '',
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, source_resource_id, relationship, target_resource_id),
    FOREIGN KEY (tenant_id, source_resource_id) REFERENCES resources(tenant_id, id),
    FOREIGN KEY (tenant_id, target_resource_id) REFERENCES resources(tenant_id, id)
);

CREATE TABLE resource_versions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    resource_id TEXT NOT NULL,
    version TEXT NOT NULL,
    version_source TEXT NOT NULL,
    observed_at TIMESTAMPTZ NOT NULL,
    source TEXT NOT NULL DEFAULT '',
    source_sequence BIGINT NULL,
    confidence DOUBLE PRECISION NOT NULL DEFAULT 1,
    authoritative BOOLEAN NOT NULL DEFAULT FALSE,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    FOREIGN KEY (tenant_id, resource_id) REFERENCES resources(tenant_id, id),
    UNIQUE (tenant_id, resource_id, version_source, version, source, source_sequence)
);
CREATE INDEX resource_versions_history_idx ON resource_versions (tenant_id, resource_id, observed_at DESC);

CREATE TABLE observations (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    work_id TEXT NOT NULL,
    session_id TEXT NULL,
    resource_id TEXT NOT NULL,
    observed_version TEXT NOT NULL,
    version_source TEXT NOT NULL,
    source TEXT NOT NULL DEFAULT '',
    confidence DOUBLE PRECISION NOT NULL DEFAULT 1,
    evidence_type TEXT NOT NULL DEFAULT 'EXPLICIT',
    observed_at TIMESTAMPTZ NOT NULL,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    FOREIGN KEY (tenant_id, work_id) REFERENCES work(tenant_id, id),
    FOREIGN KEY (tenant_id, session_id) REFERENCES sessions(tenant_id, id),
    FOREIGN KEY (tenant_id, resource_id) REFERENCES resources(tenant_id, id)
);
CREATE INDEX observations_work_idx ON observations (tenant_id, work_id, resource_id, observed_at DESC);

CREATE TABLE actions (
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    id TEXT NOT NULL,
    work_id TEXT NOT NULL,
    actor_id TEXT NOT NULL,
    session_id TEXT NULL,
    operation TEXT NOT NULL,
    tool TEXT NOT NULL DEFAULT '',
    source TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'PROPOSED',
    risk TEXT NOT NULL DEFAULT 'MEDIUM',
    arguments_hash TEXT NOT NULL,
    arguments JSONB NULL,
    idempotency_key TEXT NULL,
    effect_key TEXT NULL,
    lease_token_hash BYTEA NULL,
    requested_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    started_at TIMESTAMPTZ NULL,
    finished_at TIMESTAMPTZ NULL,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, work_id) REFERENCES work(tenant_id, id),
    FOREIGN KEY (tenant_id, actor_id) REFERENCES actors(tenant_id, id),
    FOREIGN KEY (tenant_id, session_id) REFERENCES sessions(tenant_id, id)
);
CREATE INDEX actions_work_idx ON actions (tenant_id, work_id, requested_at DESC);
CREATE INDEX actions_effect_idx ON actions (tenant_id, effect_key, requested_at DESC) WHERE effect_key IS NOT NULL;
CREATE INDEX actions_active_idx ON actions (tenant_id, status, requested_at DESC);

CREATE TABLE action_resources (
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    action_id TEXT NOT NULL,
    resource_id TEXT NOT NULL,
    mode TEXT NOT NULL,
    operation TEXT NOT NULL DEFAULT '',
    observed_version TEXT NULL,
    expected_version TEXT NULL,
    version_source TEXT NOT NULL DEFAULT '',
    confidence DOUBLE PRECISION NOT NULL DEFAULT 1,
    evidence_type TEXT NOT NULL DEFAULT 'EXPLICIT',
    evidence JSONB NOT NULL DEFAULT '{}'::jsonb,
    PRIMARY KEY (tenant_id, action_id, resource_id, mode),
    FOREIGN KEY (tenant_id, action_id) REFERENCES actions(tenant_id, id) ON DELETE CASCADE,
    FOREIGN KEY (tenant_id, resource_id) REFERENCES resources(tenant_id, id)
);
CREATE INDEX action_resources_resource_idx ON action_resources (tenant_id, resource_id, mode);

CREATE TABLE idempotency_records (
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    idempotency_key TEXT NOT NULL,
    operation TEXT NOT NULL,
    payload_hash TEXT NOT NULL,
    action_id TEXT NOT NULL,
    status TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, idempotency_key),
    FOREIGN KEY (tenant_id, action_id) REFERENCES actions(tenant_id, id)
);

CREATE TABLE effects (
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    id TEXT NOT NULL,
    action_id TEXT NOT NULL,
    operation TEXT NOT NULL,
    effect_key TEXT NOT NULL,
    status TEXT NOT NULL,
    external_id TEXT NULL,
    occurred_at TIMESTAMPTZ NULL,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, id),
    UNIQUE (tenant_id, effect_key),
    FOREIGN KEY (tenant_id, action_id) REFERENCES actions(tenant_id, id)
);

CREATE TABLE leases (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    reservation_id UUID NOT NULL,
    resource_id TEXT NOT NULL,
    action_id TEXT NOT NULL,
    mode TEXT NOT NULL,
    fencing_token BIGINT NOT NULL,
    token_hash BYTEA NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    heartbeat_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    FOREIGN KEY (tenant_id, resource_id) REFERENCES resources(tenant_id, id),
    FOREIGN KEY (tenant_id, action_id) REFERENCES actions(tenant_id, id) ON DELETE CASCADE,
    UNIQUE (tenant_id, action_id, resource_id)
);
CREATE INDEX leases_resource_idx ON leases (tenant_id, resource_id, expires_at);
CREATE INDEX leases_action_idx ON leases (tenant_id, action_id);

CREATE TABLE decisions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    action_id TEXT NOT NULL,
    decision TEXT NOT NULL,
    reason_code TEXT NOT NULL,
    enforced BOOLEAN NOT NULL,
    confidence DOUBLE PRECISION NOT NULL DEFAULT 1,
    retry_after_ms BIGINT NULL,
    lease_token TEXT NULL,
    evidence JSONB NOT NULL DEFAULT '[]'::jsonb,
    snapshot JSONB NOT NULL,
    kernel_version TEXT NOT NULL,
    resolver_version TEXT NOT NULL,
    policy_version TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    FOREIGN KEY (tenant_id, action_id) REFERENCES actions(tenant_id, id)
);
CREATE INDEX decisions_action_idx ON decisions (tenant_id, action_id, created_at DESC);

CREATE TABLE action_results (
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    action_id TEXT NOT NULL,
    status TEXT NOT NULL,
    external_result_id TEXT NULL,
    retryable BOOLEAN NULL,
    resource_versions JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_resources JSONB NOT NULL DEFAULT '[]'::jsonb,
    compensation JSONB NOT NULL DEFAULT '{}'::jsonb,
    result_metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    recorded_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, action_id),
    FOREIGN KEY (tenant_id, action_id) REFERENCES actions(tenant_id, id)
);

CREATE TABLE approvals (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    action_id TEXT NOT NULL,
    policy_id UUID NULL,
    status TEXT NOT NULL DEFAULT 'PENDING',
    reason_code TEXT NOT NULL,
    requested_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    expires_at TIMESTAMPTZ NULL,
    resolved_at TIMESTAMPTZ NULL,
    resolved_by TEXT NULL,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    FOREIGN KEY (tenant_id, action_id) REFERENCES actions(tenant_id, id)
);
CREATE UNIQUE INDEX approvals_pending_action_idx ON approvals (tenant_id, action_id) WHERE status = 'PENDING';

CREATE TABLE resource_rules (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    source TEXT NOT NULL,
    tool TEXT NOT NULL,
    operation TEXT NOT NULL,
    resources JSONB NOT NULL,
    effect_key_fields JSONB NOT NULL DEFAULT '[]'::jsonb,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    version INT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (tenant_id, source, tool)
);

CREATE TABLE conflict_rules (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    left_operation TEXT NOT NULL,
    right_operation TEXT NOT NULL,
    resource_type TEXT NOT NULL DEFAULT '*',
    outcome TEXT NOT NULL CHECK (outcome IN ('ALLOW','WAIT','DEDUPLICATE')),
    reason_code TEXT NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    version INT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (tenant_id, left_operation, right_operation, resource_type)
);

CREATE TABLE policy_rules (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    operation TEXT NOT NULL DEFAULT '*',
    resource_type TEXT NOT NULL DEFAULT '*',
    minimum_risk TEXT NULL,
    argument_pointer TEXT NULL,
    numeric_threshold DOUBLE PRECISION NULL,
    outcome TEXT NOT NULL CHECK (outcome IN ('REQUIRE_APPROVAL','REJECT','WARN')),
    reason_code TEXT NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    version INT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (tenant_id, name)
);

CREATE TABLE events (
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    id TEXT NOT NULL,
    type TEXT NOT NULL,
    entity_type TEXT NOT NULL DEFAULT '',
    entity_id TEXT NOT NULL DEFAULT '',
    source TEXT NOT NULL,
    occurred_at TIMESTAMPTZ NOT NULL,
    received_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, id)
);

CREATE TABLE outbox (
    id BIGSERIAL PRIMARY KEY,
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    topic TEXT NOT NULL,
    key TEXT NOT NULL,
    payload JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    published_at TIMESTAMPTZ NULL,
    attempts INT NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX outbox_pending_idx ON outbox (next_attempt_at, id) WHERE published_at IS NULL;

CREATE TABLE webhook_endpoints (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    url TEXT NOT NULL,
    secret_ciphertext BYTEA NOT NULL,
    event_types TEXT[] NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);

CREATE TABLE webhook_deliveries (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    endpoint_id UUID NOT NULL REFERENCES webhook_endpoints(id) ON DELETE CASCADE,
    outbox_id BIGINT NOT NULL REFERENCES outbox(id) ON DELETE CASCADE,
    status TEXT NOT NULL,
    response_code INT NULL,
    attempts INT NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NULL,
    delivered_at TIMESTAMPTZ NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (endpoint_id, outbox_id)
);

CREATE TABLE costs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    actor_id TEXT NULL,
    agent_id TEXT NULL,
    session_id TEXT NULL,
    work_id TEXT NULL,
    action_id TEXT NULL,
    provider TEXT NOT NULL,
    model TEXT NULL,
    cost_type TEXT NOT NULL,
    input_units BIGINT NULL,
    output_units BIGINT NULL,
    compute_units DOUBLE PRECISION NULL,
    amount_micros_usd BIGINT NOT NULL,
    waste_reason TEXT NULL,
    observed_at TIMESTAMPTZ NOT NULL,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb
);
CREATE INDEX costs_work_idx ON costs (tenant_id, work_id, observed_at DESC);
CREATE INDEX costs_action_idx ON costs (tenant_id, action_id, observed_at DESC);

CREATE TABLE otel_spans (
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    trace_id TEXT NOT NULL,
    span_id TEXT NOT NULL,
    name TEXT NOT NULL,
    start_time TIMESTAMPTZ NOT NULL,
    end_time TIMESTAMPTZ NULL,
    attributes JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, trace_id, span_id)
);

-- +goose Down
DROP TABLE IF EXISTS otel_spans;
DROP TABLE IF EXISTS costs;
DROP TABLE IF EXISTS webhook_deliveries;
DROP TABLE IF EXISTS webhook_endpoints;
DROP TABLE IF EXISTS outbox;
DROP TABLE IF EXISTS events;
DROP TABLE IF EXISTS policy_rules;
DROP TABLE IF EXISTS conflict_rules;
DROP TABLE IF EXISTS resource_rules;
DROP TABLE IF EXISTS approvals;
DROP TABLE IF EXISTS action_results;
DROP TABLE IF EXISTS decisions;
DROP TABLE IF EXISTS leases;
DROP TABLE IF EXISTS effects;
DROP TABLE IF EXISTS idempotency_records;
DROP TABLE IF EXISTS action_resources;
DROP TABLE IF EXISTS actions;
DROP TABLE IF EXISTS observations;
DROP TABLE IF EXISTS resource_versions;
DROP TABLE IF EXISTS resource_edges;
DROP TABLE IF EXISTS resources;
DROP TABLE IF EXISTS work_edges;
DROP TABLE IF EXISTS work;
DROP TABLE IF EXISTS pull_requests;
DROP TABLE IF EXISTS repositories;
DROP TABLE IF EXISTS sessions;
DROP TABLE IF EXISTS agents;
DROP TABLE IF EXISTS actors;
DROP TABLE IF EXISTS api_keys;
DROP TABLE IF EXISTS tenants;
