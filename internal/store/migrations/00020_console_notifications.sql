-- +goose Up
-- +goose StatementBegin
CREATE FUNCTION console_route_notification() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE event TEXT; kind TEXT;
BEGIN
 IF TG_TABLE_NAME='approvals' THEN event:='approval.requested';kind:='approval';ELSE event:='incident.opened';kind:='incident';END IF;
 INSERT INTO notification_jobs(tenant_id,id,channel_id,kind,ref_id)
 SELECT NEW.tenant_id,'route:'||event||':'||NEW.id::text||':'||ch, ch,kind,NEW.id::text
 FROM notification_config c CROSS JOIN LATERAL jsonb_array_elements(c.routes) route
 CROSS JOIN LATERAL jsonb_array_elements_text(route->'channels') ch
 WHERE c.tenant_id=NEW.tenant_id AND route->>'event'=event ON CONFLICT DO NOTHING;
 RETURN NEW;
END $$;
CREATE FUNCTION console_revert_autopilot() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE grant_row autopilot_grants; slips INT;
BEGIN
 FOR grant_row IN SELECT g.* FROM autopilot_grants g JOIN actions a ON a.tenant_id=g.tenant_id AND a.actor_id=g.actor_id AND a.operation=g.operation WHERE a.tenant_id=NEW.tenant_id AND a.id=NEW.action_id AND g.active FOR UPDATE OF g LOOP
 SELECT count(*) INTO slips FROM (SELECT kind,outcome FROM console_action_facts WHERE tenant_id=grant_row.tenant_id AND actor_id=grant_row.actor_id AND operation=grant_row.operation AND at>=grant_row.since ORDER BY at DESC,action_id DESC LIMIT (grant_row.revert_after->>'per')::int) f WHERE kind='recheck' OR outcome IN('MISMATCH','PARTIAL','FAILED');
 IF slips>=(grant_row.revert_after->>'slips')::int THEN UPDATE autopilot_grants SET active=false,reverted_at=now() WHERE tenant_id=grant_row.tenant_id AND actor_id=grant_row.actor_id;END IF;
 END LOOP;
 RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER console_notify_approval AFTER INSERT ON approvals FOR EACH ROW EXECUTE FUNCTION console_route_notification();
CREATE TRIGGER console_notify_incident AFTER INSERT ON incidents FOR EACH ROW EXECUTE FUNCTION console_route_notification();
CREATE TRIGGER zz_console_autopilot AFTER INSERT OR UPDATE OF status ON outcomes FOR EACH ROW EXECUTE FUNCTION console_revert_autopilot();
CREATE TRIGGER zz_console_autopilot_decision AFTER INSERT ON decisions FOR EACH ROW EXECUTE FUNCTION console_revert_autopilot();

-- +goose Down
DROP TRIGGER console_notify_approval ON approvals;
DROP TRIGGER console_notify_incident ON incidents;
DROP TRIGGER zz_console_autopilot ON outcomes;
DROP TRIGGER zz_console_autopilot_decision ON decisions;
DROP FUNCTION console_route_notification(),console_revert_autopilot();
