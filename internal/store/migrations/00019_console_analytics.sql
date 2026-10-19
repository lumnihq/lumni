-- +goose Up
-- One fact per action and incrementally corrected aggregates. Rechecks and
-- verification updates replace a contribution, never count an action twice.
CREATE TABLE console_action_facts (
 tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE, action_id TEXT NOT NULL,
 at TIMESTAMPTZ NOT NULL, bucket TIMESTAMPTZ NOT NULL, actor_id TEXT NOT NULL, operation TEXT NOT NULL, app TEXT NOT NULL,
 kind TEXT NOT NULL, reason_code TEXT NOT NULL, outcome TEXT NOT NULL, would TEXT NOT NULL,
 cost_micros_usd BIGINT NOT NULL, kept_micros_usd BIGINT NOT NULL, resources TEXT[] NOT NULL, clean BOOLEAN NOT NULL,
 PRIMARY KEY(tenant_id,action_id), FOREIGN KEY(tenant_id,action_id) REFERENCES actions(tenant_id,id) ON DELETE CASCADE
);
CREATE INDEX console_facts_time ON console_action_facts(tenant_id,at);
CREATE INDEX console_facts_actor ON console_action_facts(tenant_id,actor_id,at DESC,action_id DESC);
CREATE INDEX console_facts_dirty ON console_action_facts(tenant_id,actor_id,at DESC,action_id DESC) WHERE NOT clean;
CREATE INDEX console_facts_reason ON console_action_facts(tenant_id,actor_id,operation,reason_code,at DESC,action_id DESC);
CREATE INDEX console_facts_anomaly ON console_action_facts(tenant_id,actor_id,operation,at);
CREATE TABLE console_metrics (
 tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE, bucket TIMESTAMPTZ NOT NULL,
 actor_id TEXT NOT NULL, operation TEXT NOT NULL, app TEXT NOT NULL, kind TEXT NOT NULL, reason_code TEXT NOT NULL,
 outcome TEXT NOT NULL, would TEXT NOT NULL, total BIGINT NOT NULL DEFAULT 0, cost_micros_usd BIGINT NOT NULL DEFAULT 0,
 kept_micros_usd BIGINT NOT NULL DEFAULT 0, last_at TIMESTAMPTZ NOT NULL,
 updated_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(), exported_at TIMESTAMPTZ,
 PRIMARY KEY(tenant_id,bucket,actor_id,operation,app,kind,reason_code,outcome,would)
);
CREATE INDEX console_metrics_range ON console_metrics(tenant_id,bucket);
CREATE INDEX console_metrics_export ON console_metrics(updated_at) WHERE exported_at IS NULL;
CREATE TABLE console_resource_metrics (
 tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE, bucket TIMESTAMPTZ NOT NULL, resource_id TEXT NOT NULL,
 actor_id TEXT NOT NULL, kind TEXT NOT NULL, total BIGINT NOT NULL DEFAULT 0,
 PRIMARY KEY(tenant_id,bucket,resource_id,actor_id,kind)
);
CREATE INDEX console_resource_metrics_range ON console_resource_metrics(tenant_id,bucket);
CREATE TABLE console_cost_metrics (
 tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE, bucket TIMESTAMPTZ NOT NULL,
 actor_id TEXT NOT NULL, app TEXT NOT NULL, operation TEXT NOT NULL, cost_micros_usd BIGINT NOT NULL DEFAULT 0,
 PRIMARY KEY(tenant_id,bucket,actor_id,app,operation)
);
CREATE INDEX costs_console_time ON costs(tenant_id,observed_at);
CREATE TABLE resource_lease_events (
 tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE, id BIGSERIAL, resource_id TEXT NOT NULL,
 action_id TEXT NOT NULL, at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(), PRIMARY KEY(tenant_id,id)
);
CREATE INDEX resource_lease_events_range ON resource_lease_events(tenant_id,resource_id,at);

-- +goose StatementBegin
CREATE FUNCTION console_kind(d TEXT) RETURNS TEXT LANGUAGE sql IMMUTABLE AS $$
 SELECT CASE d WHEN 'DEDUPLICATE' THEN 'stop' WHEN 'REJECT' THEN 'stop' WHEN 'WAIT' THEN 'wait' WHEN 'REPLAN' THEN 'recheck' WHEN 'REQUIRE_APPROVAL' THEN 'ask' ELSE 'allow' END
$$;
CREATE FUNCTION console_metric_delta(f console_action_facts, sign BIGINT) RETURNS void LANGUAGE plpgsql AS $$
DECLARE rid TEXT;
BEGIN
 INSERT INTO console_metrics(tenant_id,bucket,actor_id,operation,app,kind,reason_code,outcome,would,total,cost_micros_usd,kept_micros_usd,last_at)
 VALUES(f.tenant_id,f.bucket,f.actor_id,f.operation,f.app,f.kind,f.reason_code,f.outcome,f.would,sign,sign*f.cost_micros_usd,sign*f.kept_micros_usd,f.at)
 ON CONFLICT(tenant_id,bucket,actor_id,operation,app,kind,reason_code,outcome,would) DO UPDATE SET
 total=console_metrics.total+EXCLUDED.total,cost_micros_usd=console_metrics.cost_micros_usd+EXCLUDED.cost_micros_usd,
 kept_micros_usd=console_metrics.kept_micros_usd+EXCLUDED.kept_micros_usd,last_at=GREATEST(console_metrics.last_at,EXCLUDED.last_at),updated_at=clock_timestamp(),exported_at=NULL;
 FOREACH rid IN ARRAY f.resources LOOP
 INSERT INTO console_resource_metrics(tenant_id,bucket,resource_id,actor_id,kind,total) VALUES(f.tenant_id,f.bucket,rid,f.actor_id,f.kind,sign)
 ON CONFLICT(tenant_id,bucket,resource_id,actor_id,kind) DO UPDATE SET total=console_resource_metrics.total+EXCLUDED.total;
 END LOOP;
END $$;
CREATE FUNCTION console_refresh_action(t UUID, aid TEXT) RETURNS void LANGUAGE plpgsql AS $$
DECLARE f console_action_facts; old_f console_action_facts; a actions; d decisions; step decisions; amount TEXT; money NUMERIC;
BEGIN
 -- Every writer of the same action serializes on its authoritative row.
 SELECT * INTO a FROM actions WHERE tenant_id=t AND id=aid FOR UPDATE;
 IF NOT FOUND THEN RETURN; END IF;
 SELECT * INTO old_f FROM console_action_facts WHERE tenant_id=t AND action_id=aid;
 IF FOUND THEN PERFORM console_metric_delta(old_f,-1); END IF;
 SELECT * INTO d FROM decisions WHERE tenant_id=t AND action_id=aid ORDER BY created_at DESC,id DESC LIMIT 1;
 step:=d;
 IF d.decision IN('ALLOW','WOULD_ALLOW') THEN
  SELECT * INTO step FROM decisions WHERE tenant_id=t AND action_id=aid AND decision NOT IN('ALLOW','WOULD_ALLOW') ORDER BY created_at DESC,id DESC LIMIT 1;
 END IF;
 f.tenant_id:=t;f.action_id:=aid;f.at:=a.requested_at;
 -- Fifteen-minute cells preserve local hour boundaries, including :30 and :45 zones.
 f.bucket:=date_bin(interval '15 minutes',a.requested_at,'2000-01-01'::timestamptz);
 f.actor_id:=a.actor_id;f.operation:=a.operation;f.app:=lower(trim(COALESCE(NULLIF(a.metadata->>'app',''),split_part(a.operation,'.',1))));
 f.kind:=console_kind(step.decision);f.reason_code:=COALESCE(step.reason_code,d.reason_code,'NO_DECISION');
 SELECT COALESCE(status,'UNKNOWN') INTO f.outcome FROM outcomes WHERE tenant_id=t AND action_id=aid;
 f.outcome:=COALESCE(f.outcome,'UNKNOWN');
 f.would:=CASE WHEN step.decision LIKE 'WOULD_%' THEN console_kind(substring(step.decision FROM 7)) ELSE '' END;
 IF f.would='allow' THEN f.would:=''; END IF;
 SELECT COALESCE(sum(amount_micros_usd),0) INTO f.cost_micros_usd FROM costs WHERE tenant_id=t AND action_id=aid;
 SELECT COALESCE(array_agg(DISTINCT resource_id ORDER BY resource_id),'{}') INTO f.resources FROM action_resources WHERE tenant_id=t AND action_id=aid;
 f.kept_micros_usd:=0;
 IF step.decision='DEDUPLICATE' AND a.operation ~* '(refund|charge|payment|payout|invoice|transfer|credit)' THEN
  amount:=COALESCE(a.metadata->>'amount',a.metadata->>'amount_usd',a.metadata->>'value',a.metadata->>'total');
  IF amount ~ '^[0-9]+(\.[0-9]+)?$' THEN money:=amount::numeric*1000000;f.kept_micros_usd:=LEAST(money,9223372036854775807)::bigint;END IF;
 END IF;
 f.clean:=f.kind='allow' AND f.outcome NOT IN('MISMATCH','PARTIAL','FAILED');
 INSERT INTO console_action_facts VALUES(f.*) ON CONFLICT(tenant_id,action_id) DO UPDATE SET
 at=EXCLUDED.at,bucket=EXCLUDED.bucket,actor_id=EXCLUDED.actor_id,operation=EXCLUDED.operation,app=EXCLUDED.app,
 kind=EXCLUDED.kind,reason_code=EXCLUDED.reason_code,outcome=EXCLUDED.outcome,would=EXCLUDED.would,
 cost_micros_usd=EXCLUDED.cost_micros_usd,kept_micros_usd=EXCLUDED.kept_micros_usd,resources=EXCLUDED.resources,clean=EXCLUDED.clean;
 PERFORM console_metric_delta(f,1);
END $$;
CREATE FUNCTION console_refresh_trigger() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 PERFORM console_refresh_action(NEW.tenant_id,CASE WHEN TG_TABLE_NAME='actions' THEN to_jsonb(NEW)->>'id' ELSE to_jsonb(NEW)->>'action_id' END);
 RETURN NEW;
END $$;
CREATE FUNCTION console_lease_event() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 INSERT INTO resource_lease_events(tenant_id,resource_id,action_id)VALUES(NEW.tenant_id,NEW.resource_id,NEW.action_id);
 RETURN NEW;
END $$;
CREATE FUNCTION console_cost_delta(c costs, sign BIGINT) RETURNS void LANGUAGE plpgsql AS $$
DECLARE cost_actor TEXT; cost_app TEXT; cost_operation TEXT;
BEGIN
 IF NOT EXISTS(SELECT 1 FROM tenants WHERE id=c.tenant_id) THEN RETURN;END IF;
 SELECT COALESCE(c.actor_id,a.actor_id,w.actor_id,''),lower(trim(COALESCE(NULLIF(a.metadata->>'app',''),split_part(a.operation,'.',1),c.metadata->>'app',''))),COALESCE(a.operation,'')
 INTO cost_actor,cost_app,cost_operation FROM (SELECT 1)seed LEFT JOIN actions a ON a.tenant_id=c.tenant_id AND a.id=c.action_id LEFT JOIN work w ON w.tenant_id=c.tenant_id AND w.id=c.work_id;
 INSERT INTO console_cost_metrics(tenant_id,bucket,actor_id,app,operation,cost_micros_usd)
 VALUES(c.tenant_id,date_bin(interval '15 minutes',c.observed_at,'2000-01-01'::timestamptz),cost_actor,cost_app,cost_operation,sign*c.amount_micros_usd)
 ON CONFLICT(tenant_id,bucket,actor_id,app,operation)DO UPDATE SET cost_micros_usd=console_cost_metrics.cost_micros_usd+EXCLUDED.cost_micros_usd;
END $$;
CREATE FUNCTION console_cost_trigger() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP<>'INSERT' THEN
  PERFORM console_cost_delta(OLD,-1);
  IF TG_OP='DELETE' OR OLD.action_id IS DISTINCT FROM NEW.action_id THEN PERFORM console_refresh_action(OLD.tenant_id,OLD.action_id);END IF;
 END IF;
 IF TG_OP<>'DELETE' THEN PERFORM console_cost_delta(NEW,1);PERFORM console_refresh_action(NEW.tenant_id,NEW.action_id);RETURN NEW;END IF;
 RETURN OLD;
END $$;
CREATE FUNCTION console_spent(t UUID, actor TEXT, f TIMESTAMPTZ, e TIMESTAMPTZ) RETURNS BIGINT LANGUAGE sql STABLE AS $$
 WITH cells AS(
 SELECT cost_micros_usd FROM console_cost_metrics WHERE tenant_id=t AND bucket>=f AND bucket+interval '15 minutes'<=e AND (actor='' OR actor_id=actor)
 UNION ALL SELECT c.amount_micros_usd FROM costs c LEFT JOIN actions a ON a.tenant_id=c.tenant_id AND a.id=c.action_id LEFT JOIN work w ON w.tenant_id=c.tenant_id AND w.id=c.work_id
 WHERE c.tenant_id=t AND c.observed_at>=f AND c.observed_at<e AND ((f<>date_bin(interval '15 minutes',f,'2000-01-01'::timestamptz) AND c.observed_at<date_bin(interval '15 minutes',f,'2000-01-01'::timestamptz)+interval '15 minutes') OR (e<>date_bin(interval '15 minutes',e,'2000-01-01'::timestamptz) AND c.observed_at>=date_bin(interval '15 minutes',e,'2000-01-01'::timestamptz))) AND (actor='' OR COALESCE(c.actor_id,a.actor_id,w.actor_id,'')=actor))
 SELECT COALESCE(sum(cost_micros_usd),0)::bigint FROM cells
$$;
-- +goose StatementEnd
CREATE TRIGGER console_actions AFTER INSERT OR UPDATE OF status,metadata ON actions FOR EACH ROW EXECUTE FUNCTION console_refresh_trigger();
CREATE TRIGGER console_decisions AFTER INSERT ON decisions FOR EACH ROW EXECUTE FUNCTION console_refresh_trigger();
CREATE TRIGGER console_outcomes AFTER INSERT OR UPDATE OF status ON outcomes FOR EACH ROW EXECUTE FUNCTION console_refresh_trigger();
CREATE TRIGGER console_costs AFTER INSERT OR UPDATE OF amount_micros_usd,observed_at,actor_id,action_id,work_id,metadata OR DELETE ON costs FOR EACH ROW EXECUTE FUNCTION console_cost_trigger();
CREATE TRIGGER console_action_resources AFTER INSERT ON action_resources FOR EACH ROW EXECUTE FUNCTION console_refresh_trigger();
CREATE TRIGGER console_lease_history AFTER INSERT ON leases FOR EACH ROW EXECUTE FUNCTION console_lease_event();
-- Backfill existing tenants in sets; upgrading a large installation must not
-- issue one row lock and multiple aggregate updates for each historic action.
WITH latest AS(
 SELECT DISTINCT ON(tenant_id,action_id)tenant_id,action_id,decision,reason_code FROM decisions ORDER BY tenant_id,action_id,created_at DESC,id DESC),
 earlier AS(
 SELECT DISTINCT ON(tenant_id,action_id)tenant_id,action_id,decision,reason_code FROM decisions WHERE decision NOT IN('ALLOW','WOULD_ALLOW') ORDER BY tenant_id,action_id,created_at DESC,id DESC),
 chosen AS(
 SELECT a.*,CASE WHEN l.decision IN('ALLOW','WOULD_ALLOW') THEN COALESCE(e.decision,l.decision) ELSE l.decision END console_decision,
 CASE WHEN l.decision IN('ALLOW','WOULD_ALLOW') THEN COALESCE(e.reason_code,l.reason_code) ELSE l.reason_code END console_reason
 FROM actions a LEFT JOIN latest l ON l.tenant_id=a.tenant_id AND l.action_id=a.id LEFT JOIN earlier e ON e.tenant_id=a.tenant_id AND e.action_id=a.id),
 amounts AS(SELECT tenant_id,action_id,sum(amount_micros_usd)cost FROM costs WHERE action_id IS NOT NULL GROUP BY tenant_id,action_id),
 refs AS(SELECT tenant_id,action_id,array_agg(DISTINCT resource_id ORDER BY resource_id)resources FROM action_resources GROUP BY tenant_id,action_id)
INSERT INTO console_action_facts
SELECT a.tenant_id,a.id,a.requested_at,date_bin(interval '15 minutes',a.requested_at,'2000-01-01'::timestamptz),a.actor_id,a.operation,
 lower(trim(COALESCE(NULLIF(a.metadata->>'app',''),split_part(a.operation,'.',1)))),console_kind(a.console_decision),COALESCE(a.console_reason,'NO_DECISION'),COALESCE(o.status,'UNKNOWN'),
 CASE WHEN a.console_decision LIKE 'WOULD_%' AND a.console_decision<>'WOULD_ALLOW' THEN console_kind(substring(a.console_decision FROM 7)) ELSE '' END,COALESCE(c.cost,0),
 CASE WHEN a.console_decision='DEDUPLICATE' AND a.operation ~* '(refund|charge|payment|payout|invoice|transfer|credit)' AND COALESCE(a.metadata->>'amount',a.metadata->>'amount_usd',a.metadata->>'value',a.metadata->>'total') ~ '^[0-9]+(\.[0-9]+)?$'
 THEN LEAST(COALESCE(a.metadata->>'amount',a.metadata->>'amount_usd',a.metadata->>'value',a.metadata->>'total')::numeric*1000000,9223372036854775807)::bigint ELSE 0 END,
 COALESCE(r.resources,'{}'),console_kind(a.console_decision)='allow' AND COALESCE(o.status,'UNKNOWN') NOT IN('MISMATCH','PARTIAL','FAILED')
FROM chosen a LEFT JOIN outcomes o ON o.tenant_id=a.tenant_id AND o.action_id=a.id LEFT JOIN amounts c ON c.tenant_id=a.tenant_id AND c.action_id=a.id LEFT JOIN refs r ON r.tenant_id=a.tenant_id AND r.action_id=a.id;
INSERT INTO console_metrics(tenant_id,bucket,actor_id,operation,app,kind,reason_code,outcome,would,total,cost_micros_usd,kept_micros_usd,last_at)
SELECT tenant_id,bucket,actor_id,operation,app,kind,reason_code,outcome,would,count(*),sum(cost_micros_usd),sum(kept_micros_usd),max(at) FROM console_action_facts GROUP BY tenant_id,bucket,actor_id,operation,app,kind,reason_code,outcome,would;
INSERT INTO console_resource_metrics SELECT tenant_id,bucket,r,actor_id,kind,count(*) FROM console_action_facts CROSS JOIN LATERAL unnest(resources)r GROUP BY tenant_id,bucket,r,actor_id,kind;
INSERT INTO console_cost_metrics SELECT c.tenant_id,date_bin(interval '15 minutes',c.observed_at,'2000-01-01'::timestamptz),COALESCE(c.actor_id,a.actor_id,w.actor_id,''),lower(trim(COALESCE(NULLIF(a.metadata->>'app',''),split_part(a.operation,'.',1),c.metadata->>'app',''))),COALESCE(a.operation,''),sum(c.amount_micros_usd)
FROM costs c LEFT JOIN actions a ON a.tenant_id=c.tenant_id AND a.id=c.action_id LEFT JOIN work w ON w.tenant_id=c.tenant_id AND w.id=c.work_id GROUP BY 1,2,3,4,5;

-- +goose Down
DROP TRIGGER console_actions ON actions;
DROP TRIGGER console_decisions ON decisions;
DROP TRIGGER console_outcomes ON outcomes;
DROP TRIGGER console_costs ON costs;
DROP TRIGGER console_action_resources ON action_resources;
DROP TRIGGER console_lease_history ON leases;
DROP FUNCTION console_refresh_trigger(),console_lease_event(),console_refresh_action(UUID,TEXT),console_metric_delta(console_action_facts,BIGINT),console_kind(TEXT),console_cost_trigger(),console_cost_delta(costs,BIGINT),console_spent(UUID,TEXT,TIMESTAMPTZ,TIMESTAMPTZ);
DROP TABLE console_action_facts,console_metrics,console_resource_metrics,console_cost_metrics,resource_lease_events;
DROP INDEX costs_console_time;
