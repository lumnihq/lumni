-- +goose Up
ALTER TABLE onboarding_codes ADD COLUMN host TEXT, ADD COLUMN project TEXT;
ALTER TABLE spend_limits ADD COLUMN tz TEXT NOT NULL DEFAULT 'UTC';

-- The two midnights can be 23 or 25 hours apart across daylight saving changes.
-- +goose StatementBegin
CREATE FUNCTION console_spend_window(zone TEXT, at_time TIMESTAMPTZ)
RETURNS TABLE(start_at TIMESTAMPTZ, end_at TIMESTAMPTZ) LANGUAGE sql STABLE AS $$
 SELECT date_trunc('day',at_time AT TIME ZONE zone) AT TIME ZONE zone,
        (date_trunc('day',at_time AT TIME ZONE zone)+interval '1 day') AT TIME ZONE zone
$$;

-- Snapshot explicit action context while the approval decision is committed.
-- Retained audit entries do not change if an action is subsequently edited.
CREATE FUNCTION console_approval_audit_context() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE context JSONB;
BEGIN
 IF NEW.type='approval.decided' THEN
  SELECT jsonb_build_object('actor_id',a.actor_id,'operation',a.operation,
   'summary',COALESCE(CASE WHEN jsonb_typeof(a.metadata->'summary')='string'
     THEN NULLIF(a.metadata->>'summary','') END,a.operation)) INTO context
  FROM actions a WHERE a.tenant_id=NEW.tenant_id AND a.id=NEW.target->>'action_id';
  NEW.target:=NEW.target||COALESCE(context,'{}'::jsonb);
 END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER console_approval_audit_context BEFORE INSERT ON audit_events
 FOR EACH ROW EXECUTE FUNCTION console_approval_audit_context();

-- +goose Down
DROP TRIGGER console_approval_audit_context ON audit_events;
DROP FUNCTION console_approval_audit_context();
DROP FUNCTION console_spend_window(TEXT,TIMESTAMPTZ);
ALTER TABLE spend_limits DROP COLUMN tz;
ALTER TABLE onboarding_codes DROP COLUMN host, DROP COLUMN project;
