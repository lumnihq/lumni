-- +goose Up
UPDATE graph_changes SET entity_type = CASE entity_type
    WHEN 'agents' THEN 'agent'
    WHEN 'sessions' THEN 'session'
    WHEN 'work_edges' THEN 'work_edge'
    WHEN 'resources' THEN 'resource'
    WHEN 'resource_edges' THEN 'resource_edge'
    WHEN 'observations' THEN 'observation'
    WHEN 'actions' THEN 'action'
    WHEN 'action_resources' THEN 'action_resource'
    WHEN 'decisions' THEN 'decision'
    WHEN 'approvals' THEN 'approval'
    WHEN 'leases' THEN 'lease'
    WHEN 'costs' THEN 'cost'
    ELSE entity_type
END;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION lumni_graph_change() RETURNS trigger AS $$
DECLARE
    body JSONB;
    tenant UUID;
    entity TEXT;
    entity_kind TEXT;
    lenses TEXT[];
BEGIN
    body := CASE WHEN TG_OP = 'DELETE' THEN to_jsonb(OLD) ELSE to_jsonb(NEW) END;
    tenant := (body ->> 'tenant_id')::UUID;
    entity := COALESCE(body ->> 'id', body ->> 'action_id', body ->> 'resource_id',
        body ->> 'work_id', body ->> 'source_work_id', body ->> 'source_resource_id', '');
    entity_kind := CASE TG_TABLE_NAME
        WHEN 'agents' THEN 'agent'
        WHEN 'sessions' THEN 'session'
        WHEN 'work_edges' THEN 'work_edge'
        WHEN 'resources' THEN 'resource'
        WHEN 'resource_edges' THEN 'resource_edge'
        WHEN 'observations' THEN 'observation'
        WHEN 'actions' THEN 'action'
        WHEN 'action_resources' THEN 'action_resource'
        WHEN 'decisions' THEN 'decision'
        WHEN 'approvals' THEN 'approval'
        WHEN 'leases' THEN 'lease'
        WHEN 'costs' THEN 'cost'
        ELSE TG_TABLE_NAME
    END;
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
    VALUES (tenant, upper(TG_TABLE_NAME || '.' || TG_OP), entity_kind, entity, lenses);
    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

-- +goose Down
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
