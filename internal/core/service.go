package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"lumni/internal/resource"
	"lumni/pkg/protocol"
)

type Service struct {
	pool  *pgxpool.Pool
	query *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Service { return &Service{pool: pool, query: pool} }

func NewWithPools(ingestion, query *pgxpool.Pool) *Service {
	return &Service{pool: ingestion, query: query}
}

func raw(value json.RawMessage) []byte {
	if len(value) == 0 {
		return []byte("{}")
	}
	return value
}

func (s *Service) PutActor(ctx context.Context, tenantID string, in protocol.Actor) (protocol.Actor, error) {
	if in.ID == "" || in.Type == "" {
		return protocol.Actor{}, errors.New("id and type are required")
	}
	err := s.pool.QueryRow(ctx, `INSERT INTO actors (tenant_id,id,type,runtime,display_name,metadata)
		VALUES ($1,$2,$3,$4,$5,$6) ON CONFLICT (tenant_id,id) DO UPDATE SET
		type=EXCLUDED.type,runtime=EXCLUDED.runtime,display_name=EXCLUDED.display_name,metadata=EXCLUDED.metadata,updated_at=clock_timestamp()
		RETURNING id,type,runtime,display_name,metadata,created_at,updated_at,paused AND (until IS NULL OR until>now()),paused_at,paused_by,until`, tenantID, in.ID, in.Type, in.Runtime,
		in.DisplayName, raw(in.Metadata)).Scan(&in.ID, &in.Type, &in.Runtime, &in.DisplayName, &in.Metadata, &in.CreatedAt, &in.UpdatedAt, &in.Paused, &in.PausedAt, &in.PausedBy, &in.Until)
	in.SchemaVersion = protocol.SchemaVersion
	return in, err
}

func (s *Service) GetActor(ctx context.Context, tenantID, id string) (protocol.Actor, error) {
	var out protocol.Actor
	err := s.query.QueryRow(ctx, `SELECT id,type,runtime,display_name,metadata,created_at,updated_at,paused AND (until IS NULL OR until>now()),paused_at,paused_by,until FROM actors WHERE tenant_id=$1 AND id=$2`, tenantID, id).
		Scan(&out.ID, &out.Type, &out.Runtime, &out.DisplayName, &out.Metadata, &out.CreatedAt, &out.UpdatedAt, &out.Paused, &out.PausedAt, &out.PausedBy, &out.Until)
	out.SchemaVersion = protocol.SchemaVersion
	return out, err
}

func (s *Service) ListActors(ctx context.Context, tenantID string) ([]protocol.Actor, error) {
	rows, err := s.query.Query(ctx, `SELECT id,type,runtime,display_name,metadata,created_at,updated_at,paused AND (until IS NULL OR until>now()),paused_at,paused_by,until FROM actors WHERE tenant_id=$1 ORDER BY created_at DESC LIMIT 500`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []protocol.Actor
	for rows.Next() {
		var item protocol.Actor
		if err := rows.Scan(&item.ID, &item.Type, &item.Runtime, &item.DisplayName, &item.Metadata, &item.CreatedAt, &item.UpdatedAt, &item.Paused, &item.PausedAt, &item.PausedBy, &item.Until); err != nil {
			return nil, err
		}
		item.SchemaVersion = protocol.SchemaVersion
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Service) PutAgent(ctx context.Context, tenantID string, in protocol.Agent) (protocol.Agent, error) {
	if in.ID == "" || in.ActorID == "" || in.Name == "" {
		return protocol.Agent{}, errors.New("id, actor_id, and name are required")
	}
	err := s.pool.QueryRow(ctx, `INSERT INTO agents
		(tenant_id,id,actor_id,name,framework,model_provider,model,version,repository_id,deployment_id,metadata)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) ON CONFLICT (tenant_id,id) DO UPDATE SET
		name=EXCLUDED.name,framework=EXCLUDED.framework,model_provider=EXCLUDED.model_provider,model=EXCLUDED.model,
		version=EXCLUDED.version,repository_id=EXCLUDED.repository_id,deployment_id=EXCLUDED.deployment_id,metadata=EXCLUDED.metadata,updated_at=clock_timestamp()
		RETURNING id,actor_id,name,framework,model_provider,model,version,repository_id::text,deployment_id,metadata,created_at,updated_at`,
		tenantID, in.ID, in.ActorID, in.Name, in.Framework, in.ModelProvider, in.Model, in.Version, in.RepositoryID, in.DeploymentID, raw(in.Metadata)).
		Scan(&in.ID, &in.ActorID, &in.Name, &in.Framework, &in.ModelProvider, &in.Model, &in.Version, &in.RepositoryID, &in.DeploymentID, &in.Metadata, &in.CreatedAt, &in.UpdatedAt)
	in.SchemaVersion = protocol.SchemaVersion
	return in, err
}

func (s *Service) ListAgents(ctx context.Context, tenantID string) ([]protocol.Agent, error) {
	rows, err := s.query.Query(ctx, `SELECT id,actor_id,name,framework,model_provider,model,version,repository_id::text,deployment_id,metadata,created_at,updated_at FROM agents WHERE tenant_id=$1 ORDER BY created_at DESC LIMIT 500`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []protocol.Agent
	for rows.Next() {
		var item protocol.Agent
		if err := rows.Scan(&item.ID, &item.ActorID, &item.Name, &item.Framework, &item.ModelProvider, &item.Model, &item.Version, &item.RepositoryID, &item.DeploymentID, &item.Metadata, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		item.SchemaVersion = protocol.SchemaVersion
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Service) StartSession(ctx context.Context, tenantID string, in protocol.Session) (protocol.Session, error) {
	if in.ID == "" {
		in.ID = "sess_" + uuid.NewString()
	}
	if in.ActorID == "" {
		return protocol.Session{}, errors.New("actor_id is required")
	}
	if in.Status == "" {
		in.Status = "RUNNING"
	}
	err := s.pool.QueryRow(ctx, `INSERT INTO sessions (tenant_id,id,agent_id,actor_id,runtime,external_id,status,metadata)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT (tenant_id,id) DO UPDATE SET metadata=EXCLUDED.metadata
		RETURNING id,agent_id,actor_id,runtime,external_id,status,started_at,finished_at,metadata`, tenantID, in.ID, in.AgentID, in.ActorID, in.Runtime, in.ExternalID, in.Status, raw(in.Metadata)).
		Scan(&in.ID, &in.AgentID, &in.ActorID, &in.Runtime, &in.ExternalID, &in.Status, &in.StartedAt, &in.FinishedAt, &in.Metadata)
	in.SchemaVersion = protocol.SchemaVersion
	return in, err
}

func (s *Service) CompleteSession(ctx context.Context, tenantID, id string) error {
	tag, err := s.pool.Exec(ctx, `UPDATE sessions SET status='COMPLETED',finished_at=clock_timestamp() WHERE tenant_id=$1 AND id=$2 AND finished_at IS NULL`, tenantID, id)
	if err == nil && tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return err
}

func (s *Service) StartWork(ctx context.Context, tenantID string, in protocol.Work) (protocol.Work, error) {
	if in.ID == "" {
		in.ID = "work_" + uuid.NewString()
	}
	if in.ActorID == "" || strings.TrimSpace(in.Goal) == "" {
		return protocol.Work{}, errors.New("actor_id and goal are required")
	}
	if in.Status == "" {
		in.Status = protocol.WorkRunning
	}
	if in.Priority == 0 {
		in.Priority = 50
	}
	if in.Risk == "" {
		in.Risk = protocol.RiskMedium
	}
	err := s.pool.QueryRow(ctx, `INSERT INTO work
		(tenant_id,id,actor_id,agent_id,session_id,parent_work_id,goal,status,priority,risk,repository_id,issue_ref,pr_ref,base_commit,metadata)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15) ON CONFLICT (tenant_id,id) DO UPDATE SET metadata=EXCLUDED.metadata
		RETURNING id,actor_id,agent_id,session_id,parent_work_id,goal,status,priority,risk,repository_id::text,issue_ref,pr_ref,base_commit,started_at,completed_at,metadata`,
		tenantID, in.ID, in.ActorID, in.AgentID, in.SessionID, in.ParentWorkID, in.Goal, in.Status, in.Priority, in.Risk, in.RepositoryID, in.IssueRef, in.PRRef, in.BaseCommit, raw(in.Metadata)).
		Scan(&in.ID, &in.ActorID, &in.AgentID, &in.SessionID, &in.ParentWorkID, &in.Goal, &in.Status, &in.Priority, &in.Risk, &in.RepositoryID, &in.IssueRef, &in.PRRef, &in.BaseCommit, &in.StartedAt, &in.CompletedAt, &in.Metadata)
	in.SchemaVersion = protocol.SchemaVersion
	return in, err
}

func (s *Service) GetWork(ctx context.Context, tenantID, id string) (protocol.Work, error) {
	var in protocol.Work
	err := s.query.QueryRow(ctx, `SELECT id,actor_id,agent_id,session_id,parent_work_id,goal,status,priority,risk,repository_id::text,issue_ref,pr_ref,base_commit,started_at,completed_at,metadata FROM work WHERE tenant_id=$1 AND id=$2`, tenantID, id).
		Scan(&in.ID, &in.ActorID, &in.AgentID, &in.SessionID, &in.ParentWorkID, &in.Goal, &in.Status, &in.Priority, &in.Risk, &in.RepositoryID, &in.IssueRef, &in.PRRef, &in.BaseCommit, &in.StartedAt, &in.CompletedAt, &in.Metadata)
	in.SchemaVersion = protocol.SchemaVersion
	return in, err
}

func (s *Service) ListWork(ctx context.Context, tenantID string) ([]protocol.Work, error) {
	rows, err := s.query.Query(ctx, `SELECT id,actor_id,agent_id,session_id,parent_work_id,goal,status,priority,risk,repository_id::text,issue_ref,pr_ref,base_commit,started_at,completed_at,metadata FROM work WHERE tenant_id=$1 ORDER BY started_at DESC LIMIT 500`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []protocol.Work
	for rows.Next() {
		var in protocol.Work
		if err := rows.Scan(&in.ID, &in.ActorID, &in.AgentID, &in.SessionID, &in.ParentWorkID, &in.Goal, &in.Status, &in.Priority, &in.Risk, &in.RepositoryID, &in.IssueRef, &in.PRRef, &in.BaseCommit, &in.StartedAt, &in.CompletedAt, &in.Metadata); err != nil {
			return nil, err
		}
		in.SchemaVersion = protocol.SchemaVersion
		out = append(out, in)
	}
	return out, rows.Err()
}

func (s *Service) SetWorkStatus(ctx context.Context, tenantID, id string, status protocol.WorkStatus) error {
	var mode string
	if err := s.pool.QueryRow(ctx, `SELECT execution_mode FROM work WHERE tenant_id=$1 AND id=$2`, tenantID, id).Scan(&mode); err != nil {
		return err
	}
	if mode == "NATIVE" {
		return errors.New("native work transitions require the runtime API")
	}
	column := "completed_at"
	if status == protocol.WorkCancelled {
		column = "cancelled_at"
	}
	if status == protocol.WorkSuperseded {
		column = "superseded_at"
	}
	query := fmt.Sprintf("UPDATE work SET status=$3,%s=clock_timestamp() WHERE tenant_id=$1 AND id=$2", column)
	tag, err := s.pool.Exec(ctx, query, tenantID, id, status)
	if err == nil && tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return err
}

func (s *Service) PutResource(ctx context.Context, tenantID string, in protocol.Resource) (protocol.Resource, error) {
	in.ResourceRef = resource.Canonicalize(in.ResourceRef)
	err := s.pool.QueryRow(ctx, `INSERT INTO resources (tenant_id,id,type,external_id,namespace,metadata) VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (tenant_id,id) DO UPDATE SET metadata=EXCLUDED.metadata,updated_at=clock_timestamp()
		RETURNING id,type,external_id,namespace,current_version,version_source,version_confidence,fencing_token,metadata,created_at,updated_at`, tenantID, in.ID, in.Type, in.ExternalID, in.Namespace, raw(in.Metadata)).
		Scan(&in.ID, &in.Type, &in.ExternalID, &in.Namespace, &in.CurrentVersion, &in.VersionSource, &in.VersionConfidence, &in.FencingToken, &in.Metadata, &in.CreatedAt, &in.UpdatedAt)
	in.SchemaVersion = protocol.SchemaVersion
	return in, err
}

func (s *Service) GetResource(ctx context.Context, tenantID, id string) (protocol.Resource, error) {
	var in protocol.Resource
	err := s.query.QueryRow(ctx, `SELECT id,type,external_id,namespace,current_version,version_source,version_confidence,fencing_token,metadata,created_at,updated_at FROM resources WHERE tenant_id=$1 AND id=$2`, tenantID, id).Scan(&in.ID, &in.Type, &in.ExternalID, &in.Namespace, &in.CurrentVersion, &in.VersionSource, &in.VersionConfidence, &in.FencingToken, &in.Metadata, &in.CreatedAt, &in.UpdatedAt)
	in.SchemaVersion = protocol.SchemaVersion
	return in, err
}

func (s *Service) ListResources(ctx context.Context, tenantID string) ([]protocol.Resource, error) {
	rows, err := s.query.Query(ctx, `SELECT id,type,external_id,namespace,current_version,version_source,version_confidence,fencing_token,metadata,created_at,updated_at FROM resources WHERE tenant_id=$1 ORDER BY updated_at DESC LIMIT 500`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []protocol.Resource
	for rows.Next() {
		var in protocol.Resource
		if err := rows.Scan(&in.ID, &in.Type, &in.ExternalID, &in.Namespace, &in.CurrentVersion, &in.VersionSource, &in.VersionConfidence, &in.FencingToken, &in.Metadata, &in.CreatedAt, &in.UpdatedAt); err != nil {
			return nil, err
		}
		in.SchemaVersion = protocol.SchemaVersion
		out = append(out, in)
	}
	return out, rows.Err()
}

func (s *Service) UpdateVersion(ctx context.Context, tenantID, resourceID string, in protocol.ResourceVersionInput) error {
	if in.Version == "" || in.VersionSource == "" {
		return errors.New("version and version_source are required")
	}
	observed := time.Now().UTC()
	if in.ObservedAt != nil {
		observed = *in.ObservedAt
	}
	if in.Confidence == 0 {
		in.Confidence = 1
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var currentVersion, currentSource *string
	var currentSequence *int64
	var currentObserved *time.Time
	if err := tx.QueryRow(ctx, `SELECT current_version,version_source,version_sequence,version_observed_at
		FROM resources WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, tenantID, resourceID).Scan(&currentVersion, &currentSource, &currentSequence, &currentObserved); err != nil {
		return err
	}
	authoritative := currentVersion == nil || currentSource == nil
	if currentVersion != nil && currentSource != nil {
		authoritative = *currentSource == in.VersionSource
		if authoritative {
			switch {
			case in.Sequence != nil:
				authoritative = currentSequence == nil || *in.Sequence > *currentSequence
			case currentSequence != nil:
				authoritative = false
			default:
				authoritative = currentObserved == nil || !observed.Before(*currentObserved)
			}
		}
	}
	_, err = tx.Exec(ctx, `INSERT INTO resource_versions (tenant_id,resource_id,version,version_source,observed_at,source,source_sequence,confidence,authoritative,metadata,actor_id,action_id,summary,value_hash) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14) ON CONFLICT DO NOTHING`, tenantID, resourceID, in.Version, in.VersionSource, observed, in.Source, in.Sequence, in.Confidence, authoritative, raw(in.Metadata), in.ActorID, in.ActionID, in.Summary, in.ValueHash)
	if err != nil {
		return err
	}
	if authoritative {
		if _, err := tx.Exec(ctx, `UPDATE resources SET current_version=$3,version_source=$4,version_confidence=$5,
			version_observed_at=$6,version_sequence=$7,updated_at=clock_timestamp() WHERE tenant_id=$1 AND id=$2`, tenantID, resourceID, in.Version, in.VersionSource, in.Confidence, observed, in.Sequence); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func stableMutationID(tenant, value string) string {
	if id, e := uuid.Parse(value); e == nil {
		return id.String()
	}
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(tenant+":"+value)).String()
}
func (s *Service) Observe(ctx context.Context, tenantID string, in protocol.ObservationInput) (string, error) {
	if in.WorkID == "" || in.Version == "" || in.VersionSource == "" {
		return "", errors.New("work_id, version, and version_source are required")
	}
	ref := resource.Canonicalize(in.Resource)
	observed := time.Now().UTC()
	if in.ObservedAt != nil {
		observed = *in.ObservedAt
	}
	if in.Confidence == 0 {
		in.Confidence = 1
	}
	if in.EvidenceType == "" {
		in.EvidenceType = "EXPLICIT"
	}
	if in.ID == "" {
		in.ID = uuid.NewString()
	}
	in.ID = stableMutationID(tenantID, in.ID)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `INSERT INTO resources(tenant_id,id,type,external_id,namespace,metadata)VALUES($1,$2,$3,$4,$5,$6)ON CONFLICT(tenant_id,id)DO NOTHING`, tenantID, ref.ID, ref.Type, ref.ExternalID, ref.Namespace, raw(ref.Metadata))
	if err != nil {
		return "", err
	}
	tag, err := tx.Exec(ctx, `INSERT INTO observations(id,tenant_id,work_id,session_id,resource_id,observed_version,version_source,confidence,evidence_type,observed_at,metadata,step_id)VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) ON CONFLICT(id) DO NOTHING`, in.ID, tenantID, in.WorkID, in.SessionID, ref.ID, in.Version, in.VersionSource, in.Confidence, in.EvidenceType, observed, raw(in.Metadata), in.StepID)
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() == 0 {
		var same bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM observations WHERE id=$1 AND tenant_id=$2 AND (work_id,session_id,resource_id,observed_version,version_source,confidence,evidence_type,metadata,step_id) IS NOT DISTINCT FROM ($3,$4,$5,$6,$7,$8,$9,$10::jsonb,$11) AND ($12::timestamptz IS NULL OR observed_at=$12))`, in.ID, tenantID, in.WorkID, in.SessionID, ref.ID, in.Version, in.VersionSource, in.Confidence, in.EvidenceType, raw(in.Metadata), in.StepID, in.ObservedAt).Scan(&same)
		if err != nil {
			return "", err
		}
		if !same {
			return "", errors.New("OBSERVATION_IDENTITY_MISMATCH")
		}
	}
	return in.ID, tx.Commit(ctx)
}

func (s *Service) IngestEvent(ctx context.Context, tenantID string, in protocol.EventEnvelope) (bool, error) {
	if in.EventID == "" || in.Type == "" {
		return false, errors.New("event_id and type are required")
	}
	if in.OccurredAt.IsZero() {
		in.OccurredAt = time.Now().UTC()
	}
	tag, err := s.pool.Exec(ctx, `INSERT INTO events(tenant_id,id,type,source,occurred_at,payload)VALUES($1,$2,$3,$4,$5,$6)ON CONFLICT(tenant_id,id)DO NOTHING`, tenantID, in.EventID, in.Type, in.Source, in.OccurredAt, raw(in.Payload))
	return tag.RowsAffected() > 0, err
}

func (s *Service) RecordCost(ctx context.Context, tenantID string, in protocol.CostInput) (string, error) {
	if in.ID == "" {
		in.ID = uuid.NewString()
	}
	in.ID = stableMutationID(tenantID, in.ID)
	observed := time.Now().UTC()
	if in.ObservedAt != nil {
		observed = *in.ObservedAt
	}
	tag, err := s.pool.Exec(ctx, `INSERT INTO costs(id,tenant_id,actor_id,agent_id,session_id,work_id,action_id,provider,model,cost_type,input_units,output_units,compute_units,amount_micros_usd,observed_at,metadata,step_id)VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17) ON CONFLICT(id) DO NOTHING`, in.ID, tenantID, in.ActorID, in.AgentID, in.SessionID, in.WorkID, in.ActionID, in.Provider, in.Model, in.CostType, in.InputUnits, in.OutputUnits, in.ComputeUnits, in.AmountMicrosUSD, observed, raw(in.Metadata), in.StepID)
	if err == nil && tag.RowsAffected() == 0 {
		var same bool
		err = s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM costs WHERE id=$1 AND tenant_id=$2 AND (actor_id,agent_id,session_id,work_id,action_id,provider,model,cost_type,input_units,output_units,compute_units,amount_micros_usd,metadata,step_id) IS NOT DISTINCT FROM ($3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15::jsonb,$16) AND ($17::timestamptz IS NULL OR observed_at=$17))`, in.ID, tenantID, in.ActorID, in.AgentID, in.SessionID, in.WorkID, in.ActionID, in.Provider, in.Model, in.CostType, in.InputUnits, in.OutputUnits, in.ComputeUnits, in.AmountMicrosUSD, raw(in.Metadata), in.StepID, in.ObservedAt).Scan(&same)
		if err == nil && !same {
			err = errors.New("COST_IDENTITY_MISMATCH")
		}
	}
	return in.ID, err
}

func (s *Service) FinOpsSummary(ctx context.Context, tenantID string, since time.Time) (map[string]any, error) {
	var total, duplicate, stale, conflict, superseded int64
	err := s.query.QueryRow(ctx, `SELECT COALESCE(sum(amount_micros_usd),0),
		COALESCE(sum(amount_micros_usd)FILTER(WHERE waste_reason='DUPLICATE'),0),
		COALESCE(sum(amount_micros_usd)FILTER(WHERE waste_reason='STALE'),0),
		COALESCE(sum(amount_micros_usd)FILTER(WHERE waste_reason='CONFLICT'),0),
		COALESCE(sum(amount_micros_usd)FILTER(WHERE waste_reason='SUPERSEDED'),0)
		FROM costs WHERE tenant_id=$1 AND observed_at>=$2`, tenantID, since).Scan(&total, &duplicate, &stale, &conflict, &superseded)
	if err != nil {
		return nil, err
	}
	var useful, unverified, wasted int64
	err = s.query.QueryRow(ctx, `SELECT COALESCE(sum(c.amount_micros_usd)FILTER(WHERE c.waste_reason IS NULL AND o.status='VERIFIED'),0),COALESCE(sum(c.amount_micros_usd)FILTER(WHERE c.waste_reason IS NULL AND COALESCE(o.status,'UNKNOWN') IN('UNKNOWN','PENDING')),0),COALESCE(sum(c.amount_micros_usd)FILTER(WHERE c.waste_reason IS NOT NULL OR o.status IN('FAILED','MISMATCH','PARTIAL')),0) FROM costs c LEFT JOIN steps st ON st.tenant_id=c.tenant_id AND st.id=c.step_id LEFT JOIN outcomes o ON o.tenant_id=c.tenant_id AND o.action_id=COALESCE(c.action_id,st.action_id) WHERE c.tenant_id=$1 AND c.observed_at>=$2`, tenantID, since).Scan(&useful, &unverified, &wasted)
	return map[string]any{"period_start": since, "total_cost_micros_usd": total, "waste": map[string]int64{"duplicate": duplicate, "stale": stale, "conflict": conflict, "superseded": superseded}, "verified_outcome_metrics_available": true, "useful_cost_micros_usd": useful, "unverified_cost_micros_usd": unverified, "wasted_cost_micros_usd": wasted}, err
}

func (s *Service) GraphForWork(ctx context.Context, tenantID, workID string) (map[string]any, error) {
	work, err := s.GetWork(ctx, tenantID, workID)
	if err != nil {
		return nil, err
	}
	rows, err := s.query.Query(ctx, `SELECT a.id,a.operation,a.status,ar.resource_id,ar.mode FROM actions a LEFT JOIN action_resources ar ON ar.tenant_id=a.tenant_id AND ar.action_id=a.id WHERE a.tenant_id=$1 AND a.work_id=$2 ORDER BY a.requested_at`, tenantID, workID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var edges []map[string]any
	for rows.Next() {
		var actionID, operation, status string
		var resourceID, mode *string
		if err := rows.Scan(&actionID, &operation, &status, &resourceID, &mode); err != nil {
			return nil, err
		}
		edges = append(edges, map[string]any{"action_id": actionID, "operation": operation, "status": status, "resource_id": resourceID, "mode": mode})
	}
	return map[string]any{"work": work, "activity": edges}, rows.Err()
}

func (s *Service) Action(ctx context.Context, tenantID, id string) (map[string]any, error) {
	var action map[string]any
	var rawAction []byte
	err := s.query.QueryRow(ctx, `SELECT jsonb_build_object('id',id,'work_id',work_id,'actor_id',actor_id,'session_id',session_id,'operation',operation,'tool',tool,'source',source,'status',status,'risk',risk,'idempotency_key',idempotency_key,'effect_key',effect_key,'requested_at',requested_at,'started_at',started_at,'finished_at',finished_at,'metadata',metadata)FROM actions WHERE tenant_id=$1 AND id=$2`, tenantID, id).Scan(&rawAction)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(rawAction, &action); err != nil {
		return nil, err
	}
	var decisionRaw []byte
	_ = s.query.QueryRow(ctx, `SELECT jsonb_build_object('receipt_id',id,'decision',decision,'reason_code',reason_code,'enforced',enforced,'evidence',evidence,'evaluations',evaluations,'kernel_version',kernel_version,'created_at',created_at)FROM decisions WHERE tenant_id=$1 AND action_id=$2 ORDER BY created_at DESC LIMIT 1`, tenantID, id).Scan(&decisionRaw)
	if len(decisionRaw) > 0 {
		var d any
		_ = json.Unmarshal(decisionRaw, &d)
		action["latest_decision"] = d
	}
	return action, nil
}

func IsNotFound(err error) bool { return errors.Is(err, pgx.ErrNoRows) }
