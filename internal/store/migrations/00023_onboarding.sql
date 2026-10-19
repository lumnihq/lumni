-- +goose Up
CREATE TABLE onboarding_codes (
 tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
 code_hash BYTEA PRIMARY KEY, scopes TEXT[] NOT NULL,
 actor_hint TEXT NOT NULL DEFAULT '', actor_id TEXT,
 created_by UUID NOT NULL REFERENCES api_keys(id), issued_key_id UUID REFERENCES api_keys(id),
 created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(), expires_at TIMESTAMPTZ NOT NULL,
 used_at TIMESTAMPTZ
);
CREATE INDEX onboarding_codes_tenant ON onboarding_codes(tenant_id,created_at);
CREATE INDEX onboarding_codes_issued_key ON onboarding_codes(tenant_id,issued_key_id);
CREATE TABLE onboarding_exchange_rate (
 peer_hash BYTEA NOT NULL, bucket TIMESTAMPTZ NOT NULL, requests INT NOT NULL,
 PRIMARY KEY(peer_hash,bucket)
);
CREATE INDEX actors_type_page ON actors(tenant_id,type,created_at DESC,id DESC);
CREATE INDEX graph_changes_type_cursor ON graph_changes(tenant_id,event_type,sequence);

-- +goose StatementBegin
CREATE FUNCTION onboarding_graph_event() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_TABLE_NAME='actors' THEN
  INSERT INTO graph_changes(tenant_id,event_type,entity_type,entity_id,affected_lenses)
  VALUES(NEW.tenant_id,'actor.created','actor',NEW.id,ARRAY['live','agents']);
 ELSE
  INSERT INTO graph_changes(tenant_id,event_type,entity_type,entity_id,affected_lenses)
  VALUES(NEW.tenant_id,'action.checked','action',NEW.action_id,ARRAY['live','transaction']);
 END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER onboarding_actor_created AFTER INSERT ON actors FOR EACH ROW EXECUTE FUNCTION onboarding_graph_event();
CREATE TRIGGER onboarding_action_checked AFTER INSERT ON decisions FOR EACH ROW EXECUTE FUNCTION onboarding_graph_event();

-- +goose Down
DROP TRIGGER onboarding_actor_created ON actors;
DROP TRIGGER onboarding_action_checked ON decisions;
DROP FUNCTION onboarding_graph_event();
DROP INDEX actors_type_page,graph_changes_type_cursor;
DROP TABLE onboarding_codes,onboarding_exchange_rate;
