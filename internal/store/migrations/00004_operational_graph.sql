-- +goose Up
ALTER TABLE decisions
    ADD COLUMN evaluations JSONB NOT NULL DEFAULT '[]'::jsonb;

CREATE TABLE browser_sessions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    api_key_id UUID NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE,
    token_hash BYTEA NOT NULL UNIQUE,
    csrf_hash BYTEA NOT NULL,
    scopes TEXT[] NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    idle_expires_at TIMESTAMPTZ NOT NULL,
    absolute_expires_at TIMESTAMPTZ NOT NULL,
    revoked_at TIMESTAMPTZ NULL
);
CREATE INDEX browser_sessions_tenant_idx ON browser_sessions (tenant_id, last_seen_at DESC);
CREATE INDEX browser_sessions_expiry_idx ON browser_sessions (idle_expires_at, absolute_expires_at)
    WHERE revoked_at IS NULL;

CREATE TABLE graph_changes (
    sequence BIGSERIAL PRIMARY KEY,
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    event_type TEXT NOT NULL,
    entity_type TEXT NOT NULL,
    entity_id TEXT NOT NULL DEFAULT '',
    affected_lenses TEXT[] NOT NULL DEFAULT ARRAY['live','agents','work','resources','transaction','finops']::TEXT[],
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX graph_changes_tenant_sequence_idx ON graph_changes (tenant_id, sequence);
CREATE INDEX graph_changes_tenant_time_idx ON graph_changes (tenant_id, occurred_at DESC);

CREATE TABLE analytics_outbox (
    id BIGSERIAL PRIMARY KEY,
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    event_id TEXT NOT NULL,
    kind TEXT NOT NULL,
    entity_type TEXT NOT NULL DEFAULT '',
    entity_id TEXT NOT NULL DEFAULT '',
    relationship TEXT NOT NULL DEFAULT '',
    source_id TEXT NOT NULL DEFAULT '',
    target_id TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT '',
    decision TEXT NOT NULL DEFAULT '',
    reason_code TEXT NOT NULL DEFAULT '',
    provider TEXT NOT NULL DEFAULT '',
    model TEXT NOT NULL DEFAULT '',
    waste_reason TEXT NOT NULL DEFAULT '',
    amount_micros_usd BIGINT NOT NULL DEFAULT 0,
    duration_ms DOUBLE PRECISION NOT NULL DEFAULT 0,
    occurred_at TIMESTAMPTZ NOT NULL,
    exported_at TIMESTAMPTZ NULL,
    attempts INT NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    last_error TEXT NULL,
    UNIQUE (tenant_id, event_id)
);
CREATE INDEX analytics_outbox_pending_idx ON analytics_outbox (next_attempt_at, id)
    WHERE exported_at IS NULL;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION lumni_graph_change() RETURNS trigger AS $$
DECLARE
    body JSONB;
    tenant UUID;
    entity TEXT;
    lenses TEXT[];
BEGIN
    body := CASE WHEN TG_OP = 'DELETE' THEN to_jsonb(OLD) ELSE to_jsonb(NEW) END;
    tenant := (body ->> 'tenant_id')::UUID;
    entity := COALESCE(body ->> 'id', body ->> 'action_id', body ->> 'resource_id',
        body ->> 'work_id', body ->> 'source_work_id', body ->> 'source_resource_id', '');
    lenses := CASE TG_TABLE_NAME
        WHEN 'agents' THEN ARRAY['live','agents']::TEXT[]
        WHEN 'sessions' THEN ARRAY['live','agents']::TEXT[]
        WHEN 'work' THEN ARRAY['live','agents','work','finops']::TEXT[]
        WHEN 'work_edges' THEN ARRAY['work']::TEXT[]
        WHEN 'resources' THEN ARRAY['live','resources']::TEXT[]
        WHEN 'resource_edges' THEN ARRAY['resources']::TEXT[]
        WHEN 'observations' THEN ARRAY['live','resources','transaction']::TEXT[]
        WHEN 'actions' THEN ARRAY['live','agents','work','resources','transaction','finops']::TEXT[]
        WHEN 'action_resources' THEN ARRAY['live','work','resources','transaction']::TEXT[]
        WHEN 'decisions' THEN ARRAY['live','transaction','finops']::TEXT[]
        WHEN 'approvals' THEN ARRAY['live','transaction']::TEXT[]
        WHEN 'leases' THEN ARRAY['live','resources','transaction']::TEXT[]
        WHEN 'costs' THEN ARRAY['agents','work','finops']::TEXT[]
        ELSE ARRAY['live']::TEXT[]
    END;
    INSERT INTO graph_changes (tenant_id, event_type, entity_type, entity_id, affected_lenses)
    VALUES (tenant, upper(TG_TABLE_NAME || '.' || TG_OP), TG_TABLE_NAME, entity, lenses);
    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER graph_change_agents AFTER INSERT OR UPDATE OR DELETE ON agents
    FOR EACH ROW EXECUTE FUNCTION lumni_graph_change();
CREATE TRIGGER graph_change_sessions AFTER INSERT OR UPDATE OR DELETE ON sessions
    FOR EACH ROW EXECUTE FUNCTION lumni_graph_change();
CREATE TRIGGER graph_change_work AFTER INSERT OR UPDATE OR DELETE ON work
    FOR EACH ROW EXECUTE FUNCTION lumni_graph_change();
CREATE TRIGGER graph_change_work_edges AFTER INSERT OR UPDATE OR DELETE ON work_edges
    FOR EACH ROW EXECUTE FUNCTION lumni_graph_change();
CREATE TRIGGER graph_change_resources AFTER INSERT OR UPDATE OR DELETE ON resources
    FOR EACH ROW EXECUTE FUNCTION lumni_graph_change();
CREATE TRIGGER graph_change_resource_edges AFTER INSERT OR UPDATE OR DELETE ON resource_edges
    FOR EACH ROW EXECUTE FUNCTION lumni_graph_change();
CREATE TRIGGER graph_change_observations AFTER INSERT OR UPDATE OR DELETE ON observations
    FOR EACH ROW EXECUTE FUNCTION lumni_graph_change();
CREATE TRIGGER graph_change_actions AFTER INSERT OR UPDATE OR DELETE ON actions
    FOR EACH ROW EXECUTE FUNCTION lumni_graph_change();
CREATE TRIGGER graph_change_action_resources AFTER INSERT OR UPDATE OR DELETE ON action_resources
    FOR EACH ROW EXECUTE FUNCTION lumni_graph_change();
CREATE TRIGGER graph_change_decisions AFTER INSERT OR UPDATE OR DELETE ON decisions
    FOR EACH ROW EXECUTE FUNCTION lumni_graph_change();
CREATE TRIGGER graph_change_approvals AFTER INSERT OR UPDATE OR DELETE ON approvals
    FOR EACH ROW EXECUTE FUNCTION lumni_graph_change();
CREATE TRIGGER graph_change_leases AFTER INSERT OR UPDATE OR DELETE ON leases
    FOR EACH ROW EXECUTE FUNCTION lumni_graph_change();
CREATE TRIGGER graph_change_costs AFTER INSERT OR UPDATE OR DELETE ON costs
    FOR EACH ROW EXECUTE FUNCTION lumni_graph_change();

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION lumni_analytics_fact() RETURNS trigger AS $$
DECLARE
    event_key TEXT;
    occurred TIMESTAMPTZ;
BEGIN
    IF TG_TABLE_NAME = 'events' THEN
        event_key := 'event:' || NEW.id;
        occurred := NEW.occurred_at;
        INSERT INTO analytics_outbox
            (tenant_id,event_id,kind,entity_type,entity_id,status,occurred_at)
        VALUES
            (NEW.tenant_id,event_key,'event',NEW.entity_type,NEW.entity_id,NEW.type,occurred)
        ON CONFLICT (tenant_id,event_id) DO NOTHING;
    ELSIF TG_TABLE_NAME = 'decisions' THEN
        event_key := 'decision:' || NEW.id::TEXT;
        INSERT INTO analytics_outbox
            (tenant_id,event_id,kind,entity_type,entity_id,status,decision,reason_code,occurred_at)
        VALUES
            (NEW.tenant_id,event_key,'decision','action',NEW.action_id,NEW.decision,
             NEW.decision,NEW.reason_code,NEW.created_at)
        ON CONFLICT (tenant_id,event_id) DO NOTHING;
    ELSIF TG_TABLE_NAME = 'costs' THEN
        event_key := 'cost:' || NEW.id::TEXT;
        INSERT INTO analytics_outbox
            (tenant_id,event_id,kind,entity_type,entity_id,provider,model,waste_reason,
             amount_micros_usd,occurred_at)
        VALUES
            (NEW.tenant_id,event_key,'cost',
             CASE WHEN NEW.action_id IS NOT NULL THEN 'action'
                  WHEN NEW.work_id IS NOT NULL THEN 'work'
                  WHEN NEW.agent_id IS NOT NULL THEN 'agent' ELSE 'tenant' END,
             COALESCE(NEW.action_id,NEW.work_id,NEW.agent_id,''),NEW.provider,
             COALESCE(NEW.model,''),COALESCE(NEW.waste_reason,''),NEW.amount_micros_usd,
             NEW.observed_at)
        ON CONFLICT (tenant_id,event_id) DO UPDATE SET
            waste_reason=EXCLUDED.waste_reason,
            amount_micros_usd=EXCLUDED.amount_micros_usd,
            occurred_at=EXCLUDED.occurred_at,
            exported_at=NULL,
            next_attempt_at=clock_timestamp();
    ELSIF TG_TABLE_NAME = 'otel_spans' THEN
        event_key := 'span:' || NEW.trace_id || ':' || NEW.span_id;
        INSERT INTO analytics_outbox
            (tenant_id,event_id,kind,entity_type,entity_id,status,duration_ms,occurred_at)
        VALUES
            (NEW.tenant_id,event_key,'span','span',NEW.trace_id || ':' || NEW.span_id,
             NEW.name,GREATEST(0,EXTRACT(EPOCH FROM (COALESCE(NEW.end_time,NEW.start_time)-NEW.start_time))*1000),
             NEW.start_time)
        ON CONFLICT (tenant_id,event_id) DO NOTHING;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER analytics_events AFTER INSERT ON events
    FOR EACH ROW EXECUTE FUNCTION lumni_analytics_fact();
CREATE TRIGGER analytics_decisions AFTER INSERT ON decisions
    FOR EACH ROW EXECUTE FUNCTION lumni_analytics_fact();
CREATE TRIGGER analytics_costs AFTER INSERT OR UPDATE OF waste_reason ON costs
    FOR EACH ROW EXECUTE FUNCTION lumni_analytics_fact();
CREATE TRIGGER analytics_spans AFTER INSERT ON otel_spans
    FOR EACH ROW EXECUTE FUNCTION lumni_analytics_fact();

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION lumni_analytics_relationship() RETURNS trigger AS $$
DECLARE
    event_key TEXT;
    relation TEXT;
    source_entity TEXT;
    target_entity TEXT;
BEGIN
    IF TG_TABLE_NAME = 'action_resources' THEN
        event_key := 'edge:action-resource:' || NEW.action_id || ':' || NEW.resource_id || ':' || NEW.mode;
        relation := NEW.mode;
        source_entity := 'action:' || NEW.action_id;
        target_entity := 'resource:' || NEW.resource_id;
    ELSIF TG_TABLE_NAME = 'work_edges' THEN
        event_key := 'edge:work:' || NEW.source_work_id || ':' || NEW.relationship || ':' || NEW.target_work_id;
        relation := NEW.relationship;
        source_entity := 'work:' || NEW.source_work_id;
        target_entity := 'work:' || NEW.target_work_id;
    ELSE
        event_key := 'edge:resource:' || NEW.source_resource_id || ':' || NEW.relationship || ':' || NEW.target_resource_id;
        relation := NEW.relationship;
        source_entity := 'resource:' || NEW.source_resource_id;
        target_entity := 'resource:' || NEW.target_resource_id;
    END IF;
    INSERT INTO analytics_outbox
        (tenant_id,event_id,kind,entity_type,entity_id,relationship,source_id,target_id,occurred_at)
    VALUES
        (NEW.tenant_id,event_key,'relationship','edge',event_key,relation,source_entity,target_entity,clock_timestamp())
    ON CONFLICT (tenant_id,event_id) DO UPDATE SET relationship=EXCLUDED.relationship,
        source_id=EXCLUDED.source_id,target_id=EXCLUDED.target_id,occurred_at=EXCLUDED.occurred_at,
        exported_at=NULL,next_attempt_at=clock_timestamp();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER analytics_action_resources AFTER INSERT OR UPDATE ON action_resources
    FOR EACH ROW EXECUTE FUNCTION lumni_analytics_relationship();
CREATE TRIGGER analytics_work_edges AFTER INSERT OR UPDATE ON work_edges
    FOR EACH ROW EXECUTE FUNCTION lumni_analytics_relationship();
CREATE TRIGGER analytics_resource_edges AFTER INSERT OR UPDATE ON resource_edges
    FOR EACH ROW EXECUTE FUNCTION lumni_analytics_relationship();

-- +goose Down
DROP TRIGGER IF EXISTS analytics_resource_edges ON resource_edges;
DROP TRIGGER IF EXISTS analytics_work_edges ON work_edges;
DROP TRIGGER IF EXISTS analytics_action_resources ON action_resources;
DROP FUNCTION IF EXISTS lumni_analytics_relationship();
DROP TRIGGER IF EXISTS analytics_spans ON otel_spans;
DROP TRIGGER IF EXISTS analytics_costs ON costs;
DROP TRIGGER IF EXISTS analytics_decisions ON decisions;
DROP TRIGGER IF EXISTS analytics_events ON events;
DROP FUNCTION IF EXISTS lumni_analytics_fact();
DROP TRIGGER IF EXISTS graph_change_costs ON costs;
DROP TRIGGER IF EXISTS graph_change_leases ON leases;
DROP TRIGGER IF EXISTS graph_change_approvals ON approvals;
DROP TRIGGER IF EXISTS graph_change_decisions ON decisions;
DROP TRIGGER IF EXISTS graph_change_action_resources ON action_resources;
DROP TRIGGER IF EXISTS graph_change_actions ON actions;
DROP TRIGGER IF EXISTS graph_change_observations ON observations;
DROP TRIGGER IF EXISTS graph_change_resource_edges ON resource_edges;
DROP TRIGGER IF EXISTS graph_change_resources ON resources;
DROP TRIGGER IF EXISTS graph_change_work_edges ON work_edges;
DROP TRIGGER IF EXISTS graph_change_work ON work;
DROP TRIGGER IF EXISTS graph_change_sessions ON sessions;
DROP TRIGGER IF EXISTS graph_change_agents ON agents;
DROP FUNCTION IF EXISTS lumni_graph_change();
DROP TABLE IF EXISTS analytics_outbox;
DROP TABLE IF EXISTS graph_changes;
DROP TABLE IF EXISTS browser_sessions;
ALTER TABLE decisions DROP COLUMN IF EXISTS evaluations;
