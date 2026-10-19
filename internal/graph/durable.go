package graph

import (
	"context"
	"lumni/pkg/protocol"
)

func (s *Service) loadDurable(ctx context.Context, tenant string, f Filter, b *builder) error {
	rows, err := s.pool.Query(ctx, `WITH selected_work AS(SELECT id FROM work WHERE tenant_id=$1 AND started_at BETWEEN $2 AND $3 ORDER BY started_at DESC LIMIT 500), entities AS(
 SELECT 'step'::text kind,st.id,st.name label,st.status,st.created_at,'work'::text parent_type,st.work_id parent_id,'HAS_STEP'::text edge FROM steps st WHERE tenant_id=$1 AND work_id IN(SELECT id FROM selected_work)
 UNION ALL SELECT 'outcome',o.id,o.id,o.status,o.created_at,'action',o.action_id,'EXPECTED_OUTCOME' FROM outcomes o WHERE tenant_id=$1 AND work_id IN(SELECT id FROM selected_work)
 UNION ALL SELECT 'effect',e.id,a.operation,e.status,e.created_at,'action',e.action_id,'PRODUCED_EFFECT' FROM effect_occurrences e JOIN actions a ON a.tenant_id=e.tenant_id AND a.id=e.action_id WHERE e.tenant_id=$1 AND a.work_id IN(SELECT id FROM selected_work)
 UNION ALL SELECT 'incident',i.id,i.title,i.status,i.created_at,'work',i.work_id,'HAS_FINDING' FROM incidents i WHERE tenant_id=$1 AND work_id IN(SELECT id FROM selected_work)
 UNION ALL SELECT 'verifier',v.id,v.name,CASE WHEN v.enabled THEN 'ENABLED' ELSE 'DISABLED' END,v.created_at,'outcome',o.id,'VERIFIED_BY' FROM verifiers v JOIN outcomes o ON o.tenant_id=v.tenant_id AND o.verifier_id=v.id WHERE v.tenant_id=$1 AND o.work_id IN(SELECT id FROM selected_work)
 )SELECT kind,id,label,status,created_at,parent_type,parent_id,edge FROM entities ORDER BY created_at DESC LIMIT 1500`, tenant, f.From, f.To)
	if err != nil {
		return err
	}
	defer rows.Close()
	var edges []protocol.GraphLink
	for rows.Next() {
		var n protocol.GraphNode
		var parentType, parentID, edge string
		if err = rows.Scan(&n.Type, &n.EntityID, &n.Label, &n.Status, &n.OccurredAt, &parentType, &parentID, &edge); err != nil {
			return err
		}
		n.ID = nodeID(n.Type, n.EntityID)
		b.addNode(n)
		edges = append(edges, protocol.GraphLink{ID: edge + ":" + parentID + ":" + n.ID, Source: nodeID(parentType, parentID), Target: n.ID, Type: edge})
	}
	if err = rows.Err(); err != nil {
		return err
	}
	rows.Close()
	for _, e := range edges {
		b.addEdge(e)
	}
	links, err := s.pool.Query(ctx, `SELECT id,action_id FROM steps WHERE tenant_id=$1 AND action_id IS NOT NULL AND work_id IN(SELECT id FROM work WHERE tenant_id=$1 AND started_at BETWEEN $2 AND $3) LIMIT 1500`, tenant, f.From, f.To)
	if err != nil {
		return err
	}
	defer links.Close()
	for links.Next() {
		var step, action string
		if err = links.Scan(&step, &action); err != nil {
			return err
		}
		b.addEdge(protocol.GraphLink{ID: "step-action:" + step, Source: nodeID("step", step), Target: nodeID("action", action), Type: "PROPOSED_ACTION"})
	}
	return links.Err()
}
