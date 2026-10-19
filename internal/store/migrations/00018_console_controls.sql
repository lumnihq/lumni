-- +goose Up
ALTER TABLE actors ADD COLUMN paused BOOLEAN NOT NULL DEFAULT false,
 ADD COLUMN paused_at TIMESTAMPTZ, ADD COLUMN paused_by TEXT, ADD COLUMN until TIMESTAMPTZ, ADD COLUMN pause_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE tenants ADD COLUMN paused BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE policy_rules ADD COLUMN deleted_at TIMESTAMPTZ;
ALTER TABLE dependencies ADD COLUMN created_at TIMESTAMPTZ NOT NULL DEFAULT now();
ALTER TABLE enforcement_rules ADD COLUMN created_at TIMESTAMPTZ NOT NULL DEFAULT now();
ALTER TABLE reconciliation_handlers ADD COLUMN created_at TIMESTAMPTZ NOT NULL DEFAULT now();
ALTER TABLE resource_versions ADD COLUMN actor_id TEXT, ADD COLUMN action_id TEXT, ADD COLUMN summary TEXT, ADD COLUMN value_hash TEXT;
ALTER TABLE outcomes ADD COLUMN retry_attempt_id TEXT, ADD COLUMN retry_started_at TIMESTAMPTZ;
ALTER TABLE action_resources ADD COLUMN observed_at TIMESTAMPTZ;
ALTER TABLE composio_connections ADD COLUMN toolkit TEXT NOT NULL DEFAULT 'github' CHECK(toolkit IN('github','slack'));
CREATE INDEX actions_console_page ON actions(tenant_id,requested_at DESC,id DESC);
CREATE INDEX actions_actor_page ON actions(tenant_id,actor_id,requested_at DESC,id DESC);
CREATE INDEX approvals_console_page ON approvals(tenant_id,status,requested_at DESC,id DESC);
CREATE INDEX outcomes_console_page ON outcomes(tenant_id,created_at DESC,id DESC);
CREATE INDEX incidents_console_page ON incidents(tenant_id,created_at DESC,id DESC);

CREATE TABLE admission_limits (
 tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE, id TEXT NOT NULL,
 scope JSONB NOT NULL DEFAULT '{}', max_in_flight INT CHECK(max_in_flight>0), per_minute INT CHECK(per_minute>0),
 hold BOOLEAN NOT NULL DEFAULT false, mode TEXT NOT NULL DEFAULT 'ENFORCE' CHECK(mode IN('ENFORCE','WARN','SHADOW')),
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(), updated_at TIMESTAMPTZ NOT NULL DEFAULT now(), PRIMARY KEY(tenant_id,id)
);
CREATE TABLE spend_limits (
 tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE, scope_key TEXT NOT NULL DEFAULT '',
 daily_micros_usd BIGINT NOT NULL CHECK(daily_micros_usd>0), when_reached TEXT NOT NULL CHECK(when_reached IN('HOLD','SLOW')),
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(), PRIMARY KEY(tenant_id,scope_key)
);
CREATE TABLE autopilot_grants (
 tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE, actor_id TEXT NOT NULL, operation TEXT NOT NULL,
 skip_rules TEXT[] NOT NULL, below JSONB NOT NULL, revert_after JSONB NOT NULL,
 active BOOLEAN NOT NULL DEFAULT true, since TIMESTAMPTZ NOT NULL DEFAULT now(), reverted_at TIMESTAMPTZ,
 PRIMARY KEY(tenant_id,actor_id), FOREIGN KEY(tenant_id,actor_id) REFERENCES actors(tenant_id,id) ON DELETE CASCADE
);
CREATE TABLE proofs (
 tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE, id TEXT NOT NULL, action_id TEXT NOT NULL,
 token_hash BYTEA NOT NULL UNIQUE, show JSONB NOT NULL, expires_at TIMESTAMPTZ NOT NULL, revoked_at TIMESTAMPTZ,
 url_ciphertext BYTEA NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(), PRIMARY KEY(tenant_id,id),
 FOREIGN KEY(tenant_id,action_id) REFERENCES actions(tenant_id,id)
);
CREATE TABLE notification_config (
 tenant_id UUID PRIMARY KEY REFERENCES tenants(id) ON DELETE CASCADE, channels JSONB NOT NULL DEFAULT '[]', routes JSONB NOT NULL DEFAULT '[]',
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(), updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE notification_jobs (
 tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE, id TEXT NOT NULL, channel_id TEXT NOT NULL,
 kind TEXT NOT NULL, ref_id TEXT NOT NULL, payload JSONB NOT NULL DEFAULT '{}', status TEXT NOT NULL DEFAULT 'PENDING',
 attempts INT NOT NULL DEFAULT 0, next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(), last_error TEXT,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(), sent_at TIMESTAMPTZ, PRIMARY KEY(tenant_id,id)
);
CREATE INDEX notification_jobs_ready ON notification_jobs(next_attempt_at) WHERE status='PENDING';
CREATE TABLE notification_schedules (
 tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE, route_key TEXT NOT NULL, last_sent_date TEXT NOT NULL,
 PRIMARY KEY(tenant_id,route_key)
);
CREATE TABLE audit_events (
 tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE, id UUID NOT NULL DEFAULT gen_random_uuid(),
 at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(), by TEXT NOT NULL, type TEXT NOT NULL,
 target JSONB NOT NULL, before JSONB, after JSONB, PRIMARY KEY(tenant_id,id)
);
CREATE INDEX audit_events_page ON audit_events(tenant_id,at DESC,id DESC);
CREATE TABLE app_call_timings (
 id BIGSERIAL PRIMARY KEY, tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
 app TEXT NOT NULL, at TIMESTAMPTZ NOT NULL DEFAULT now(), duration_ms BIGINT NOT NULL,
 error BOOLEAN NOT NULL DEFAULT false, timeout BOOLEAN NOT NULL DEFAULT false
);
CREATE INDEX app_call_timings_range ON app_call_timings(tenant_id,app,at);
CREATE TABLE billing_plans (
 tenant_id UUID PRIMARY KEY REFERENCES tenants(id) ON DELETE CASCADE, plan TEXT NOT NULL DEFAULT 'team',
 price_per_action_micros_usd BIGINT NOT NULL DEFAULT 20000 CHECK(price_per_action_micros_usd>=0)
);
CREATE TABLE public_proof_rate (
 key BYTEA NOT NULL, bucket TIMESTAMPTZ NOT NULL, requests INT NOT NULL, PRIMARY KEY(key,bucket)
);

-- Audit events are written in the same transaction as the mutation. The API
-- sets lumni.audit_by locally; maintenance uses the system identity.
-- +goose StatementBegin
CREATE FUNCTION console_scope_matches(s JSONB, op TEXT, actor TEXT, resources TEXT[]) RETURNS boolean LANGUAGE sql IMMUTABLE AS $$
 SELECT (COALESCE(s->>'operation','') IN('','*') OR s->>'operation'=op)
 AND (COALESCE(s->>'actor_id','')='' OR s->>'actor_id'=actor)
 AND (COALESCE(s->>'resource','')='' OR EXISTS(SELECT 1 FROM unnest(resources) r WHERE
 CASE WHEN right(s->>'resource',1)='*' THEN left(r,length(s->>'resource')-1)=left(s->>'resource',length(s->>'resource')-1) ELSE r=s->>'resource' END))
$$;
CREATE FUNCTION console_audit() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE b JSONB; a JSONB; t JSONB; typ TEXT; who TEXT;
BEGIN
 b:=CASE WHEN TG_OP='INSERT' THEN NULL ELSE to_jsonb(OLD) END;
 a:=CASE WHEN TG_OP='DELETE' THEN NULL ELSE to_jsonb(NEW) END;
 IF b IS NOT DISTINCT FROM a THEN RETURN COALESCE(NEW,OLD); END IF;
 who:=COALESCE(NULLIF(current_setting('lumni.audit_by',true),''),'system');
 t:=COALESCE(a,b); typ:=TG_ARGV[0];
 IF NOT EXISTS(SELECT 1 FROM tenants WHERE id=(t->>'tenant_id')::uuid) THEN RETURN COALESCE(NEW,OLD);END IF;
 IF TG_TABLE_NAME='policy_rules' THEN
  typ:='policy_rule.'||CASE WHEN TG_OP='INSERT' THEN 'created' WHEN a->>'deleted_at' IS NOT NULL AND b->>'deleted_at' IS NULL THEN 'deleted' ELSE 'updated' END;
 ELSIF TG_TABLE_NAME='enforcement_rules' THEN typ:='enforcement.'||CASE WHEN TG_OP='DELETE' THEN 'cleared' ELSE 'set' END;
 ELSIF TG_TABLE_NAME='autopilot_grants' THEN typ:='autopilot.'||CASE WHEN TG_OP='DELETE' OR a->>'active'='false' THEN 'reverted' ELSE 'granted' END;
 ELSIF TG_TABLE_NAME='actors' THEN
  IF (b->>'paused',b->>'until') IS NOT DISTINCT FROM (a->>'paused',a->>'until') THEN RETURN NEW; END IF;
  typ:=CASE WHEN a->>'paused'='true' THEN 'actor.paused' ELSE 'actor.resumed' END;
 ELSIF TG_TABLE_NAME='approvals' THEN
  IF TG_OP='INSERT' OR b->>'status'=a->>'status' THEN RETURN NEW; END IF;
  typ:='approval.decided';who:=COALESCE(a->>'resolved_by',who);
 END IF;
 INSERT INTO audit_events(tenant_id,by,type,target,before,after)
 VALUES((t->>'tenant_id')::uuid,who,typ,
 jsonb_strip_nulls(jsonb_build_object('id',t->'id','actor_id',t->'actor_id','operation',t->'operation','rule',t->'rule','action_id',t->'action_id')),
 b-'tenant_id'-'secret_ciphertext'-'token_hash'-'url_ciphertext',a-'tenant_id'-'secret_ciphertext'-'token_hash'-'url_ciphertext');
 RETURN COALESCE(NEW,OLD);
END $$;
-- +goose StatementEnd
CREATE TRIGGER console_policy_audit AFTER INSERT OR UPDATE OR DELETE ON policy_rules FOR EACH ROW EXECUTE FUNCTION console_audit('policy_rule.updated');
CREATE TRIGGER console_enforcement_audit AFTER INSERT OR UPDATE OR DELETE ON enforcement_rules FOR EACH ROW EXECUTE FUNCTION console_audit('enforcement.set');
CREATE TRIGGER console_verifier_audit AFTER INSERT OR UPDATE OR DELETE ON verifiers FOR EACH ROW EXECUTE FUNCTION console_audit('verifier.changed');
CREATE TRIGGER console_limit_audit AFTER INSERT OR UPDATE OR DELETE ON admission_limits FOR EACH ROW EXECUTE FUNCTION console_audit('limit.set');
CREATE TRIGGER console_spend_audit AFTER INSERT OR UPDATE OR DELETE ON spend_limits FOR EACH ROW EXECUTE FUNCTION console_audit('spend_limit.set');
CREATE TRIGGER console_actor_audit AFTER UPDATE ON actors FOR EACH ROW EXECUTE FUNCTION console_audit('actor.paused');
CREATE TRIGGER console_approval_audit AFTER UPDATE ON approvals FOR EACH ROW EXECUTE FUNCTION console_audit('approval.decided');
CREATE TRIGGER console_autopilot_audit AFTER INSERT OR UPDATE OR DELETE ON autopilot_grants FOR EACH ROW EXECUTE FUNCTION console_audit('autopilot.granted');
CREATE TRIGGER console_proof_audit AFTER INSERT OR UPDATE ON proofs FOR EACH ROW EXECUTE FUNCTION console_audit('proof.created');

-- +goose Down
DROP TRIGGER console_policy_audit ON policy_rules;
DROP TRIGGER console_enforcement_audit ON enforcement_rules;
DROP TRIGGER console_verifier_audit ON verifiers;
DROP TRIGGER console_actor_audit ON actors;
DROP TRIGGER console_approval_audit ON approvals;
DROP TABLE admission_limits,spend_limits,autopilot_grants,proofs,notification_config,notification_jobs,notification_schedules,audit_events,app_call_timings,billing_plans,public_proof_rate;
DROP FUNCTION console_audit();
DROP FUNCTION console_scope_matches(JSONB,TEXT,TEXT,TEXT[]);
DROP INDEX actions_console_page,actions_actor_page,approvals_console_page,outcomes_console_page,incidents_console_page;
ALTER TABLE actors DROP COLUMN paused,DROP COLUMN paused_at,DROP COLUMN paused_by,DROP COLUMN until,DROP COLUMN pause_reason;
ALTER TABLE tenants DROP COLUMN paused;
ALTER TABLE policy_rules DROP COLUMN deleted_at;
ALTER TABLE dependencies DROP COLUMN created_at;
ALTER TABLE enforcement_rules DROP COLUMN created_at;
ALTER TABLE reconciliation_handlers DROP COLUMN created_at;
ALTER TABLE resource_versions DROP COLUMN actor_id,DROP COLUMN action_id,DROP COLUMN summary,DROP COLUMN value_hash;
ALTER TABLE outcomes DROP COLUMN retry_attempt_id, DROP COLUMN retry_started_at;
ALTER TABLE action_resources DROP COLUMN observed_at;
ALTER TABLE composio_connections DROP COLUMN toolkit;
