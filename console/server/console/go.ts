// What the Go API sends back, as far as the console reads it. Durable lists
// are raw table rows (snake_case, Postgres timestamps); core lists follow
// pkg/protocol. Fields the console doesn’t use are left out.

export interface GoApproval {
  id: string;
  action_id: string;
  status: 'PENDING' | 'APPROVED' | 'DENIED' | 'EXPIRED';
  reason_code: string;
  requested_at: string;
  expires_at: string | null;
  resolved_at: string | null;
  resolved_by: string | null;
  metadata: Record<string, unknown> | null;
}

/** An incident row: `finding_<receipt>` for every step-in, `outcome_<id>` for one that didn’t happen. */
export interface GoIncident {
  id: string;
  kind: string;
  work_id: string | null;
  action_id: string | null;
  status: 'OPEN' | 'RESOLVED' | string;
  title: string;
  evidence: Record<string, unknown> | null;
  created_at: string;
  updated_at: string;
}

export interface GoOutcome {
  id: string;
  action_id: string;
  work_id: string;
  verifier_id: string | null;
  expected: unknown;
  status: string;
  observed: unknown;
  attempts: number;
  reason: string;
  created_at: string;
  updated_at: string;
}

/** A row of the durable /v1/actions list; arguments are never in it. */
export interface GoActionRow {
  id: string;
  work_id: string;
  actor_id: string;
  operation: string;
  tool: string;
  source: string;
  status: string;
  risk: string;
  requested_at: string;
  started_at: string | null;
  finished_at: string | null;
  metadata: Record<string, unknown> | null;
}

export interface GoDecision {
  receipt_id: string;
  decision: string;
  reason_code: string;
  enforced: boolean;
  evidence: { type: string; data?: Record<string, unknown> }[] | null;
  evaluations: GoEvaluation[] | null;
  created_at: string;
}

/** GET /v1/actions/{id}. */
export interface GoAction extends GoActionRow {
  latest_decision?: GoDecision;
}

export interface GoEvaluation {
  stage: string;
  outcome: string;
  reason_code?: string;
  subject_id?: string;
  details?: Record<string, unknown>;
}

export interface GoActor {
  id: string;
  type: string;
  display_name?: string;
  metadata?: Record<string, unknown> | null;
}

export interface GoAgent {
  id: string;
  actor_id: string;
  name: string;
  framework?: string;
  model_provider?: string;
  model?: string;
}

export interface GoVerifier {
  id: string;
  name: string;
  kind: string;
  version: string;
  config: Record<string, unknown> | null;
  enabled: boolean;
  created_at: string;
}

export interface GoPolicyRule {
  id: string;
  name: string;
  operation: string;
  resource_type: string;
  minimum_risk: string | null;
  argument_pointer: string | null;
  numeric_threshold: number | null;
  outcome: 'REQUIRE_APPROVAL' | 'REJECT' | 'WARN';
  reason_code: string;
  enabled: boolean;
  version: number;
  created_at: string;
}

export interface GoEnforcement {
  operation: string;
  rule: string;
  mode: 'SHADOW' | 'WARN' | 'ENFORCE';
}

export interface GoConfig {
  enforcement_mode: 'SHADOW' | 'WARN' | 'ENFORCE';
  semantic_enabled?: boolean;
  /** Every AI paused (POST /v1/config/pause). */
  paused?: boolean;
}

export interface GoNode {
  id: string;
  entity_id: string;
  type: string;
  label: string;
  subtitle?: string;
  status?: string;
  risk?: string;
  agent_state?: string;
  cost_micros_usd?: number;
  occurred_at?: string;
  metadata?: unknown;
}

export interface GoEdge {
  id: string;
  source: string;
  target: string;
  type: string;
  label?: string;
  metadata?: unknown;
}

export interface GoTrend {
  bucket: string;
  actions: number;
  decisions: number;
  failures: number;
  cost_micros_usd: number;
  waste_micros_usd: number;
}

export interface GoSnapshot {
  lens: string;
  nodes: GoNode[] | null;
  edges: GoEdge[] | null;
  trends?: GoTrend[] | null;
  truncated: boolean;
  cursor: number;
}

export interface GoFinOps {
  series: GoTrend[] | null;
  by_waste: { key: string; label: string; amount_micros_usd: number; count: number }[] | null;
  total_cost_micros_usd: number;
  waste_micros_usd: number;
}

export interface GoLiveEdge {
  action_id: string;
  operation: string;
  status: string;
  resource_id: string;
  mode: string;
}

export interface GoEntity {
  entity: GoNode;
  related_nodes: GoNode[] | null;
  related_edges: GoEdge[] | null;
  timeline: { id: string; type: string; source: string; occurred_at: string; payload?: unknown }[] | null;
  evaluations?: GoEvaluation[] | null;
}

export interface GoAttempt {
  outcome_id: string;
  id: string;
  evidence: Record<string, unknown> | null;
  created_at: string;
}

export interface GoSession {
  tenant_id: string;
  scopes: string[];
  csrf_token?: string;
  expires_at?: string;
}
