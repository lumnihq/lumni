package graph

import (
	"context"
	"encoding/json"
	"lumni/pkg/protocol"
	"time"
)

// PagedSnapshot traverses the window by stable node identity. Edges may refer
// to a node on another page; joining pages reconstructs the complete graph.
func (s *Service) PagedSnapshot(ctx context.Context, tenant string, f Filter, after string) (protocol.GraphSnapshot, string, error) {
	b := newBuilder(f.Lens)
	types := []string{}
	for _, t := range []string{"agent", "session", "work", "action", "resource", "decision", "evaluation", "approval", "lease", "step", "effect", "outcome", "verifier", "incident", "observation", "provider", "model", "waste"} {
		if b.accepts(t) {
			types = append(types, t)
		}
	}
	q := `WITH entities AS (
 SELECT 'action' type,a.id entity_id,a.operation label,a.status,a.requested_at at,a.metadata||jsonb_build_object('actor_id',a.actor_id) metadata,a.risk FROM actions a WHERE tenant_id=$1 AND requested_at>=$2 AND requested_at<$3
 UNION ALL SELECT 'work',id,goal,status,started_at,metadata,risk FROM work WHERE tenant_id=$1 AND started_at>=$2 AND started_at<$3
 UNION ALL SELECT 'agent',id,name,'',created_at,metadata,NULL FROM agents WHERE tenant_id=$1 AND created_at<$3
 UNION ALL SELECT 'session',id,runtime,status,started_at,metadata,NULL FROM sessions WHERE tenant_id=$1 AND started_at>=$2 AND started_at<$3
 UNION ALL SELECT 'resource',r.id,COALESCE(r.metadata->>'name',r.external_id),NULL,r.updated_at,to_jsonb(r)-'tenant_id',NULL FROM resources r WHERE r.tenant_id=$1 AND (r.updated_at>=$2 AND r.updated_at<$3 OR EXISTS(SELECT 1 FROM action_resources ar JOIN actions a ON a.tenant_id=ar.tenant_id AND a.id=ar.action_id WHERE ar.tenant_id=r.tenant_id AND ar.resource_id=r.id AND a.requested_at>=$2 AND a.requested_at<$3))
 UNION ALL SELECT 'decision',d.id::text,d.decision,d.decision,d.created_at,jsonb_build_object('reason_code',d.reason_code,'enforced',d.enforced,'evaluations',d.evaluations,'action_id',d.action_id),NULL FROM decisions d JOIN actions a ON a.tenant_id=d.tenant_id AND a.id=d.action_id WHERE d.tenant_id=$1 AND a.requested_at>=$2 AND a.requested_at<$3
 UNION ALL SELECT 'evaluation',d.id::text||':'||lpad((e.n-1)::text,2,'0'),e.v->>'stage',e.v->>'outcome',d.created_at,COALESCE(e.v->'details','{}'),NULL FROM decisions d JOIN actions a ON a.tenant_id=d.tenant_id AND a.id=d.action_id CROSS JOIN LATERAL jsonb_array_elements(d.evaluations)WITH ORDINALITY e(v,n) WHERE d.tenant_id=$1 AND a.requested_at>=$2 AND a.requested_at<$3
 UNION ALL SELECT 'observation',o.id::text,o.observed_version,'OBSERVED',o.observed_at,to_jsonb(o)-'tenant_id'-'metadata',NULL FROM observations o WHERE tenant_id=$1 AND observed_at>=$2 AND observed_at<$3
 UNION ALL SELECT 'approval',id::text,reason_code,status,requested_at,to_jsonb(p)-'tenant_id',NULL FROM approvals p WHERE tenant_id=$1 AND requested_at>=$2 AND requested_at<$3
 UNION ALL SELECT 'lease',id::text,mode,'ACTIVE',created_at,to_jsonb(l)-'tenant_id'-'token_hash',NULL FROM leases l WHERE tenant_id=$1 AND created_at>=$2 AND created_at<$3
 UNION ALL SELECT 'outcome',o.id,o.reason,o.status,o.created_at,to_jsonb(o)-'tenant_id'-'claim_token'-'claim_until',NULL FROM outcomes o JOIN actions a ON a.tenant_id=o.tenant_id AND a.id=o.action_id WHERE o.tenant_id=$1 AND a.requested_at>=$2 AND a.requested_at<$3
 UNION ALL SELECT 'verifier',id,name,CASE WHEN enabled THEN 'ENABLED' ELSE 'DISABLED' END,created_at,to_jsonb(v)-'tenant_id'-'secret_ciphertext',NULL FROM verifiers v WHERE tenant_id=$1 AND created_at<$3
 UNION ALL SELECT 'incident',id,title,status,created_at,evidence,NULL FROM incidents WHERE tenant_id=$1 AND created_at>=$2 AND created_at<$3
 UNION ALL SELECT 'step',id,key,status,created_at,'{}',NULL FROM (SELECT *,step_key key FROM steps) st WHERE tenant_id=$1 AND created_at>=$2 AND created_at<$3
 UNION ALL SELECT 'effect',id,effect_key,status,created_at,'{}',NULL FROM effect_occurrences WHERE tenant_id=$1 AND created_at>=$2 AND created_at<$3
 UNION ALL SELECT 'provider',provider,provider,NULL,max(observed_at),jsonb_build_object('cost_micros_usd',sum(amount_micros_usd)),NULL FROM costs WHERE tenant_id=$1 AND observed_at>=$2 AND observed_at<$3 GROUP BY provider
 UNION ALL SELECT 'model',provider||':'||model,model,NULL,max(observed_at),jsonb_build_object('cost_micros_usd',sum(amount_micros_usd)),NULL FROM costs WHERE tenant_id=$1 AND observed_at>=$2 AND observed_at<$3 AND COALESCE(model,'')<>'' GROUP BY provider,model
 UNION ALL SELECT 'waste',waste_reason,waste_reason,'WASTE',max(observed_at),jsonb_build_object('cost_micros_usd',sum(amount_micros_usd)),NULL FROM costs WHERE tenant_id=$1 AND observed_at>=$2 AND observed_at<$3 AND COALESCE(waste_reason,'')<>'' GROUP BY waste_reason
 )SELECT jsonb_strip_nulls(jsonb_build_object('id',type||':'||entity_id,'entity_id',entity_id,'type',type,'label',label,'status',status,'occurred_at',at,'metadata',metadata,'risk',risk,'cost_micros_usd',CASE WHEN type IN('provider','model','waste') THEN (metadata->>'cost_micros_usd')::bigint END)),type||':'||entity_id FROM entities WHERE type=ANY($4) AND type||':'||entity_id>$5 AND ($7='' OR status=$7) AND ($8='' OR risk=$8) ORDER BY type||':'||entity_id LIMIT $6`
	out := protocol.GraphSnapshot{SchemaVersion: protocol.SchemaVersion, Lens: f.Lens, Nodes: []protocol.GraphNode{}, Edges: []protocol.GraphLink{}, Attention: []protocol.AttentionItem{}, Limit: f.Limit, AsOf: time.Now().UTC()}
	if out.Lens == "" {
		out.Lens = protocol.GraphLensLive
	}
	rows, err := s.pool.Query(ctx, q, tenant, f.From, f.To, types, after, f.Limit+1, f.Status, string(f.Risk))
	if err != nil {
		return out, "", err
	}
	keys := []string{}
	last := ""
	next := ""
	for rows.Next() {
		var data []byte
		var key string
		if err = rows.Scan(&data, &key); err != nil {
			break
		}
		if len(out.Nodes) == f.Limit {
			next = last
			out.Truncated = true
			break
		}
		var n protocol.GraphNode
		if err = json.Unmarshal(data, &n); err != nil {
			break
		}
		out.Nodes = append(out.Nodes, n)
		keys = append(keys, key)
		last = key
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return out, "", err
	}
	edges, err := s.pool.Query(ctx, `WITH links AS (
 SELECT 'work-action:'||id id,'work:'||work_id source,'action:'||id target,'PROPOSES' type FROM actions WHERE tenant_id=$1 AND ('work:'||work_id=ANY($2) OR 'action:'||id=ANY($2))
 UNION ALL SELECT 'action-resource:'||action_id||':'||resource_id,'action:'||action_id,'resource:'||resource_id,mode FROM action_resources WHERE tenant_id=$1 AND ('action:'||action_id=ANY($2) OR 'resource:'||resource_id=ANY($2))
 UNION ALL SELECT 'action-decision:'||id::text,'action:'||action_id,'decision:'||id::text,'EVALUATED_AS' FROM decisions WHERE tenant_id=$1 AND ('action:'||action_id=ANY($2) OR 'decision:'||id::text=ANY($2))
 UNION ALL SELECT 'evaluation-edge:'||d.id::text||':'||lpad((e.n-1)::text,2,'0'),CASE WHEN e.n=1 THEN 'decision:'||d.id::text ELSE 'evaluation:'||d.id::text||':'||lpad((e.n-2)::text,2,'0') END,'evaluation:'||d.id::text||':'||lpad((e.n-1)::text,2,'0'),'NEXT_STAGE' FROM decisions d CROSS JOIN LATERAL jsonb_array_elements(d.evaluations)WITH ORDINALITY e(v,n) WHERE d.tenant_id=$1 AND ('decision:'||d.id::text=ANY($2) OR 'evaluation:'||d.id::text||':'||lpad((e.n-1)::text,2,'0')=ANY($2) OR 'evaluation:'||d.id::text||':'||lpad((e.n-2)::text,2,'0')=ANY($2))
 UNION ALL SELECT 'observation-resource:'||id::text,'observation:'||id::text,'resource:'||resource_id,'OBSERVED' FROM observations WHERE tenant_id=$1 AND ('observation:'||id::text=ANY($2) OR 'resource:'||resource_id=ANY($2))
 UNION ALL SELECT 'action-approval:'||id::text,'action:'||action_id,'approval:'||id::text,'REQUIRES_APPROVAL' FROM approvals WHERE tenant_id=$1 AND ('action:'||action_id=ANY($2) OR 'approval:'||id::text=ANY($2))
 UNION ALL SELECT 'action-outcome:'||id,'action:'||action_id,'outcome:'||id,'HAS_OUTCOME' FROM outcomes WHERE tenant_id=$1 AND ('action:'||action_id=ANY($2) OR 'outcome:'||id=ANY($2))
 UNION ALL SELECT 'outcome-verifier:'||id,'outcome:'||id,'verifier:'||verifier_id,'VERIFIED_BY' FROM outcomes WHERE tenant_id=$1 AND verifier_id IS NOT NULL AND ('outcome:'||id=ANY($2) OR 'verifier:'||verifier_id=ANY($2))
 UNION ALL SELECT 'lease-action:'||id::text,'action:'||action_id,'lease:'||id::text,'HOLDS' FROM leases WHERE tenant_id=$1 AND ('action:'||action_id=ANY($2) OR 'lease:'||id::text=ANY($2))
 UNION ALL SELECT 'work-step:'||id,'work:'||work_id,'step:'||id,'HAS_STEP' FROM steps WHERE tenant_id=$1 AND ('work:'||work_id=ANY($2) OR 'step:'||id=ANY($2))
 UNION ALL SELECT 'step-action:'||id,'step:'||id,'action:'||action_id,'PROPOSED_ACTION' FROM steps WHERE tenant_id=$1 AND action_id IS NOT NULL AND ('step:'||id=ANY($2) OR 'action:'||action_id=ANY($2))
 UNION ALL SELECT 'effect-action:'||id,'action:'||action_id,'effect:'||id,'PRODUCED_EFFECT' FROM effect_occurrences WHERE tenant_id=$1 AND ('action:'||action_id=ANY($2) OR 'effect:'||id=ANY($2))
 UNION ALL SELECT 'work-incident:'||id,'work:'||work_id,'incident:'||id,'HAS_FINDING' FROM incidents WHERE tenant_id=$1 AND work_id IS NOT NULL AND ('work:'||work_id=ANY($2) OR 'incident:'||id=ANY($2))
 UNION ALL SELECT 'resource-edge:'||source_resource_id||':'||relationship||':'||target_resource_id,'resource:'||source_resource_id,'resource:'||target_resource_id,relationship FROM resource_edges WHERE tenant_id=$1 AND ('resource:'||source_resource_id=ANY($2) OR 'resource:'||target_resource_id=ANY($2))
 UNION ALL SELECT DISTINCT 'provider-model:'||provider||':'||model,'provider:'||provider,'model:'||provider||':'||model,'BILLS' FROM costs WHERE tenant_id=$1 AND COALESCE(model,'')<>'' AND ('provider:'||provider=ANY($2) OR 'model:'||provider||':'||model=ANY($2))
 UNION ALL SELECT DISTINCT 'provider-waste:'||provider||':'||waste_reason,'provider:'||provider,'waste:'||waste_reason,'ATTRIBUTED_WASTE' FROM costs WHERE tenant_id=$1 AND COALESCE(waste_reason,'')<>'' AND ('provider:'||provider=ANY($2) OR 'waste:'||waste_reason=ANY($2))
 )SELECT id,source,target,type FROM links`, tenant, keys)
	if err != nil {
		return out, "", err
	}
	defer edges.Close()
	for edges.Next() {
		var e protocol.GraphLink
		if err = edges.Scan(&e.ID, &e.Source, &e.Target, &e.Type); err != nil {
			return out, "", err
		}
		out.Edges = append(out.Edges, e)
	}
	if err = edges.Err(); err != nil {
		return out, "", err
	}
	_ = s.pool.QueryRow(ctx, `SELECT COALESCE(max(sequence),0) FROM graph_changes WHERE tenant_id=$1`, tenant).Scan(&out.Cursor)
	return out, next, nil
}
