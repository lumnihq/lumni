// Package graph builds tenant-scoped operational projections from authoritative
// PostgreSQL state. It deliberately stays off the admission transaction path.
package graph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/lumnihq/lumni/pkg/protocol"
)

type Filter struct {
	Lens      protocol.GraphLens
	From      time.Time
	To        time.Time
	FocusType string
	FocusID   string
	Depth     int
	Status    string
	Risk      protocol.RiskLevel
	Limit     int
}

type Service struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Service { return &Service{pool: pool} }

type builder struct {
	nodes     map[string]protocol.GraphNode
	edges     map[string]protocol.GraphLink
	attention []protocol.AttentionItem
	lens      protocol.GraphLens
}

func newBuilder(lens protocol.GraphLens) *builder {
	return &builder{nodes: map[string]protocol.GraphNode{}, edges: map[string]protocol.GraphLink{}, lens: lens}
}

func nodeID(kind, id string) string { return strings.ToLower(kind) + ":" + id }

func (b *builder) accepts(kind string) bool {
	kind = strings.ToLower(kind)
	if contains([]string{"step", "effect", "outcome", "verifier", "incident"}, kind) {
		return true
	}
	switch b.lens {
	case protocol.GraphLensAgents:
		return contains([]string{"agent", "session", "work", "action"}, kind)
	case protocol.GraphLensWork:
		return contains([]string{"agent", "work", "action", "resource"}, kind)
	case protocol.GraphLensResources:
		return contains([]string{"resource", "action", "observation", "lease"}, kind)
	case protocol.GraphLensTransaction:
		return contains([]string{"action", "decision", "evaluation", "approval", "lease", "resource"}, kind)
	case protocol.GraphLensFinOps:
		return contains([]string{"agent", "work", "action", "provider", "model", "waste"}, kind)
	default:
		return contains([]string{"agent", "work", "action", "resource", "approval", "lease"}, kind)
	}
}

func (b *builder) addNode(node protocol.GraphNode) {
	if b.accepts(node.Type) {
		b.nodes[node.ID] = node
	}
}

func (b *builder) addEdge(edge protocol.GraphLink) {
	if _, ok := b.nodes[edge.Source]; !ok {
		return
	}
	if _, ok := b.nodes[edge.Target]; !ok {
		return
	}
	b.edges[edge.ID] = edge
}

func (s *Service) Snapshot(ctx context.Context, tenantID string, f Filter) (protocol.GraphSnapshot, error) {
	now := time.Now().UTC()
	if f.Lens == "" {
		f.Lens = protocol.GraphLensLive
	}
	if f.To.IsZero() || f.To.After(now) {
		f.To = now
	}
	if f.From.IsZero() {
		f.From = f.To.Add(-24 * time.Hour)
	}
	if f.Depth <= 0 {
		f.Depth = 2
	}
	if f.Depth > 5 {
		f.Depth = 5
	}
	if f.Limit <= 0 {
		f.Limit = 500
	}
	if f.Limit > 500 {
		f.Limit = 500
	}

	b := newBuilder(f.Lens)
	if err := s.loadAgents(ctx, tenantID, f, b); err != nil {
		return protocol.GraphSnapshot{}, err
	}
	if err := s.loadSessions(ctx, tenantID, f, b); err != nil {
		return protocol.GraphSnapshot{}, err
	}
	if err := s.loadWork(ctx, tenantID, f, b); err != nil {
		return protocol.GraphSnapshot{}, err
	}
	if err := s.loadActions(ctx, tenantID, f, b); err != nil {
		return protocol.GraphSnapshot{}, err
	}
	if err := s.loadWasteAttention(ctx, tenantID, f, b); err != nil {
		return protocol.GraphSnapshot{}, err
	}
	if err := s.loadResources(ctx, tenantID, f, b); err != nil {
		return protocol.GraphSnapshot{}, err
	}
	if err := s.loadGovernance(ctx, tenantID, f, b); err != nil {
		return protocol.GraphSnapshot{}, err
	}
	if err := s.loadDomainEdges(ctx, tenantID, f, b); err != nil {
		return protocol.GraphSnapshot{}, err
	}
	if f.Lens == protocol.GraphLensFinOps {
		if err := s.loadCostDimensions(ctx, tenantID, f, b); err != nil {
			return protocol.GraphSnapshot{}, err
		}
	}

	if err := s.loadDurable(ctx, tenantID, f, b); err != nil {
		return protocol.GraphSnapshot{}, err
	}
	nodes, edges := filtered(b, f)
	if nodes == nil {
		nodes = []protocol.GraphNode{}
	}
	if edges == nil {
		edges = []protocol.GraphLink{}
	}
	if b.attention == nil {
		b.attention = []protocol.AttentionItem{}
	}
	truncated := len(nodes) > f.Limit
	if truncated {
		nodes, edges = truncate(nodes, edges, b.attention, f.Limit)
	}
	sort.Slice(b.attention, func(i, j int) bool {
		if b.attention[i].Priority == b.attention[j].Priority {
			return timeValue(b.attention[i].OccurredAt).After(timeValue(b.attention[j].OccurredAt))
		}
		return b.attention[i].Priority > b.attention[j].Priority
	})
	var cursor int64
	_ = s.pool.QueryRow(ctx, `SELECT COALESCE(max(sequence),0) FROM graph_changes WHERE tenant_id=$1`, tenantID).Scan(&cursor)
	return protocol.GraphSnapshot{
		SchemaVersion: protocol.SchemaVersion,
		Lens:          f.Lens,
		Nodes:         nodes,
		Edges:         edges,
		Attention:     b.attention,
		AsOf:          now,
		Cursor:        cursor,
		Truncated:     truncated,
		Limit:         f.Limit,
		Analytics: protocol.AnalyticsFreshness{
			Source: "postgres", AsOf: now, LagSeconds: 0,
		},
	}, nil
}

func (s *Service) loadAgents(ctx context.Context, tenantID string, f Filter, b *builder) error {
	if !b.accepts("agent") {
		return nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT ag.id,ag.name,ag.framework,ag.model,ag.updated_at,ag.metadata,
			CASE
			 WHEN EXISTS(SELECT 1 FROM work w JOIN actions a ON a.tenant_id=w.tenant_id AND a.work_id=w.id WHERE w.tenant_id=ag.tenant_id AND w.agent_id=ag.id AND w.status IN('RUNNING','WAITING','BLOCKED') AND a.status IN('FAILED','UNKNOWN')) THEN 'FAILED_OR_UNKNOWN'
			 WHEN EXISTS(SELECT 1 FROM work w JOIN actions a ON a.tenant_id=w.tenant_id AND a.work_id=w.id WHERE w.tenant_id=ag.tenant_id AND w.agent_id=ag.id AND w.status IN('RUNNING','WAITING','BLOCKED') AND a.status='AWAITING_APPROVAL') THEN 'AWAITING_APPROVAL'
			 WHEN EXISTS(SELECT 1 FROM work w JOIN actions a ON a.tenant_id=w.tenant_id AND a.work_id=w.id JOIN decisions d ON d.tenant_id=a.tenant_id AND d.action_id=a.id WHERE w.tenant_id=ag.tenant_id AND w.agent_id=ag.id AND w.status IN('RUNNING','WAITING','BLOCKED') AND d.decision IN('REPLAN','WOULD_REPLAN')) THEN 'REPLAN'
			 WHEN EXISTS(SELECT 1 FROM work w JOIN actions a ON a.tenant_id=w.tenant_id AND a.work_id=w.id JOIN decisions d ON d.tenant_id=a.tenant_id AND d.action_id=a.id WHERE w.tenant_id=ag.tenant_id AND w.agent_id=ag.id AND w.status IN('RUNNING','WAITING','BLOCKED') AND d.decision IN('WAIT','WOULD_WAIT')) THEN 'WAITING'
			 WHEN EXISTS(SELECT 1 FROM work w JOIN actions a ON a.tenant_id=w.tenant_id AND a.work_id=w.id WHERE w.tenant_id=ag.tenant_id AND w.agent_id=ag.id AND a.status='ACTIVE') THEN 'ACTIVE'
			 WHEN EXISTS(SELECT 1 FROM work w JOIN actions a ON a.tenant_id=w.tenant_id AND a.work_id=w.id WHERE w.tenant_id=ag.tenant_id AND w.agent_id=ag.id AND a.status='RESERVED') THEN 'RESERVED'
			 WHEN EXISTS(SELECT 1 FROM work w WHERE w.tenant_id=ag.tenant_id AND w.agent_id=ag.id AND w.status IN('RUNNING','WAITING','BLOCKED')) THEN 'IDLE'
			 ELSE 'COMPLETED' END,
			COALESCE((SELECT sum(c.amount_micros_usd) FROM costs c WHERE c.tenant_id=ag.tenant_id AND c.agent_id=ag.id AND c.observed_at BETWEEN $2 AND $3),0)
		FROM agents ag WHERE ag.tenant_id=$1 ORDER BY ag.updated_at DESC LIMIT 500`, tenantID, f.From, f.To)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, name, framework, model, state string
		var updated time.Time
		var metadata []byte
		var cost int64
		if err := rows.Scan(&id, &name, &framework, &model, &updated, &metadata, &state, &cost); err != nil {
			return err
		}
		subtitle := strings.TrimSpace(strings.Join([]string{framework, model}, " · "))
		b.addNode(protocol.GraphNode{ID: nodeID("agent", id), EntityID: id, Type: "agent", Label: name, Subtitle: subtitle, Status: state, AgentState: state, CostMicros: cost, OccurredAt: &updated, Metadata: metadata})
	}
	return rows.Err()
}

func (s *Service) loadSessions(ctx context.Context, tenantID string, f Filter, b *builder) error {
	if !b.accepts("session") {
		return nil
	}
	rows, err := s.pool.Query(ctx, `SELECT id,agent_id,status,runtime,started_at,metadata FROM sessions
		WHERE tenant_id=$1 AND (started_at BETWEEN $2 AND $3 OR status='RUNNING') ORDER BY started_at DESC LIMIT 500`, tenantID, f.From, f.To)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, status, runtime string
		var agentID *string
		var started time.Time
		var metadata []byte
		if err := rows.Scan(&id, &agentID, &status, &runtime, &started, &metadata); err != nil {
			return err
		}
		n := protocol.GraphNode{ID: nodeID("session", id), EntityID: id, Type: "session", Label: id, Subtitle: runtime, Status: status, OccurredAt: &started, Metadata: metadata}
		b.addNode(n)
		if agentID != nil {
			b.addEdge(protocol.GraphLink{ID: "agent-session:" + *agentID + ":" + id, Source: nodeID("agent", *agentID), Target: n.ID, Type: "RUNS", Label: "runs"})
		}
	}
	return rows.Err()
}

func (s *Service) loadWork(ctx context.Context, tenantID string, f Filter, b *builder) error {
	if !b.accepts("work") {
		return nil
	}
	rows, err := s.pool.Query(ctx, `SELECT id,agent_id,session_id,parent_work_id,goal,status,risk,started_at,metadata,
		COALESCE((SELECT sum(amount_micros_usd) FROM costs c WHERE c.tenant_id=w.tenant_id AND c.work_id=w.id AND c.observed_at BETWEEN $2 AND $3),0)
		FROM work w WHERE tenant_id=$1 AND (started_at BETWEEN $2 AND $3 OR status IN('RUNNING','WAITING','BLOCKED')
			OR EXISTS(SELECT 1 FROM costs c WHERE c.tenant_id=w.tenant_id AND c.work_id=w.id AND c.observed_at BETWEEN $2 AND $3))
		ORDER BY started_at DESC LIMIT 500`, tenantID, f.From, f.To)
	if err != nil {
		return err
	}
	defer rows.Close()
	type relation struct {
		work, agent     string
		session, parent *string
	}
	var relations []relation
	for rows.Next() {
		var id, goal, status string
		var risk protocol.RiskLevel
		var agentID, sessionID, parentID *string
		var started time.Time
		var metadata []byte
		var cost int64
		if err := rows.Scan(&id, &agentID, &sessionID, &parentID, &goal, &status, &risk, &started, &metadata, &cost); err != nil {
			return err
		}
		b.addNode(protocol.GraphNode{ID: nodeID("work", id), EntityID: id, Type: "work", Label: goal, Subtitle: id, Status: status, Risk: risk, CostMicros: cost, OccurredAt: &started, Metadata: metadata})
		rel := relation{work: id, session: sessionID, parent: parentID}
		if agentID != nil {
			rel.agent = *agentID
		}
		relations = append(relations, rel)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, rel := range relations {
		if rel.agent != "" {
			b.addEdge(protocol.GraphLink{ID: "agent-work:" + rel.agent + ":" + rel.work, Source: nodeID("agent", rel.agent), Target: nodeID("work", rel.work), Type: "OWNS", Label: "owns"})
		}
		if rel.session != nil {
			b.addEdge(protocol.GraphLink{ID: "session-work:" + *rel.session + ":" + rel.work, Source: nodeID("session", *rel.session), Target: nodeID("work", rel.work), Type: "CONTAINS", Label: "contains"})
		}
		if rel.parent != nil {
			b.addEdge(protocol.GraphLink{ID: "work-parent:" + *rel.parent + ":" + rel.work, Source: nodeID("work", *rel.parent), Target: nodeID("work", rel.work), Type: "PARENT_OF", Label: "parent"})
		}
	}
	return nil
}

func (s *Service) loadActions(ctx context.Context, tenantID string, f Filter, b *builder) error {
	if !b.accepts("action") && !b.accepts("decision") {
		return nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT a.id,a.work_id,a.operation,a.status,a.risk,a.requested_at,a.metadata||jsonb_build_object('actor_id',a.actor_id),
			d.id::text,d.decision,d.reason_code,d.evaluations,d.created_at,
			COALESCE((SELECT sum(c.amount_micros_usd) FROM costs c WHERE c.tenant_id=a.tenant_id AND c.action_id=a.id AND c.observed_at BETWEEN $2 AND $3),0)
		FROM actions a
		LEFT JOIN LATERAL (SELECT id,decision,reason_code,evaluations,created_at FROM decisions
			WHERE tenant_id=a.tenant_id AND action_id=a.id ORDER BY created_at DESC LIMIT 1) d ON true
		WHERE a.tenant_id=$1 AND (a.requested_at BETWEEN $2 AND $3 OR a.status IN('PROPOSED','AWAITING_APPROVAL','RESERVED','ACTIVE','UNKNOWN')
			OR EXISTS(SELECT 1 FROM costs c WHERE c.tenant_id=a.tenant_id AND c.action_id=a.id AND c.observed_at BETWEEN $2 AND $3))
		ORDER BY a.requested_at DESC LIMIT 500`, tenantID, f.From, f.To)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, workID, operation, status string
		var risk protocol.RiskLevel
		var requested time.Time
		var metadata []byte
		var receiptID, decision, reason *string
		var evaluations []byte
		var decidedAt *time.Time
		var cost int64
		if err := rows.Scan(&id, &workID, &operation, &status, &risk, &requested, &metadata, &receiptID, &decision, &reason, &evaluations, &decidedAt, &cost); err != nil {
			return err
		}
		actionNode := protocol.GraphNode{ID: nodeID("action", id), EntityID: id, Type: "action", Label: operation, Subtitle: id, Status: status, Risk: risk, CostMicros: cost, OccurredAt: &requested, Metadata: metadata}
		b.addNode(actionNode)
		b.addEdge(protocol.GraphLink{ID: "work-action:" + workID + ":" + id, Source: nodeID("work", workID), Target: actionNode.ID, Type: "PROPOSES", Label: "proposes"})
		if receiptID != nil {
			decisionNode := protocol.GraphNode{ID: nodeID("decision", *receiptID), EntityID: *receiptID, Type: "decision", Label: value(decision), Subtitle: value(reason), Status: value(decision), OccurredAt: decidedAt}
			b.addNode(decisionNode)
			b.addEdge(protocol.GraphLink{ID: "action-decision:" + id + ":" + *receiptID, Source: actionNode.ID, Target: decisionNode.ID, Type: "EVALUATED_AS", Label: value(decision)})
			var stages []protocol.DecisionEvaluation
			_ = json.Unmarshal(evaluations, &stages)
			previous := decisionNode.ID
			for index, stage := range stages {
				stageID := fmt.Sprintf("evaluation:%s:%02d", *receiptID, index)
				b.addNode(protocol.GraphNode{ID: stageID, EntityID: fmt.Sprintf("%s:%d", *receiptID, index), Type: "evaluation", Label: stage.Stage, Subtitle: string(stage.ReasonCode), Status: stage.Outcome, OccurredAt: decidedAt, Metadata: rawJSON(stage.Details)})
				b.addEdge(protocol.GraphLink{ID: fmt.Sprintf("evaluation-edge:%s:%02d", *receiptID, index), Source: previous, Target: stageID, Type: "NEXT_STAGE", Label: stage.Outcome})
				previous = stageID
			}
			b.addDecisionAttention(id, operation, risk, value(decision), value(reason), requested)
		}
		if status == "UNKNOWN" || status == "FAILED" {
			priority := 100
			if status == "FAILED" {
				priority = 90
			}
			b.attention = append(b.attention, protocol.AttentionItem{ID: "action:" + id + ":" + status, EntityType: "action", EntityID: id, Priority: priority, Category: status, Title: operation + " is " + strings.ToLower(status), Status: status, Risk: risk, OccurredAt: &requested})
		}
	}
	return rows.Err()
}

func (b *builder) addDecisionAttention(actionID, operation string, risk protocol.RiskLevel, decision, reason string, at time.Time) {
	priority, category := 0, ""
	switch decision {
	case "REQUIRE_APPROVAL", "WOULD_REQUIRE_APPROVAL":
		priority, category = 80, "APPROVAL"
	case "REPLAN", "WOULD_REPLAN":
		priority, category = 70, "REPLAN"
	case "WAIT", "WOULD_WAIT":
		priority, category = 60, "CONFLICT"
	case "REJECT", "WOULD_REJECT":
		priority, category = 90, "REJECTED"
	}
	if priority > 0 {
		b.attention = append(b.attention, protocol.AttentionItem{ID: "decision:" + actionID + ":" + decision, EntityType: "action", EntityID: actionID, Priority: priority, Category: category, Title: operation + " needs attention", Description: reason, Status: decision, Risk: risk, OccurredAt: &at})
	}
}

func (s *Service) loadWasteAttention(ctx context.Context, tenantID string, f Filter, b *builder) error {
	if !b.accepts("action") {
		return nil
	}
	rows, err := s.pool.Query(ctx, `SELECT c.id::text,c.action_id,a.operation,a.risk,c.waste_reason,c.amount_micros_usd,c.observed_at
		FROM costs c JOIN actions a ON a.tenant_id=c.tenant_id AND a.id=c.action_id
		WHERE c.tenant_id=$1 AND c.observed_at BETWEEN $2 AND $3 AND c.waste_reason IS NOT NULL AND c.waste_reason<>''
		ORDER BY c.amount_micros_usd DESC,c.observed_at DESC LIMIT 20`, tenantID, f.From, f.To)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, actionID, operation, reason string
		var risk protocol.RiskLevel
		var amount int64
		var observed time.Time
		if err := rows.Scan(&id, &actionID, &operation, &risk, &reason, &amount, &observed); err != nil {
			return err
		}
		b.attention = append(b.attention, protocol.AttentionItem{
			ID: "waste:" + id, EntityType: "action", EntityID: actionID, Priority: 40,
			Category: "HIGH_WASTE", Title: operation + " has avoidable spend",
			Description: fmt.Sprintf("%s · $%.4f", reason, float64(amount)/1_000_000),
			Status:      "WASTE", Risk: risk, OccurredAt: &observed,
		})
	}
	return rows.Err()
}

func (s *Service) loadResources(ctx context.Context, tenantID string, f Filter, b *builder) error {
	if !b.accepts("resource") && !b.accepts("observation") {
		return nil
	}
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT r.id,r.type,r.external_id,r.namespace,r.current_version,r.version_source,r.fencing_token,r.updated_at,r.metadata
		FROM resources r LEFT JOIN action_resources ar ON ar.tenant_id=r.tenant_id AND ar.resource_id=r.id
		LEFT JOIN actions a ON a.tenant_id=ar.tenant_id AND a.id=ar.action_id
		WHERE r.tenant_id=$1 AND (r.updated_at BETWEEN $2 AND $3 OR a.requested_at BETWEEN $2 AND $3 OR a.status IN('PROPOSED','AWAITING_APPROVAL','RESERVED','ACTIVE','UNKNOWN'))
		ORDER BY r.updated_at DESC LIMIT 500`, tenantID, f.From, f.To)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, typ, external, namespace string
		var version, source *string
		var fence int64
		var updated time.Time
		var metadata []byte
		if err := rows.Scan(&id, &typ, &external, &namespace, &version, &source, &fence, &updated, &metadata); err != nil {
			return err
		}
		subtitle := namespace + ":" + external
		if version != nil {
			subtitle += " @ " + *version
		}
		b.addNode(protocol.GraphNode{ID: nodeID("resource", id), EntityID: id, Type: "resource", Label: typ, Subtitle: subtitle, Status: "FENCE " + fmt.Sprint(fence), OccurredAt: &updated, Metadata: metadata})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	links, err := s.pool.Query(ctx, `SELECT ar.action_id,ar.resource_id,ar.mode,ar.operation,ar.observed_version,ar.expected_version
		FROM action_resources ar JOIN actions a ON a.tenant_id=ar.tenant_id AND a.id=ar.action_id
		WHERE ar.tenant_id=$1 AND (a.requested_at BETWEEN $2 AND $3 OR a.status IN('PROPOSED','AWAITING_APPROVAL','RESERVED','ACTIVE','UNKNOWN'))
		ORDER BY a.requested_at DESC LIMIT 1000`, tenantID, f.From, f.To)
	if err != nil {
		return err
	}
	defer links.Close()
	for links.Next() {
		var actionID, resourceID, mode, operation string
		var observed, expected *string
		if err := links.Scan(&actionID, &resourceID, &mode, &operation, &observed, &expected); err != nil {
			return err
		}
		b.addEdge(protocol.GraphLink{ID: "action-resource:" + actionID + ":" + resourceID + ":" + mode, Source: nodeID("action", actionID), Target: nodeID("resource", resourceID), Type: mode, Label: strings.ToLower(mode), Metadata: rawJSON(map[string]any{"operation": operation, "observed_version": observed, "expected_version": expected})})
	}
	return links.Err()
}

func (s *Service) loadGovernance(ctx context.Context, tenantID string, f Filter, b *builder) error {
	semanticRows, err := s.pool.Query(ctx, `
		SELECT id,kind,work_id,action_id,status,title,evidence,created_at
		FROM incidents
		WHERE tenant_id=$1 AND evidence->>'evidence_type'='SEMANTIC_INFERENCE'
		  AND (created_at BETWEEN $2 AND $3 OR status='OPEN')
		ORDER BY created_at DESC LIMIT 500`, tenantID, f.From, f.To)
	if err != nil {
		return err
	}
	for semanticRows.Next() {
		var id, kind, status, title string
		var workID, actionID *string
		var evidence []byte
		var at time.Time
		if err := semanticRows.Scan(&id, &kind, &workID, &actionID, &status, &title, &evidence, &at); err != nil {
			semanticRows.Close()
			return err
		}
		b.addNode(protocol.GraphNode{ID: nodeID("incident", id), EntityID: id, Type: "incident", Label: title, Subtitle: kind, Status: status, OccurredAt: &at, Metadata: evidence})
		entityType, entityID := "work", ""
		if workID != nil {
			entityID = *workID
		}
		if actionID != nil {
			entityType, entityID = "action", *actionID
		}
		if entityID != "" {
			b.addEdge(protocol.GraphLink{ID: "semantic-finding:" + id, Source: nodeID(entityType, entityID), Target: nodeID("incident", id), Type: "HAS_FINDING", Label: "advisory finding"})
		}
		if status == "OPEN" {
			probability, triageScore := semanticScores(evidence)
			priority := 40 + int(math.Round(30*probability))
			if triageScore > 0 {
				priority = 40 + int(math.Round(40*triageScore))
			}
			b.attention = append(b.attention, protocol.AttentionItem{ID: "incident:" + id, EntityType: entityType, EntityID: entityID, Priority: priority, Category: kind, Title: title, Description: fmt.Sprintf("Advisory semantic inference (p=%.2f)", probability), Status: status, OccurredAt: &at})
		}
	}
	if err := semanticRows.Err(); err != nil {
		semanticRows.Close()
		return err
	}
	semanticRows.Close()

	if b.accepts("approval") {
		rows, err := s.pool.Query(ctx, `SELECT id::text,action_id,status,reason_code,requested_at FROM approvals
			WHERE tenant_id=$1 AND (requested_at BETWEEN $2 AND $3 OR status='PENDING') ORDER BY requested_at DESC LIMIT 500`, tenantID, f.From, f.To)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id, actionID, status, reason string
			var at time.Time
			if err := rows.Scan(&id, &actionID, &status, &reason, &at); err != nil {
				rows.Close()
				return err
			}
			b.addNode(protocol.GraphNode{ID: nodeID("approval", id), EntityID: id, Type: "approval", Label: "Approval", Subtitle: reason, Status: status, OccurredAt: &at})
			b.addEdge(protocol.GraphLink{ID: "action-approval:" + actionID + ":" + id, Source: nodeID("action", actionID), Target: nodeID("approval", id), Type: "REQUIRES", Label: "approval"})
			if status == "PENDING" {
				b.attention = append(b.attention, protocol.AttentionItem{ID: "approval:" + id, EntityType: "approval", EntityID: id, Priority: 80, Category: "APPROVAL", Title: "Approval is waiting", Description: reason, Status: status, OccurredAt: &at})
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
	}
	if b.accepts("lease") {
		rows, err := s.pool.Query(ctx, `SELECT id::text,action_id,resource_id,mode,fencing_token,expires_at,heartbeat_at FROM leases
			WHERE tenant_id=$1 AND expires_at>$2 ORDER BY expires_at LIMIT 500`, tenantID, f.From)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id, actionID, resourceID, mode string
			var fence int64
			var expires, heartbeat time.Time
			if err := rows.Scan(&id, &actionID, &resourceID, &mode, &fence, &expires, &heartbeat); err != nil {
				return err
			}
			b.addNode(protocol.GraphNode{ID: nodeID("lease", id), EntityID: id, Type: "lease", Label: mode + " lease", Subtitle: "Fence " + fmt.Sprint(fence), Status: leaseStatus(expires), OccurredAt: &heartbeat, Metadata: rawJSON(map[string]any{"expires_at": expires, "fencing_token": fence})})
			b.addEdge(protocol.GraphLink{ID: "action-lease:" + actionID + ":" + id, Source: nodeID("action", actionID), Target: nodeID("lease", id), Type: "HOLDS", Label: "holds"})
			b.addEdge(protocol.GraphLink{ID: "lease-resource:" + id + ":" + resourceID, Source: nodeID("lease", id), Target: nodeID("resource", resourceID), Type: "LOCKS", Label: "locks"})
			if time.Until(expires) < 10*time.Second {
				b.attention = append(b.attention, protocol.AttentionItem{ID: "lease:" + id, EntityType: "lease", EntityID: id, Priority: 50, Category: "LEASE_EXPIRING", Title: "Lease expires soon", Description: resourceID, Status: leaseStatus(expires), OccurredAt: &heartbeat})
			}
		}
		return rows.Err()
	}
	return nil
}

func semanticScores(raw []byte) (float64, float64) {
	var evidence struct {
		Probability float64 `json:"probability"`
		TriageScore float64 `json:"triage_score"`
	}
	_ = json.Unmarshal(raw, &evidence)
	return evidence.Probability, evidence.TriageScore
}

func (s *Service) loadDomainEdges(ctx context.Context, tenantID string, f Filter, b *builder) error {
	if b.lens == protocol.GraphLensWork {
		rows, err := s.pool.Query(ctx, `SELECT source_work_id,relationship,target_work_id FROM work_edges WHERE tenant_id=$1 LIMIT 1000`, tenantID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var source, relation, target string
			if err := rows.Scan(&source, &relation, &target); err != nil {
				rows.Close()
				return err
			}
			b.addEdge(protocol.GraphLink{ID: "work-edge:" + source + ":" + relation + ":" + target, Source: nodeID("work", source), Target: nodeID("work", target), Type: relation, Label: strings.ToLower(relation)})
		}
		rows.Close()
	}
	if b.lens == protocol.GraphLensResources {
		rows, err := s.pool.Query(ctx, `SELECT source_resource_id,relationship,target_resource_id FROM resource_edges WHERE tenant_id=$1 LIMIT 1000`, tenantID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var source, relation, target string
			if err := rows.Scan(&source, &relation, &target); err != nil {
				rows.Close()
				return err
			}
			b.addEdge(protocol.GraphLink{ID: "resource-edge:" + source + ":" + relation + ":" + target, Source: nodeID("resource", source), Target: nodeID("resource", target), Type: relation, Label: strings.ToLower(relation)})
		}
		rows.Close()
	}
	return nil
}

func (s *Service) loadCostDimensions(ctx context.Context, tenantID string, f Filter, b *builder) error {
	rows, err := s.pool.Query(ctx, `SELECT provider,COALESCE(model,''),COALESCE(waste_reason,''),sum(amount_micros_usd),count(*)
		FROM costs WHERE tenant_id=$1 AND observed_at BETWEEN $2 AND $3
		GROUP BY provider,COALESCE(model,''),COALESCE(waste_reason,'') ORDER BY sum(amount_micros_usd) DESC LIMIT 100`, tenantID, f.From, f.To)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var provider, model, waste string
		var amount, count int64
		if err := rows.Scan(&provider, &model, &waste, &amount, &count); err != nil {
			return err
		}
		providerID := nodeID("provider", provider)
		b.addNode(protocol.GraphNode{ID: providerID, EntityID: provider, Type: "provider", Label: provider, Subtitle: fmt.Sprintf("%d records", count), CostMicros: amount})
		if model != "" {
			modelID := nodeID("model", provider+":"+model)
			b.addNode(protocol.GraphNode{ID: modelID, EntityID: provider + ":" + model, Type: "model", Label: model, Subtitle: provider, CostMicros: amount})
			b.addEdge(protocol.GraphLink{ID: "provider-model:" + provider + ":" + model, Source: providerID, Target: modelID, Type: "BILLS", Label: "bills"})
		}
		if waste != "" {
			wasteID := nodeID("waste", waste)
			b.addNode(protocol.GraphNode{ID: wasteID, EntityID: waste, Type: "waste", Label: waste, Subtitle: "avoidable spend", Status: "WASTE", CostMicros: amount})
			b.addEdge(protocol.GraphLink{ID: "provider-waste:" + provider + ":" + waste, Source: providerID, Target: wasteID, Type: "ATTRIBUTED_WASTE", Label: "waste"})
		}
	}
	return rows.Err()
}

func (s *Service) Entity(ctx context.Context, tenantID, entityType, entityID string) (protocol.EntityInspector, error) {
	lens := lensFor(entityType)
	snapshot, err := s.Snapshot(ctx, tenantID, Filter{Lens: lens, From: time.Now().Add(-365 * 24 * time.Hour), FocusType: entityType, FocusID: entityID, Depth: 1, Limit: 500})
	if err != nil {
		return protocol.EntityInspector{}, err
	}
	wanted := nodeID(entityType, entityID)
	var entity protocol.GraphNode
	for _, node := range snapshot.Nodes {
		if node.ID == wanted {
			entity = node
			break
		}
	}
	if entity.ID == "" {
		return protocol.EntityInspector{}, pgx.ErrNoRows
	}
	details, err := s.entityDetails(ctx, tenantID, entityType, entityID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return protocol.EntityInspector{}, err
	}
	rows, err := s.pool.Query(ctx, `SELECT id,type,source,occurred_at,payload FROM events
		WHERE tenant_id=$1 AND entity_id=$2 ORDER BY occurred_at DESC LIMIT 100`, tenantID, entityID)
	if err != nil {
		return protocol.EntityInspector{}, err
	}
	defer rows.Close()
	var timeline []protocol.GraphTimelineEvent
	for rows.Next() {
		var event protocol.GraphTimelineEvent
		if err := rows.Scan(&event.ID, &event.Type, &event.Source, &event.OccurredAt, &event.Payload); err != nil {
			return protocol.EntityInspector{}, err
		}
		timeline = append(timeline, event)
	}
	var evaluations []protocol.DecisionEvaluation
	if entityType == "action" {
		var raw []byte
		_ = s.pool.QueryRow(ctx, `SELECT evaluations FROM decisions WHERE tenant_id=$1 AND action_id=$2 ORDER BY created_at DESC LIMIT 1`, tenantID, entityID).Scan(&raw)
		_ = json.Unmarshal(raw, &evaluations)
	}
	if timeline == nil {
		timeline = []protocol.GraphTimelineEvent{}
	}
	if evaluations == nil {
		evaluations = []protocol.DecisionEvaluation{}
	}
	actions := safeActions(entityType, entityID, entity.Status, details)
	if actions == nil {
		actions = []protocol.SafeAction{}
	}
	return protocol.EntityInspector{SchemaVersion: protocol.SchemaVersion, Entity: entity, RelatedNodes: without(snapshot.Nodes, wanted), RelatedEdges: snapshot.Edges, Timeline: timeline, Evaluations: evaluations, Details: details, SafeActions: actions, AsOf: time.Now().UTC()}, rows.Err()
}

func (s *Service) entityDetails(ctx context.Context, tenantID, typ, id string) ([]byte, error) {
	queries := map[string]string{
		"step":     `SELECT to_jsonb(st)-'input_ciphertext'-'output_ciphertext'-'admission_ciphertext' FROM steps st WHERE tenant_id=$1 AND id=$2`,
		"outcome":  `SELECT to_jsonb(o)-'claim_token'-'claim_until' FROM outcomes o WHERE tenant_id=$1 AND id=$2`,
		"effect":   `SELECT to_jsonb(e) FROM effect_occurrences e WHERE tenant_id=$1 AND id=$2`,
		"verifier": `SELECT to_jsonb(v)-'secret_ciphertext' FROM verifiers v WHERE tenant_id=$1 AND id=$2`,
		"incident": `SELECT to_jsonb(i) FROM incidents i WHERE tenant_id=$1 AND id=$2`,
		"agent":    `SELECT to_jsonb(t) FROM (SELECT id,actor_id,name,framework,model_provider,model,version,deployment_id,metadata,created_at,updated_at FROM agents WHERE tenant_id=$1 AND id=$2)t`,
		"session":  `SELECT to_jsonb(t) FROM (SELECT id,agent_id,actor_id,runtime,external_id,status,started_at,finished_at,metadata FROM sessions WHERE tenant_id=$1 AND id=$2)t`,
		"work":     `SELECT to_jsonb(t) FROM (SELECT id,actor_id,agent_id,session_id,parent_work_id,goal,status,priority,risk,started_at,completed_at,metadata FROM work WHERE tenant_id=$1 AND id=$2)t`,
		"action":   `SELECT to_jsonb(t) FROM (SELECT a.id,a.work_id,a.actor_id,a.session_id,a.operation,a.tool,a.source,a.status,a.risk,a.idempotency_key,a.effect_key,a.requested_at,a.started_at,a.finished_at,a.metadata,d.id AS receipt_id,d.decision,d.reason_code,d.enforced,d.evaluations,l.expires_at,l.fencing_token FROM actions a LEFT JOIN LATERAL(SELECT * FROM decisions WHERE tenant_id=a.tenant_id AND action_id=a.id ORDER BY created_at DESC LIMIT 1)d ON true LEFT JOIN LATERAL(SELECT min(expires_at)expires_at,max(fencing_token)fencing_token FROM leases WHERE tenant_id=a.tenant_id AND action_id=a.id)l ON true WHERE a.tenant_id=$1 AND a.id=$2)t`,
		"resource": `SELECT to_jsonb(t) FROM (SELECT id,type,external_id,namespace,current_version,version_source,version_confidence,version_observed_at,fencing_token,metadata,created_at,updated_at FROM resources WHERE tenant_id=$1 AND id=$2)t`,
		"approval": `SELECT to_jsonb(t) FROM (SELECT id,action_id,status,reason_code,requested_at,expires_at,resolved_at,resolved_by,metadata FROM approvals WHERE tenant_id=$1 AND id::text=$2)t`,
		"lease":    `SELECT to_jsonb(t) FROM (SELECT id,resource_id,action_id,mode,fencing_token,expires_at,heartbeat_at,created_at FROM leases WHERE tenant_id=$1 AND id::text=$2)t`,
		"decision": `SELECT to_jsonb(t) FROM (SELECT id,action_id,decision,reason_code,enforced,confidence,retry_after_ms,evidence,evaluations,kernel_version,resolver_version,policy_version,created_at FROM decisions WHERE tenant_id=$1 AND id::text=$2)t`,
	}
	query, ok := queries[typ]
	if !ok {
		return nil, pgx.ErrNoRows
	}
	var out []byte
	err := s.pool.QueryRow(ctx, query, tenantID, id).Scan(&out)
	return out, err
}

func safeActions(typ, id, status string, details []byte) []protocol.SafeAction {
	var out []protocol.SafeAction
	switch typ {
	case "approval":
		if status == "PENDING" {
			out = append(out,
				protocol.SafeAction{Type: "APPROVE", Label: "Approve", Method: "POST", Path: "/v1/approvals/" + id + "/approve", CSRF: true},
				protocol.SafeAction{Type: "DENY", Label: "Reject", Method: "POST", Path: "/v1/approvals/" + id + "/deny", CSRF: true})
		}
	case "action":
		out = append(out, protocol.SafeAction{Type: "REPLAY", Label: "Replay decision", Method: "GET", Path: "/v1/replay/" + id})
		var values map[string]any
		_ = json.Unmarshal(details, &values)
		if status == "RESERVED" && values["started_at"] == nil {
			out = append(out, protocol.SafeAction{Type: "CANCEL_RESERVATION", Label: "Cancel reservation", Method: "POST", Path: "/v1/actions/" + id + "/cancel-reservation", CSRF: true})
		}
	case "decision":
		var values map[string]any
		_ = json.Unmarshal(details, &values)
		if actionID, ok := values["action_id"].(string); ok {
			out = append(out, protocol.SafeAction{Type: "REPLAY", Label: "Replay decision", Method: "GET", Path: "/v1/replay/" + actionID})
		}
	}
	return out
}

func filtered(b *builder, f Filter) ([]protocol.GraphNode, []protocol.GraphLink) {
	allowed := map[string]bool{}
	for id, node := range b.nodes {
		if f.Status != "" && !strings.EqualFold(node.Status, f.Status) {
			continue
		}
		if f.Risk != "" && node.Risk != "" && node.Risk != f.Risk {
			continue
		}
		allowed[id] = true
	}
	if f.FocusID != "" {
		focus := nodeID(f.FocusType, f.FocusID)
		visited := map[string]bool{focus: true}
		frontier := []string{focus}
		for level := 0; level < f.Depth; level++ {
			var next []string
			for _, edge := range b.edges {
				for _, current := range frontier {
					if edge.Source == current && !visited[edge.Target] {
						visited[edge.Target] = true
						next = append(next, edge.Target)
					}
					if edge.Target == current && !visited[edge.Source] {
						visited[edge.Source] = true
						next = append(next, edge.Source)
					}
				}
			}
			frontier = next
		}
		for id := range allowed {
			allowed[id] = visited[id]
		}
	}
	var nodes []protocol.GraphNode
	for id, node := range b.nodes {
		if allowed[id] {
			nodes = append(nodes, node)
		}
	}
	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].Type == nodes[j].Type {
			return nodes[i].ID < nodes[j].ID
		}
		return nodes[i].Type < nodes[j].Type
	})
	var edges []protocol.GraphLink
	for _, edge := range b.edges {
		if allowed[edge.Source] && allowed[edge.Target] {
			edges = append(edges, edge)
		}
	}
	sort.Slice(edges, func(i, j int) bool { return edges[i].ID < edges[j].ID })
	return nodes, edges
}

func truncate(nodes []protocol.GraphNode, edges []protocol.GraphLink, attention []protocol.AttentionItem, limit int) ([]protocol.GraphNode, []protocol.GraphLink) {
	priorities := map[string]int{}
	for _, item := range attention {
		priorities[nodeID(item.EntityType, item.EntityID)] = item.Priority
	}
	sort.SliceStable(nodes, func(i, j int) bool { return priorities[nodes[i].ID] > priorities[nodes[j].ID] })
	nodes = nodes[:limit]
	kept := map[string]bool{}
	for _, node := range nodes {
		kept[node.ID] = true
	}
	filteredEdges := edges[:0]
	for _, edge := range edges {
		if kept[edge.Source] && kept[edge.Target] {
			filteredEdges = append(filteredEdges, edge)
		}
	}
	return nodes, filteredEdges
}

func lensFor(typ string) protocol.GraphLens {
	switch typ {
	case "agent", "session":
		return protocol.GraphLensAgents
	case "work":
		return protocol.GraphLensWork
	case "resource":
		return protocol.GraphLensResources
	case "decision", "evaluation", "approval", "lease", "action":
		return protocol.GraphLensTransaction
	default:
		return protocol.GraphLensLive
	}
}

func leaseStatus(expires time.Time) string {
	if expires.Before(time.Now()) {
		return "EXPIRED"
	}
	if time.Until(expires) < 10*time.Second {
		return "EXPIRING"
	}
	return "ACTIVE"
}

func without(nodes []protocol.GraphNode, id string) []protocol.GraphNode {
	out := make([]protocol.GraphNode, 0, len(nodes))
	for _, node := range nodes {
		if node.ID != id {
			out = append(out, node)
		}
	}
	return out
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func value(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

func rawJSON(value any) json.RawMessage {
	if value == nil {
		return nil
	}
	raw, _ := json.Marshal(value)
	return raw
}

func timeValue(value *time.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return *value
}
