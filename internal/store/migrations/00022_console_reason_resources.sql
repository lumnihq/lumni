-- +goose Up
-- Reason examples use indexed action lookups; their top resource must not
-- rescan all matching action payloads for every dashboard request.
CREATE TABLE console_reason_resource_metrics (
 tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
 bucket TIMESTAMPTZ NOT NULL, actor_id TEXT NOT NULL, operation TEXT NOT NULL,
 app TEXT NOT NULL, kind TEXT NOT NULL, reason_code TEXT NOT NULL,
 resource_id TEXT NOT NULL, total BIGINT NOT NULL DEFAULT 0,
 PRIMARY KEY(tenant_id,bucket,actor_id,operation,app,kind,reason_code,resource_id)
);
CREATE INDEX console_reason_resource_range ON console_reason_resource_metrics
 (tenant_id,actor_id,operation,app,kind,reason_code,bucket);

-- +goose StatementBegin
CREATE FUNCTION console_reason_resource_delta(f console_action_facts, sign BIGINT) RETURNS void LANGUAGE plpgsql AS $$
DECLARE rid TEXT; effective_kind TEXT;
BEGIN
 IF NOT EXISTS(SELECT 1 FROM tenants WHERE id=f.tenant_id) THEN RETURN; END IF;
 effective_kind:=CASE WHEN f.kind='allow' AND f.outcome IN('MISMATCH','PARTIAL','FAILED') THEN 'bad' ELSE f.kind END;
 FOREACH rid IN ARRAY f.resources LOOP
  INSERT INTO console_reason_resource_metrics(tenant_id,bucket,actor_id,operation,app,kind,reason_code,resource_id,total)
  VALUES(f.tenant_id,f.bucket,f.actor_id,f.operation,f.app,effective_kind,f.reason_code,rid,sign)
  ON CONFLICT(tenant_id,bucket,actor_id,operation,app,kind,reason_code,resource_id)
  DO UPDATE SET total=console_reason_resource_metrics.total+EXCLUDED.total;
 END LOOP;
END $$;
CREATE FUNCTION console_reason_resource_trigger() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP<>'INSERT' THEN PERFORM console_reason_resource_delta(OLD,-1); END IF;
 IF TG_OP<>'DELETE' THEN PERFORM console_reason_resource_delta(NEW,1); RETURN NEW; END IF;
 RETURN OLD;
END $$;
-- +goose StatementEnd
CREATE TRIGGER console_reason_resources AFTER INSERT OR UPDATE OR DELETE ON console_action_facts
 FOR EACH ROW EXECUTE FUNCTION console_reason_resource_trigger();

INSERT INTO console_reason_resource_metrics
 SELECT tenant_id,bucket,actor_id,operation,app,
 CASE WHEN kind='allow' AND outcome IN('MISMATCH','PARTIAL','FAILED') THEN 'bad' ELSE kind END,
 reason_code,rid,count(*) FROM console_action_facts CROSS JOIN LATERAL unnest(resources)rid
 GROUP BY 1,2,3,4,5,6,7,8;

-- +goose Down
DROP TRIGGER console_reason_resources ON console_action_facts;
DROP FUNCTION console_reason_resource_trigger(),console_reason_resource_delta(console_action_facts,BIGINT);
DROP TABLE console_reason_resource_metrics;
