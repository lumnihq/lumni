package semantic

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	duplicateQuestionSet = "duplicate-work-v1"
	riskQuestionSet      = "risk-review-v1"
	traceQuestionSet     = "trace-triage-v1"
	maxJobsPerTick       = 5
	zdrRefreshInterval   = 5 * time.Minute
)

type Service struct {
	pool          *pgxpool.Pool
	evaluator     SemanticEvaluator
	maxCandidates int
	logger        *slog.Logger
	zdrCheckedAt  time.Time
	zdrEligible   bool
}

func NewService(pool *pgxpool.Pool, evaluator SemanticEvaluator, maxCandidates int, logger *slog.Logger) *Service {
	if maxCandidates < 1 || maxCandidates > 20 {
		maxCandidates = 20
	}
	return &Service{pool: pool, evaluator: evaluator, maxCandidates: maxCandidates, logger: logger}
}

// Run is intentionally separate from worker.Once: provider latency and outages
// cannot delay leases, admission maintenance, webhooks, or durable execution.
func (s *Service) Run(ctx context.Context, interval time.Duration) {
	s.tick(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.tick(ctx)
		}
	}
}

func (s *Service) tick(ctx context.Context) {
	eligible, err := s.providerEligible(ctx)
	if err != nil {
		s.logger.Warn("semantic worker disabled until ZDR can be verified", "provider", s.evaluator.Provider(), "error", err)
		return
	}
	if !eligible {
		s.logger.Warn("semantic worker disabled because model is absent from OpenRouter ZDR catalog", "model", s.evaluator.RequestedModel())
		return
	}
	if err := s.enqueue(ctx); err != nil {
		s.logger.Warn("semantic job discovery failed", "error", err)
		return
	}
	for range maxJobsPerTick {
		job, ok, err := s.claim(ctx)
		if err != nil {
			s.logger.Warn("semantic job claim failed", "error", err)
			return
		}
		if !ok {
			return
		}
		if err := s.process(ctx, job); err != nil {
			s.logger.Warn("semantic job failed", "job_id", job.ID, "kind", job.Kind, "error", err)
		}
	}
}

func (s *Service) providerEligible(ctx context.Context) (bool, error) {
	if !s.zdrCheckedAt.IsZero() && time.Since(s.zdrCheckedAt) < zdrRefreshInterval {
		return s.zdrEligible, nil
	}
	eligible, err := s.evaluator.PrivacyEligible(ctx)
	s.zdrCheckedAt = time.Now()
	if err != nil {
		// Retry transient catalog failures on the next worker tick.
		s.zdrCheckedAt = time.Time{}
		s.zdrEligible = false
		return false, err
	}
	s.zdrEligible = eligible
	return eligible, nil
}

func (s *Service) enqueue(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO semantic_jobs(tenant_id,kind,subject_type,subject_id,question_set_version)
		SELECT w.tenant_id,'DUPLICATE_WORK','work',w.id,$1
		FROM work w JOIN tenants t ON t.id=w.tenant_id
		WHERE t.settings @> '{"semantic_enabled":true}'::jsonb
		  AND w.started_at>=clock_timestamp()-interval '30 days'
		ON CONFLICT DO NOTHING`, duplicateQuestionSet)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO semantic_jobs(tenant_id,kind,subject_type,subject_id,question_set_version)
		SELECT a.tenant_id,'RISK_REVIEW','action',a.id,$1
		FROM actions a JOIN tenants t ON t.id=a.tenant_id
		WHERE t.settings @> '{"semantic_enabled":true}'::jsonb
		  AND a.requested_at>=clock_timestamp()-interval '30 days'
		ON CONFLICT DO NOTHING`, riskQuestionSet)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO semantic_jobs(tenant_id,kind,subject_type,subject_id,question_set_version)
		SELECT r.tenant_id,'TRACE_TRIAGE','action',r.action_id,$1
		FROM action_results r JOIN tenants t ON t.id=r.tenant_id
		WHERE t.settings @> '{"semantic_enabled":true}'::jsonb
		  AND r.recorded_at>=clock_timestamp()-interval '30 days'
		ON CONFLICT DO NOTHING`, traceQuestionSet)
	return err
}

type semanticJob struct {
	ID, TenantID, Kind, SubjectType, SubjectID, QuestionSet string
}

func (s *Service) claim(ctx context.Context) (semanticJob, bool, error) {
	var job semanticJob
	err := s.pool.QueryRow(ctx, `
		WITH candidate AS (
			SELECT j.id FROM semantic_jobs j
			JOIN tenants t ON t.id=j.tenant_id
			WHERE t.settings @> '{"semantic_enabled":true}'::jsonb
			  AND ((j.status='PENDING' AND j.next_attempt_at<=clock_timestamp())
			       OR (j.status='RUNNING' AND j.claim_until<=clock_timestamp()))
			ORDER BY j.created_at,j.id
			FOR UPDATE OF j SKIP LOCKED LIMIT 1
		)
		UPDATE semantic_jobs j SET status='RUNNING',attempts=j.attempts+1,
			claim_until=clock_timestamp()+interval '5 minutes',updated_at=clock_timestamp()
		FROM candidate c WHERE j.id=c.id
		RETURNING j.id::text,j.tenant_id::text,j.kind,j.subject_type,j.subject_id,j.question_set_version`).
		Scan(&job.ID, &job.TenantID, &job.Kind, &job.SubjectType, &job.SubjectID, &job.QuestionSet)
	if errors.Is(err, pgx.ErrNoRows) {
		return semanticJob{}, false, nil
	}
	return job, err == nil, err
}

type preparedEvaluation struct {
	Request      Request
	WorkID       string
	ActionID     string
	DeclaredRisk string
	Candidates   []candidate
}

func (s *Service) process(ctx context.Context, job semanticJob) error {
	var enabled bool
	if err := s.pool.QueryRow(ctx, `SELECT settings @> '{"semantic_enabled":true}'::jsonb FROM tenants WHERE id=$1`, job.TenantID).Scan(&enabled); err != nil {
		return err
	}
	if !enabled {
		return nil
	}
	prepared, err := s.prepare(ctx, job)
	if err != nil {
		s.persistFailure(ctx, job, prepared, "", 0, err)
		return err
	}
	if err := validateOutboundState(job.Kind, prepared.Request.State); err != nil {
		s.persistFailure(ctx, job, prepared, "", 0, err)
		return err
	}
	if len(prepared.Request.Questions) == 0 {
		_, err := s.pool.Exec(ctx, `UPDATE semantic_jobs SET status='COMPLETED',last_error='',claim_until=NULL,completed_at=clock_timestamp(),updated_at=clock_timestamp() WHERE id=$1`, job.ID)
		return err
	}
	payload, err := json.Marshal(prepared.Request)
	if err != nil {
		s.persistFailure(ctx, job, prepared, "", 0, err)
		return err
	}
	hash := sha256.Sum256(payload)
	inputHash := hex.EncodeToString(hash[:])
	started := time.Now()
	response, err := s.evaluator.Evaluate(ctx, prepared.Request)
	latency := time.Since(started)
	if err != nil {
		s.persistFailure(ctx, job, prepared, inputHash, latency, err)
		return err
	}
	if err := validateResponse(prepared.Request, response); err != nil {
		s.persistFailure(ctx, job, prepared, inputHash, latency, err)
		return err
	}
	return s.persistSuccess(ctx, job, prepared, inputHash, latency, response)
}

func (s *Service) prepare(ctx context.Context, job semanticJob) (preparedEvaluation, error) {
	switch job.Kind {
	case "DUPLICATE_WORK":
		return s.prepareDuplicate(ctx, job)
	case "RISK_REVIEW":
		return s.prepareRisk(ctx, job)
	case "TRACE_TRIAGE":
		return s.prepareTrace(ctx, job)
	default:
		return preparedEvaluation{}, fmt.Errorf("unknown semantic job kind %q", job.Kind)
	}
}

type workState struct {
	Goal         string `json:"goal"`
	Status       string `json:"status"`
	DeclaredRisk string `json:"declared_risk"`
}

type candidate struct {
	ID             string `json:"-"`
	Key            string `json:"candidate"`
	Goal           string `json:"goal"`
	Status         string `json:"status"`
	DeclaredRisk   string `json:"declared_risk"`
	SameRepository bool   `json:"same_repository"`
	SameIssue      bool   `json:"same_issue"`
	SharesResource bool   `json:"shares_resource"`
}

func (s *Service) prepareDuplicate(ctx context.Context, job semanticJob) (preparedEvaluation, error) {
	var current workState
	var repositoryID, issueRef *string
	err := s.pool.QueryRow(ctx, `SELECT goal,status,risk,repository_id::text,issue_ref FROM work WHERE tenant_id=$1 AND id=$2`, job.TenantID, job.SubjectID).
		Scan(&current.Goal, &current.Status, &current.DeclaredRisk, &repositoryID, &issueRef)
	if err != nil {
		return preparedEvaluation{}, err
	}
	current.Goal = redactSummary(current.Goal, 512)
	current.Status = safeLabel(current.Status)
	current.DeclaredRisk = safeLabel(current.DeclaredRisk)
	rows, err := s.pool.Query(ctx, `
		SELECT c.id,c.goal,c.status,c.risk,
		       ($3::text IS NOT NULL AND c.repository_id::text=$3) AS same_repository,
		       ($4::text IS NOT NULL AND c.issue_ref=$4) AS same_issue,
		       EXISTS(
			   SELECT 1 FROM actions ca JOIN action_resources cr
			     ON cr.tenant_id=ca.tenant_id AND cr.action_id=ca.id
			   JOIN actions oa ON oa.tenant_id=ca.tenant_id AND oa.work_id=$2
			   JOIN action_resources orr ON orr.tenant_id=oa.tenant_id AND orr.action_id=oa.id
			     AND orr.resource_id=cr.resource_id
			   WHERE ca.tenant_id=c.tenant_id AND ca.work_id=c.id
		   ) AS shares_resource
		FROM work c
		WHERE c.tenant_id=$1 AND c.id<>$2
		  AND c.status IN ('PENDING','RUNNING','WAITING','BLOCKED')
		ORDER BY same_issue DESC,shares_resource DESC,same_repository DESC,c.started_at DESC,c.id
		LIMIT $5`, job.TenantID, job.SubjectID, repositoryID, issueRef, s.maxCandidates)
	if err != nil {
		return preparedEvaluation{}, err
	}
	defer rows.Close()
	candidates := make([]candidate, 0, s.maxCandidates)
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.ID, &c.Goal, &c.Status, &c.DeclaredRisk, &c.SameRepository, &c.SameIssue, &c.SharesResource); err != nil {
			return preparedEvaluation{}, err
		}
		c.Key = fmt.Sprintf("candidate_%02d", len(candidates)+1)
		c.Goal = redactSummary(c.Goal, 512)
		c.Status = safeLabel(c.Status)
		c.DeclaredRisk = safeLabel(c.DeclaredRisk)
		candidates = append(candidates, c)
	}
	if err := rows.Err(); err != nil {
		return preparedEvaluation{}, err
	}
	questions := make(map[string]Question, len(candidates)*2)
	for _, c := range candidates {
		questions["same_goal_"+c.Key] = Question{Type: QuestionNoul, Instructions: "Do `work` and `" + c.Key + "` pursue materially the same goal?"}
		questions["supersedes_"+c.Key] = Question{Type: QuestionNoul, Instructions: "Does `work` make `" + c.Key + "` unnecessary or obsolete?"}
	}
	return preparedEvaluation{
		Request: Request{State: map[string]any{"work": current, "candidates": candidates}, Questions: questions},
		WorkID:  job.SubjectID, Candidates: candidates,
	}, nil
}

type resourceState struct {
	Type string `json:"type"`
	Mode string `json:"mode"`
}

type actionState struct {
	Operation    string          `json:"operation"`
	Tool         string          `json:"tool,omitempty"`
	Status       string          `json:"status"`
	DeclaredRisk string          `json:"declared_risk"`
	Resources    []resourceState `json:"resources"`
	Decision     string          `json:"decision,omitempty"`
	ReasonCode   string          `json:"reason_code,omitempty"`
}

func (s *Service) prepareRisk(ctx context.Context, job semanticJob) (preparedEvaluation, error) {
	var work workState
	var action actionState
	var workID string
	err := s.pool.QueryRow(ctx, `
		SELECT a.work_id,w.goal,w.status,w.risk,a.operation,a.tool,a.status,a.risk,
		       COALESCE(d.decision,''),COALESCE(d.reason_code,'')
		FROM actions a JOIN work w ON w.tenant_id=a.tenant_id AND w.id=a.work_id
		LEFT JOIN LATERAL (
			SELECT decision,reason_code FROM decisions
			WHERE tenant_id=a.tenant_id AND action_id=a.id ORDER BY created_at DESC LIMIT 1
		) d ON true
		WHERE a.tenant_id=$1 AND a.id=$2`, job.TenantID, job.SubjectID).
		Scan(&workID, &work.Goal, &work.Status, &work.DeclaredRisk, &action.Operation, &action.Tool, &action.Status, &action.DeclaredRisk, &action.Decision, &action.ReasonCode)
	if err != nil {
		return preparedEvaluation{}, err
	}
	work.Goal = redactSummary(work.Goal, 512)
	work.Status = safeLabel(work.Status)
	work.DeclaredRisk = safeLabel(work.DeclaredRisk)
	action.Operation = safeLabel(action.Operation)
	action.Tool = safeLabel(action.Tool)
	action.Status = safeLabel(action.Status)
	action.DeclaredRisk = safeLabel(action.DeclaredRisk)
	action.Decision = safeLabel(action.Decision)
	action.ReasonCode = safeReasonCode(action.ReasonCode)
	action.Resources, err = s.resources(ctx, job.TenantID, job.SubjectID)
	if err != nil {
		return preparedEvaluation{}, err
	}
	questions := map[string]Question{
		"apparent_risk": {
			Type: QuestionChoice, Instructions: "What is the consequence level of `action`?",
			Criteria: map[string]string{
				"LOW":      "Read-only or trivially reversible",
				"MEDIUM":   "Limited and reversible external change",
				"HIGH":     "Consequential financial or customer-facing change",
				"CRITICAL": "Potentially catastrophic or broadly irreversible",
			},
		},
		"goal_action_alignment": {Type: QuestionNoul, Instructions: "Does `action` directly advance `work.goal`?"},
	}
	return preparedEvaluation{
		Request: Request{State: map[string]any{"work": work, "action": action}, Questions: questions},
		WorkID:  workID, ActionID: job.SubjectID, DeclaredRisk: action.DeclaredRisk,
	}, nil
}

type traceStep struct {
	Type       string `json:"type"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Attempt    int    `json:"attempt"`
	HasError   bool   `json:"has_error"`
	ErrorClass string `json:"error_class,omitempty"`
}

func (s *Service) prepareTrace(ctx context.Context, job semanticJob) (preparedEvaluation, error) {
	var work workState
	var action actionState
	var result struct {
		Status    string `json:"status"`
		Retryable *bool  `json:"retryable,omitempty"`
	}
	var outcome struct {
		Status     string `json:"status,omitempty"`
		ReasonCode string `json:"reason_code,omitempty"`
	}
	var workID string
	err := s.pool.QueryRow(ctx, `
		SELECT a.work_id,w.goal,w.status,w.risk,a.operation,a.tool,a.status,a.risk,
		       r.status,r.retryable,COALESCE(o.status,''),COALESCE(o.reason,'')
		FROM actions a JOIN work w ON w.tenant_id=a.tenant_id AND w.id=a.work_id
		JOIN action_results r ON r.tenant_id=a.tenant_id AND r.action_id=a.id
		LEFT JOIN outcomes o ON o.tenant_id=a.tenant_id AND o.action_id=a.id
		WHERE a.tenant_id=$1 AND a.id=$2`, job.TenantID, job.SubjectID).
		Scan(&workID, &work.Goal, &work.Status, &work.DeclaredRisk, &action.Operation, &action.Tool, &action.Status, &action.DeclaredRisk,
			&result.Status, &result.Retryable, &outcome.Status, &outcome.ReasonCode)
	if err != nil {
		return preparedEvaluation{}, err
	}
	work.Goal = redactSummary(work.Goal, 512)
	work.Status = safeLabel(work.Status)
	work.DeclaredRisk = safeLabel(work.DeclaredRisk)
	action.Operation = safeLabel(action.Operation)
	action.Tool = safeLabel(action.Tool)
	action.Status = safeLabel(action.Status)
	action.DeclaredRisk = safeLabel(action.DeclaredRisk)
	result.Status = safeLabel(result.Status)
	outcome.Status = safeLabel(outcome.Status)
	outcome.ReasonCode = safeReasonCode(outcome.ReasonCode)
	action.Resources, err = s.resources(ctx, job.TenantID, job.SubjectID)
	if err != nil {
		return preparedEvaluation{}, err
	}
	rows, err := s.pool.Query(ctx, `SELECT type,name,status,attempt,error FROM steps WHERE tenant_id=$1 AND work_id=$2 ORDER BY created_at DESC,id LIMIT 50`, job.TenantID, workID)
	if err != nil {
		return preparedEvaluation{}, err
	}
	defer rows.Close()
	steps := make([]traceStep, 0, 50)
	for rows.Next() {
		var step traceStep
		var rawError string
		if err := rows.Scan(&step.Type, &step.Name, &step.Status, &step.Attempt, &rawError); err != nil {
			return preparedEvaluation{}, err
		}
		step.Type, step.Name, step.Status = safeLabel(step.Type), safeLabel(step.Name), safeLabel(step.Status)
		step.HasError = rawError != ""
		step.ErrorClass = errorClass(rawError)
		steps = append(steps, step)
	}
	if err := rows.Err(); err != nil {
		return preparedEvaluation{}, err
	}
	// The query is newest-first for a bounded index scan; reverse it before sending.
	for left, right := 0, len(steps)-1; left < right; left, right = left+1, right-1 {
		steps[left], steps[right] = steps[right], steps[left]
	}
	state := map[string]any{"work": work, "action": action, "result": result, "steps": steps}
	if outcome.Status != "" || outcome.ReasonCode != "" {
		state["outcome"] = outcome
	}
	questions := map[string]Question{
		"goal_action_alignment":       {Type: QuestionNoul, Instructions: "Does the summarized action sequence directly advance the work goal?"},
		"repetition_without_progress": {Type: QuestionNoul, Instructions: "Does the summarized sequence show repetition without meaningful progress?"},
		"outcome_supported":           {Type: QuestionNoul, Instructions: "Is the claimed result or outcome supported by the summarized sequence?"},
	}
	return preparedEvaluation{Request: Request{State: state, Questions: questions}, WorkID: workID, ActionID: job.SubjectID, DeclaredRisk: action.DeclaredRisk}, nil
}

func (s *Service) resources(ctx context.Context, tenantID, actionID string) ([]resourceState, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT r.type,ar.mode FROM action_resources ar
		JOIN resources r ON r.tenant_id=ar.tenant_id AND r.id=ar.resource_id
		WHERE ar.tenant_id=$1 AND ar.action_id=$2 ORDER BY r.type,ar.mode LIMIT 100`, tenantID, actionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	resources := make([]resourceState, 0)
	for rows.Next() {
		var resource resourceState
		if err := rows.Scan(&resource.Type, &resource.Mode); err != nil {
			return nil, err
		}
		resource.Type, resource.Mode = safeLabel(resource.Type), safeLabel(resource.Mode)
		resources = append(resources, resource)
	}
	return resources, rows.Err()
}

func validateResponse(request Request, response Response) error {
	if response.Model == "" || len(response.Model) > 200 || safeLabel(response.Model) != response.Model {
		return errors.New("semantic response has invalid model identifier")
	}
	if len(response.Answers) != len(request.Questions) {
		return fmt.Errorf("semantic response returned %d answers for %d questions", len(response.Answers), len(request.Questions))
	}
	if response.Usage.InputTokens < 0 || response.Usage.OutputTokens < 0 || response.Usage.PromptTokens < 0 || response.Usage.CompletionTokens < 0 || response.Usage.TotalTokens < 0 || response.Usage.CostUSD < 0 || response.CostMicros < 0 {
		return errors.New("semantic response has invalid usage")
	}
	for name, question := range request.Questions {
		answer, ok := response.Answers[name]
		if !ok {
			return fmt.Errorf("semantic response missing answer %q", name)
		}
		switch question.Type {
		case QuestionNoul:
			if answer.Noul == nil || answer.Choice != "" || answer.Score != nil || len(answer.Probabilities) != 0 || *answer.Noul < 0 || *answer.Noul > 1 || math.IsNaN(*answer.Noul) {
				return fmt.Errorf("semantic response has invalid Noul answer %q", name)
			}
		case QuestionChoice:
			criteria, err := choiceCriteria(question.Criteria)
			if err != nil {
				return fmt.Errorf("semantic Choice question %q: %w", name, err)
			}
			if answer.Noul != nil || answer.Score != nil || len(answer.Probabilities) != len(criteria) {
				return fmt.Errorf("semantic response has invalid Choice answer %q", name)
			}
			if _, known := criteria[answer.Choice]; !known {
				return fmt.Errorf("semantic response has unknown choice %q for %q", answer.Choice, name)
			}
			var total float64
			for choice, probability := range answer.Probabilities {
				if _, known := criteria[choice]; !known {
					return fmt.Errorf("semantic response has unknown probability choice %q for %q", choice, name)
				}
				if probability < 0 || probability > 1 || math.IsNaN(probability) {
					return fmt.Errorf("semantic response has invalid probability for %q", name)
				}
				total += probability
			}
			if math.Abs(total-1) > 0.02 {
				return fmt.Errorf("semantic response probabilities for %q sum to %.4f", name, total)
			}
		case QuestionScore:
			levels, err := scoreCriteriaCount(question.Criteria)
			if err != nil {
				return fmt.Errorf("semantic Score question %q: %w", name, err)
			}
			if answer.Noul != nil || answer.Choice != "" || answer.Score == nil || math.IsNaN(*answer.Score) || *answer.Score < 0 || *answer.Score > float64(levels-1) || len(answer.Probabilities) != levels {
				return fmt.Errorf("semantic response has invalid Score answer %q", name)
			}
			var total float64
			for level := range levels {
				probability, present := answer.Probabilities[fmt.Sprint(level)]
				if !present || probability < 0 || probability > 1 || math.IsNaN(probability) {
					return fmt.Errorf("semantic response has invalid Score probability for %q", name)
				}
				total += probability
			}
			if math.Abs(total-1) > 0.02 {
				return fmt.Errorf("semantic response Score probabilities for %q sum to %.4f", name, total)
			}
		}
		if answer.Confidence != nil && (*answer.Confidence < 0 || *answer.Confidence > 1 || math.IsNaN(*answer.Confidence)) {
			return fmt.Errorf("semantic response has invalid confidence for %q", name)
		}
	}
	return nil
}

func choiceCriteria(value any) (map[string]struct{}, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var criteria map[string]any
	if err := json.Unmarshal(raw, &criteria); err != nil || len(criteria) == 0 {
		return nil, errors.New("criteria must be a non-empty object")
	}
	keys := make(map[string]struct{}, len(criteria))
	for key := range criteria {
		keys[key] = struct{}{}
	}
	return keys, nil
}

func scoreCriteriaCount(value any) (int, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return 0, err
	}
	var criteria []any
	if err := json.Unmarshal(raw, &criteria); err != nil || len(criteria) == 0 {
		return 0, errors.New("criteria must be a non-empty ordered array")
	}
	return len(criteria), nil
}

func validateOutboundState(kind string, state any) error {
	raw, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("encode outbound semantic state: %w", err)
	}
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		return fmt.Errorf("decode outbound semantic state: %w", err)
	}
	workKeys := keySet("goal", "status", "declared_risk")
	actionKeys := keySet("operation", "tool", "status", "declared_risk", "resources", "decision", "reason_code")
	if err := validateObjectKeys(value["work"], "work", workKeys); err != nil {
		return err
	}
	switch kind {
	case "DUPLICATE_WORK":
		if err := exactKeys(value, "state", keySet("work", "candidates")); err != nil {
			return err
		}
		return validateArrayObjects(value["candidates"], "candidates", keySet("candidate", "goal", "status", "declared_risk", "same_repository", "same_issue", "shares_resource"), nil)
	case "RISK_REVIEW":
		if err := exactKeys(value, "state", keySet("work", "action")); err != nil {
			return err
		}
		if err := validateObjectKeys(value["action"], "action", actionKeys); err != nil {
			return err
		}
		action := value["action"].(map[string]any)
		return validateArrayObjects(action["resources"], "action.resources", keySet("type", "mode"), nil)
	case "TRACE_TRIAGE":
		if err := allowedKeys(value, "state", keySet("work", "action", "result", "steps", "outcome")); err != nil {
			return err
		}
		if err := validateObjectKeys(value["action"], "action", actionKeys); err != nil {
			return err
		}
		action := value["action"].(map[string]any)
		if err := validateArrayObjects(action["resources"], "action.resources", keySet("type", "mode"), nil); err != nil {
			return err
		}
		if err := validateObjectKeys(value["result"], "result", keySet("status", "retryable")); err != nil {
			return err
		}
		if err := validateArrayObjects(value["steps"], "steps", keySet("type", "name", "status", "attempt", "has_error", "error_class"), nil); err != nil {
			return err
		}
		if outcome, present := value["outcome"]; present {
			return validateObjectKeys(outcome, "outcome", keySet("status", "reason_code"))
		}
		return nil
	default:
		return fmt.Errorf("no outbound allowlist for semantic job kind %q", kind)
	}
}

func keySet(keys ...string) map[string]struct{} {
	set := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		set[key] = struct{}{}
	}
	return set
}

func exactKeys(value map[string]any, path string, allowed map[string]struct{}) error {
	if err := allowedKeys(value, path, allowed); err != nil {
		return err
	}
	for key := range allowed {
		if _, present := value[key]; !present {
			return fmt.Errorf("outbound semantic state is missing %s.%s", path, key)
		}
	}
	return nil
}

func allowedKeys(value map[string]any, path string, allowed map[string]struct{}) error {
	for key := range value {
		if _, permitted := allowed[key]; !permitted {
			return fmt.Errorf("non-allowlisted outbound semantic field %s.%s", path, key)
		}
	}
	return nil
}

func validateObjectKeys(value any, path string, allowed map[string]struct{}) error {
	object, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("outbound semantic field %s is not an object", path)
	}
	return allowedKeys(object, path, allowed)
}

func validateArrayObjects(value any, path string, allowed map[string]struct{}, validate func(map[string]any) error) error {
	items, ok := value.([]any)
	if !ok {
		return fmt.Errorf("outbound semantic field %s is not an array", path)
	}
	for index, item := range items {
		object, ok := item.(map[string]any)
		if !ok {
			return fmt.Errorf("outbound semantic field %s[%d] is not an object", path, index)
		}
		if err := allowedKeys(object, fmt.Sprintf("%s[%d]", path, index), allowed); err != nil {
			return err
		}
		if validate != nil {
			if err := validate(object); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) persistFailure(ctx context.Context, job semanticJob, prepared preparedEvaluation, inputHash string, latency time.Duration, cause error) {
	errorText := redactSummary(cause.Error(), 512)
	usage := []byte(`{}`)
	tx, err := s.pool.Begin(ctx)
	if err == nil {
		defer tx.Rollback(ctx)
		_, err = tx.Exec(ctx, `
			INSERT INTO semantic_evaluations(tenant_id,job_id,purpose,work_id,action_id,provider,requested_model,
				question_set_version,input_hash,usage,latency_ms,status,error)
			VALUES($1,$2,$3,NULLIF($4,''),NULLIF($5,''),$6,$7,$8,$9,$10,$11,'FAILED',$12)
			ON CONFLICT(job_id) DO NOTHING`, job.TenantID, job.ID, job.Kind, prepared.WorkID, prepared.ActionID, s.evaluator.Provider(), s.evaluator.RequestedModel(), job.QuestionSet, inputHash, usage, latency.Milliseconds(), errorText)
	}
	if err == nil {
		_, err = tx.Exec(ctx, `UPDATE semantic_jobs SET status='FAILED',last_error=$2,claim_until=NULL,completed_at=clock_timestamp(),updated_at=clock_timestamp() WHERE id=$1`, job.ID, errorText)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		s.logger.Error("failed to persist semantic failure", "job_id", job.ID, "error", err)
	}
}

func (s *Service) persistSuccess(ctx context.Context, job semanticJob, prepared preparedEvaluation, inputHash string, latency time.Duration, response Response) error {
	answers, err := json.Marshal(response.Answers)
	if err != nil {
		return err
	}
	usage, err := json.Marshal(response.Usage)
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var enabled bool
	if err := tx.QueryRow(ctx, `SELECT settings @> '{"semantic_enabled":true}'::jsonb FROM tenants WHERE id=$1`, job.TenantID).Scan(&enabled); err != nil {
		return err
	}
	if !enabled {
		return nil
	}
	evaluationID := uuid.NewString()
	_, err = tx.Exec(ctx, `
		INSERT INTO semantic_evaluations(id,tenant_id,job_id,purpose,work_id,action_id,provider,requested_model,returned_model,
			question_set_version,input_hash,answers,usage,cost_micros_usd,latency_ms,status)
		VALUES($1,$2,$3,$4,NULLIF($5,''),NULLIF($6,''),$7,$8,$9,$10,$11,$12,$13,$14,$15,'SUCCEEDED')`,
		evaluationID, job.TenantID, job.ID, job.Kind, prepared.WorkID, prepared.ActionID, s.evaluator.Provider(), s.evaluator.RequestedModel(), response.Model,
		job.QuestionSet, inputHash, answers, usage, response.CostMicros, latency.Milliseconds())
	if err != nil {
		return err
	}
	switch job.Kind {
	case "DUPLICATE_WORK":
		err = s.applyDuplicate(ctx, tx, job, prepared, response, evaluationID)
	case "RISK_REVIEW":
		err = s.applyRisk(ctx, tx, job, prepared, response, evaluationID)
	case "TRACE_TRIAGE":
		err = s.applyTrace(ctx, tx, job, prepared, response, evaluationID)
	}
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE semantic_jobs SET status='COMPLETED',last_error='',claim_until=NULL,completed_at=clock_timestamp(),updated_at=clock_timestamp() WHERE id=$1`, job.ID)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) applyDuplicate(ctx context.Context, tx pgx.Tx, job semanticJob, prepared preparedEvaluation, response Response, evaluationID string) error {
	for _, candidate := range prepared.Candidates {
		checks := []struct {
			Prefix, Kind, Relationship, Title string
		}{
			{"same_goal_", "LIKELY_DUPLICATE_WORK", "LIKELY_DUPLICATE", "Work may duplicate active work"},
			{"supersedes_", "LIKELY_SUPERSEDED_WORK", "LIKELY_SUPERSEDES", "Work may supersede active work"},
		}
		for _, check := range checks {
			answer := response.Answers[check.Prefix+candidate.Key]
			if answer.Noul == nil || *answer.Noul < 0.90 {
				continue
			}
			evidence := semanticEvidence(evaluationID, s.evaluator.Provider(), response.Model, job.QuestionSet, *answer.Noul)
			evidence["candidate_work_id"] = candidate.ID
			if err := upsertFinding(ctx, tx, job.TenantID, check.Kind, prepared.WorkID, "", candidate.ID, check.Title, evidence); err != nil {
				return err
			}
			rawEvidence, _ := json.Marshal(evidence)
			_, err := tx.Exec(ctx, `
				INSERT INTO work_edges(tenant_id,source_work_id,relationship,target_work_id,confidence,evidence_type,evidence)
				VALUES($1,$2,$3,$4,$5,'SEMANTIC_INFERENCE',$6)
				ON CONFLICT(tenant_id,source_work_id,relationship,target_work_id) DO UPDATE SET
					confidence=EXCLUDED.confidence,evidence_type='SEMANTIC_INFERENCE',evidence=EXCLUDED.evidence,updated_at=clock_timestamp()`,
				job.TenantID, prepared.WorkID, check.Relationship, candidate.ID, *answer.Noul, rawEvidence)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) applyRisk(ctx context.Context, tx pgx.Tx, job semanticJob, prepared preparedEvaluation, response Response, evaluationID string) error {
	answer := response.Answers["apparent_risk"]
	probability := probabilityAboveRisk(answer.Probabilities, prepared.DeclaredRisk)
	if probability >= 0.80 {
		evidence := semanticEvidence(evaluationID, s.evaluator.Provider(), response.Model, job.QuestionSet, probability)
		evidence["declared_risk"] = prepared.DeclaredRisk
		evidence["apparent_risk"] = answer.Choice
		if err := upsertFinding(ctx, tx, job.TenantID, "SEMANTIC_RISK_MISMATCH", prepared.WorkID, prepared.ActionID, "", "Action risk may be understated", evidence); err != nil {
			return err
		}
	}
	alignment := response.Answers["goal_action_alignment"]
	if alignment.Noul != nil && *alignment.Noul <= 0.20 {
		evidence := semanticEvidence(evaluationID, s.evaluator.Provider(), response.Model, job.QuestionSet, 1-*alignment.Noul)
		return upsertFinding(ctx, tx, job.TenantID, "SEMANTIC_GOAL_ACTION_MISMATCH", prepared.WorkID, prepared.ActionID, "", "Action may not advance the work goal", evidence)
	}
	return nil
}

func (s *Service) applyTrace(ctx context.Context, tx pgx.Tx, job semanticJob, prepared preparedEvaluation, response Response, evaluationID string) error {
	triageScore := traceTriageScore(response.Answers)
	checks := []struct {
		Name, Kind, Title string
		Invert            bool
	}{
		{"goal_action_alignment", "SEMANTIC_GOAL_ACTION_MISMATCH", "Trace may not advance the work goal", true},
		{"repetition_without_progress", "SEMANTIC_TRACE_STALLED", "Trace may show repetition without progress", false},
		{"outcome_supported", "SEMANTIC_OUTCOME_UNSUPPORTED", "Claimed outcome may not be supported by the trace", true},
	}
	for _, check := range checks {
		answer := response.Answers[check.Name]
		if answer.Noul == nil {
			continue
		}
		probability := *answer.Noul
		if check.Invert {
			probability = 1 - probability
		}
		if probability < 0.80 {
			continue
		}
		evidence := semanticEvidence(evaluationID, s.evaluator.Provider(), response.Model, job.QuestionSet, probability)
		evidence["triage_score"] = triageScore
		if err := upsertFinding(ctx, tx, job.TenantID, check.Kind, prepared.WorkID, prepared.ActionID, "", check.Title, evidence); err != nil {
			return err
		}
	}
	return nil
}

func upsertFinding(ctx context.Context, tx pgx.Tx, tenantID, kind, workID, actionID, discriminator, title string, evidence map[string]any) error {
	id := findingID(tenantID, kind, workID, actionID, discriminator)
	raw, err := json.Marshal(evidence)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO incidents(tenant_id,id,kind,work_id,action_id,status,title,evidence)
		VALUES($1,$2,$3,NULLIF($4,''),NULLIF($5,''),'OPEN',$6,$7)
		ON CONFLICT(tenant_id,id) DO UPDATE SET title=EXCLUDED.title,evidence=EXCLUDED.evidence,updated_at=clock_timestamp()`,
		tenantID, id, kind, workID, actionID, title, raw)
	return err
}

func semanticEvidence(evaluationID, provider, model, questionSet string, probability float64) map[string]any {
	return map[string]any{
		"advisory": true, "evidence_type": "SEMANTIC_INFERENCE", "evaluation_id": evaluationID,
		"provider": provider, "model": model, "question_set_version": questionSet, "probability": probability,
	}
}

func probabilityAboveRisk(probabilities map[string]float64, declared string) float64 {
	ranks := map[string]int{"LOW": 0, "MEDIUM": 1, "HIGH": 2, "CRITICAL": 3}
	declaredRank, ok := ranks[strings.ToUpper(declared)]
	if !ok {
		return 0
	}
	var probability float64
	for risk, value := range probabilities {
		if rank, known := ranks[strings.ToUpper(risk)]; known && rank > declaredRank {
			probability += value
		}
	}
	return probability
}

func traceTriageScore(answers map[string]Answer) float64 {
	probability := func(name string, invert bool) float64 {
		answer := answers[name]
		if answer.Noul == nil {
			return 0
		}
		value := *answer.Noul
		if invert {
			return 1 - value
		}
		return value
	}
	return 0.35*probability("goal_action_alignment", true) +
		0.30*probability("repetition_without_progress", false) +
		0.35*probability("outcome_supported", true)
}

func findingID(parts ...string) string {
	hash := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "sem-" + hex.EncodeToString(hash[:12])
}

var (
	emailPattern  = regexp.MustCompile(`(?i)\b[A-Z0-9._%+-]+@[A-Z0-9.-]+\.[A-Z]{2,}\b`)
	bearerPattern = regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/=-]+`)
	secretPattern = regexp.MustCompile(`\b[A-Za-z0-9_-]{32,}\b`)
	labelPattern  = regexp.MustCompile(`[^A-Za-z0-9._:/ -]+`)
	reasonPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,79}$`)
)

func redactSummary(value string, limit int) string {
	value = strings.TrimSpace(value)
	value = emailPattern.ReplaceAllString(value, "[REDACTED_EMAIL]")
	value = bearerPattern.ReplaceAllString(value, "[REDACTED_BEARER]")
	value = secretPattern.ReplaceAllString(value, "[REDACTED_TOKEN]")
	return truncateUTF8(value, limit)
}

func safeLabel(value string) string {
	value = redactSummary(value, 80)
	value = labelPattern.ReplaceAllString(strings.TrimSpace(value), "_")
	return truncateUTF8(value, 80)
}

func safeReasonCode(value string) string {
	value = strings.TrimSpace(value)
	if redactSummary(value, 80) != value {
		return "OTHER"
	}
	if value == "" || reasonPattern.MatchString(value) {
		return value
	}
	return "OTHER"
}

func truncateUTF8(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	value = value[:limit]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

func errorClass(value string) string {
	value = strings.ToLower(value)
	classes := []struct {
		Name  string
		Terms []string
	}{
		{"TIMEOUT", []string{"timeout", "deadline"}},
		{"RATE_LIMIT", []string{"rate limit", "429"}},
		{"AUTHORIZATION", []string{"unauthorized", "forbidden", "permission", "401", "403"}},
		{"CONFLICT", []string{"conflict", "409", "version"}},
		{"NETWORK", []string{"connection", "network", "dns"}},
	}
	for _, class := range classes {
		for _, term := range class.Terms {
			if strings.Contains(value, term) {
				return class.Name
			}
		}
	}
	if value != "" {
		return "OTHER"
	}
	return ""
}

// StableQuestionNames is useful to eval harnesses and fixture generation.
func StableQuestionNames(request Request) []string {
	names := make([]string, 0, len(request.Questions))
	for name := range request.Questions {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
