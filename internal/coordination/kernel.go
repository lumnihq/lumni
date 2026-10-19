package coordination

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"lumni/internal/resource"
	"lumni/internal/secrets"
	"lumni/pkg/protocol"
)

const (
	KernelVersion = "coord-v1"
	PolicyVersion = "policy-v1"
)

var (
	ErrNotFound                 = errors.New("not found")
	ErrInvalidLease             = errors.New("invalid or expired lease")
	ErrUnsupportedKernelVersion = errors.New("unsupported kernel version")
	ErrInvalidActionTransition  = errors.New("invalid action lifecycle transition")
	ErrResultPayloadMismatch    = errors.New("action result payload mismatch")
)

type DecisionSnapshot struct {
	Action            protocol.ActionCheckInput     `json:"action"`
	TenantMode        protocol.EnforcementMode      `json:"tenant_mode"`
	CandidateDecision protocol.DecisionType         `json:"candidate_decision"`
	ReasonCode        protocol.ReasonCode           `json:"reason_code"`
	Evidence          []protocol.Evidence           `json:"evidence,omitempty"`
	Evaluations       []protocol.DecisionEvaluation `json:"evaluations,omitempty"`
	EvaluatedAt       time.Time                     `json:"evaluated_at"`
	ExistingActionID  *string                       `json:"existing_action_id,omitempty"`
}

type replayAlgorithm func(DecisionSnapshot) (protocol.DecisionType, protocol.ReasonCode, error)

var replayAlgorithms = map[string]replayAlgorithm{
	KernelVersion: func(snapshot DecisionSnapshot) (protocol.DecisionType, protocol.ReasonCode, error) {
		if snapshot.CandidateDecision == "" || snapshot.ReasonCode == "" {
			return "", "", errors.New("decision snapshot is incomplete")
		}
		return wireDecision(snapshot.TenantMode, snapshot.CandidateDecision), snapshot.ReasonCode, nil
	},
}

func ReplaySnapshot(version string, raw []byte) (protocol.DecisionType, protocol.ReasonCode, error) {
	algorithm, ok := replayAlgorithms[version]
	if !ok {
		return "", "", fmt.Errorf("%w: %s", ErrUnsupportedKernelVersion, version)
	}
	var snapshot DecisionSnapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return "", "", err
	}
	return algorithm(snapshot)
}

type Kernel struct {
	cipher   *secrets.Manager
	pool     *pgxpool.Pool
	resolver *resource.Service
	leaseTTL time.Duration
	now      func() time.Time
}

func New(pool *pgxpool.Pool, resolver *resource.Service, leaseTTL time.Duration) *Kernel {
	return &Kernel{pool: pool, resolver: resolver, leaseTTL: leaseTTL, now: time.Now}
}

type draft struct {
	decision             protocol.DecisionType
	reason               protocol.ReasonCode
	evidence             []protocol.Evidence
	retryAfterMS         *int64
	existingActionID     *string
	externalResultID     *string
	conflictingActionIDs []string
	approvalID           *string
	controlMode          *protocol.EnforcementMode
	autopilot            bool
}

func (k *Kernel) Check(ctx context.Context, tenantID string, in protocol.ActionCheckInput) (protocol.Decision, error) {
	resolved, argumentsHash, err := k.resolver.Resolve(ctx, tenantID, in)
	unresolved := err != nil && strings.Contains(err.Error(), string(protocol.ReasonResourceUnresolved))
	if err != nil && !unresolved {
		return protocol.Decision{}, err
	}
	if unresolved {
		resolved.Resources = nil
		resolved.EffectKey = nil
	}
	in = resolved
	if err := in.Validate(); err != nil {
		return protocol.Decision{}, err
	}
	if in.ActionID == "" {
		in.ActionID = "act_" + uuid.NewString()
	}
	if in.Source == "" {
		in.Source = "api"
	}
	if in.Risk == "" {
		in.Risk = protocol.RiskMedium
	}

	tx, err := k.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return protocol.Decision{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared(hashtextextended($1,0))`, tenantID+":dependencies"); err != nil {
		return protocol.Decision{}, err
	}

	var mode protocol.EnforcementMode
	if err := tx.QueryRow(ctx, `SELECT enforcement_mode FROM tenants WHERE id = $1`, tenantID).Scan(&mode); err != nil {
		return protocol.Decision{}, err
	}
	if in.ObserveOnly {
		mode = protocol.ModeShadow
	}
	var operationMode string
	if err := tx.QueryRow(ctx, `SELECT mode FROM enforcement_rules WHERE tenant_id=$1 AND operation IN($2,'*') AND rule='*' ORDER BY (operation=$2) DESC LIMIT 1`, tenantID, in.Operation).Scan(&operationMode); err == nil && !in.ObserveOnly {
		mode = protocol.EnforcementMode(operationMode)
	} else if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return protocol.Decision{}, err
	}
	if err := lockCoordinationIdentities(ctx, tx, tenantID, in); err != nil {
		return protocol.Decision{}, err
	}
	for i := range in.Resources {
		if err := ensureResource(ctx, tx, tenantID, &in.Resources[i]); err != nil {
			return protocol.Decision{}, err
		}
	}
	if in.RecheckID == "" {
		var identity *string
		err := tx.QueryRow(ctx, `SELECT admission_identity_hash FROM actions WHERE tenant_id=$1 AND id=$2`, tenantID, in.ActionID).Scan(&identity)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return protocol.Decision{}, err
		}
		if identity != nil && *identity != admissionIdentity(in, argumentsHash, true) {
			return protocol.Decision{}, errors.New("ACTION_IDENTITY_MISMATCH")
		}
	}

	resumeApproved, early, err := k.resolveIdempotency(ctx, tx, tenantID, mode, &in, argumentsHash)
	if err != nil {
		return protocol.Decision{}, err
	}
	if early != nil {
		return *early, nil
	}
	if in.RecheckID != "" {
		var status string
		if err := tx.QueryRow(ctx, `SELECT status FROM actions WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, tenantID, in.ActionID).Scan(&status); err != nil {
			return protocol.Decision{}, err
		}
		var receipt *string
		err := tx.QueryRow(ctx, `SELECT receipt_id::text FROM action_rechecks WHERE tenant_id=$1 AND action_id=$2 AND id=$3`, tenantID, in.ActionID, in.RecheckID).Scan(&receipt)
		if err == nil && receipt != nil {
			return k.decisionReceipt(ctx, tx, tenantID, in.ActionID, *receipt)
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return protocol.Decision{}, err
		}
		if status == "RESERVED" {
			var live bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM leases WHERE tenant_id=$1 AND action_id=$2 AND expires_at>now())`, tenantID, in.ActionID).Scan(&live); err != nil {
				return protocol.Decision{}, err
			}
			if !live {
				if _, err := tx.Exec(ctx, `DELETE FROM leases WHERE tenant_id=$1 AND action_id=$2`, tenantID, in.ActionID); err != nil {
					return protocol.Decision{}, err
				}
				status = "PROPOSED"
			}
		}
		if status == "ACTIVE" || status == "SUCCEEDED" || status == "RESERVED" {
			return protocol.Decision{}, ErrInvalidActionTransition
		}
		if _, err := tx.Exec(ctx, `INSERT INTO action_rechecks(tenant_id,action_id,id)VALUES($1,$2,$3)ON CONFLICT DO NOTHING`, tenantID, in.ActionID, in.RecheckID); err != nil {
			return protocol.Decision{}, err
		}
	}

	if !resumeApproved && in.RecheckID == "" {
		exists, err := actionExists(ctx, tx, tenantID, in.ActionID)
		if err != nil {
			return protocol.Decision{}, err
		}
		if exists {
			var same bool
			if err := tx.QueryRow(ctx, `SELECT arguments_hash=$3 AND operation=$4 AND work_id=$5 AND actor_id=$6 FROM actions WHERE tenant_id=$1 AND id=$2`, tenantID, in.ActionID, argumentsHash, in.Operation, in.WorkID, in.ActorID).Scan(&same); err != nil {
				return protocol.Decision{}, err
			}
			if !same {
				return protocol.Decision{}, errors.New("ACTION_IDENTITY_MISMATCH")
			}
			return k.latestDecision(ctx, tx, tenantID, in.ActionID)
		}
		if err := insertAction(ctx, tx, tenantID, in, argumentsHash); err != nil {
			return protocol.Decision{}, err
		}
		if err := k.captureIntent(ctx, tx, tenantID, in); err != nil {
			return protocol.Decision{}, err
		}
		if in.IdempotencyKey != nil {
			_, err = tx.Exec(ctx, `INSERT INTO idempotency_records
				(tenant_id,idempotency_key,operation,payload_hash,action_id,status)
				VALUES ($1,$2,$3,$4,$5,'ACTIVE')`, tenantID, *in.IdempotencyKey, in.Operation, argumentsHash, in.ActionID)
			if err != nil {
				return protocol.Decision{}, err
			}
		}
		if err := insertActionResources(ctx, tx, tenantID, in); err != nil {
			return protocol.Decision{}, err
		}
	}
	pause, paused, err := checkPaused(ctx, tx, tenantID, in.ActorID)
	if err != nil {
		return protocol.Decision{}, err
	}
	if paused {
		return k.finish(ctx, tx, tenantID, mode, in, pause, nil, "")
	}
	if unresolved || len(in.Resources) == 0 {
		return k.finish(ctx, tx, tenantID, mode, in, draft{decision: protocol.DecisionReject, reason: protocol.ReasonResourceUnresolved}, nil, "")
	}

	var candidates []draft
	control, controlled, err := checkControls(ctx, tx, tenantID, in)
	if err != nil {
		return protocol.Decision{}, err
	}
	if controlled {
		candidates = append(candidates, control)
	}
	for _, check := range []func(context.Context, pgx.Tx, string, protocol.ActionCheckInput) (draft, bool, error){checkHoldsAndDependencies, detectDuplicate, detectStale} {
		d, found, e := check(ctx, tx, tenantID, in)
		if e != nil {
			return protocol.Decision{}, e
		}
		if found {
			candidates = append(candidates, d)
		}
	}
	conflicting, reason, err := detectConflicts(ctx, tx, tenantID, in)
	if err != nil {
		return protocol.Decision{}, err
	}
	if len(conflicting) > 0 {
		retry := int64(1000)
		candidates = append(candidates, draft{decision: protocol.DecisionWait, reason: reason, conflictingActionIDs: conflicting, retryAfterMS: &retry})
	}
	policyDraft, matched, err := evaluatePolicy(ctx, tx, tenantID, in)
	if err != nil {
		return protocol.Decision{}, err
	}
	if matched && policyDraft.decision == protocol.DecisionRequireApproval {
		var state string
		e := tx.QueryRow(ctx, `SELECT CASE WHEN expires_at<=now() AND status='PENDING' THEN 'EXPIRED' ELSE status END FROM approvals WHERE tenant_id=$1 AND action_id=$2 ORDER BY requested_at DESC LIMIT 1`, tenantID, in.ActionID).Scan(&state)
		if e != nil && !errors.Is(e, pgx.ErrNoRows) {
			return protocol.Decision{}, e
		}
		if state == "APPROVED" {
			matched = false
		}
		if state == "DENIED" || state == "EXPIRED" {
			policyDraft.decision = protocol.DecisionReject
			policyDraft.reason = protocol.ReasonCode("APPROVAL_" + state)
		}
	}
	if matched {
		candidates = append(candidates, policyDraft)
	}
	var shadow *draft
	for _, candidate := range candidates {
		candidateMode := mode
		if candidate.controlMode != nil && *candidate.controlMode != protocol.ModeEnforce {
			candidateMode = *candidate.controlMode
		}
		if !in.ObserveOnly && candidate.controlMode == nil {
			var selected protocol.EnforcementMode
			e := tx.QueryRow(ctx, `SELECT mode FROM enforcement_rules WHERE tenant_id=$1 AND operation IN($2,'*') AND rule=$3 ORDER BY (operation=$2)DESC LIMIT 1`, tenantID, in.Operation, string(candidate.reason)).Scan(&selected)
			if e == nil {
				candidateMode = selected
			} else if !errors.Is(e, pgx.ErrNoRows) {
				return protocol.Decision{}, e
			}
		}
		if candidateMode != protocol.ModeEnforce {
			if shadow == nil {
				copy := candidate
				shadow = &copy
			}
			continue
		}
		if candidate.decision == protocol.DecisionRequireApproval {
			approvalID := uuid.NewString()
			var policyID *string
			for _, ev := range candidate.evidence {
				if ev.Type == "POLICY" {
					if id, ok := ev.Data["policy_id"].(string); ok {
						policyID = &id
					}
				}
			}
			if _, e := tx.Exec(ctx, `INSERT INTO approvals(id,tenant_id,action_id,status,reason_code,expires_at,policy_id)VALUES($1,$2,$3,'PENDING',$4,now()+interval '24 hours',$5)ON CONFLICT DO NOTHING`, approvalID, tenantID, in.ActionID, candidate.reason, policyID); e != nil {
				return protocol.Decision{}, e
			}
			if e := tx.QueryRow(ctx, `SELECT id::text FROM approvals WHERE tenant_id=$1 AND action_id=$2 AND status='PENDING'`, tenantID, in.ActionID).Scan(&approvalID); e != nil {
				return protocol.Decision{}, e
			}
			candidate.approvalID = &approvalID
		}
		return k.finish(ctx, tx, tenantID, candidateMode, in, candidate, nil, "")
	}
	if shadow != nil {
		return k.finish(ctx, tx, tenantID, protocol.ModeShadow, in, *shadow, nil, "")
	}
	if mode != protocol.ModeEnforce {
		return k.finish(ctx, tx, tenantID, mode, in, draft{decision: protocol.DecisionAllow, reason: protocol.ReasonNoConflict, autopilot: policyDraft.autopilot}, nil, "")
	}
	grants, token, busy, err := k.acquire(ctx, tx, tenantID, in)
	if err != nil {
		return protocol.Decision{}, err
	}
	if len(busy) > 0 {
		retry := int64(1000)
		return k.finish(ctx, tx, tenantID, mode, in, draft{decision: protocol.DecisionWait, reason: protocol.ReasonLeaseBusy, conflictingActionIDs: busy, retryAfterMS: &retry}, nil, "")
	}
	return k.finish(ctx, tx, tenantID, mode, in, draft{decision: protocol.DecisionAllow, reason: protocol.ReasonNoConflict, autopilot: policyDraft.autopilot}, grants, token)
}

// lockCoordinationIdentities makes concurrent retries with the same caller
// identity deterministic before any ownership row is read or created. The
// sorted advisory locks are transaction-scoped and cannot outlive a failed
// admission transaction.
func lockCoordinationIdentities(ctx context.Context, tx pgx.Tx, tenantID string, in protocol.ActionCheckInput) error {
	keys := []string{"action:" + tenantID + ":" + in.ActionID}
	if in.IdempotencyKey != nil {
		keys = append(keys, "idempotency:"+tenantID+":"+*in.IdempotencyKey)
	}
	if in.EffectKey != nil {
		keys = append(keys, "effect:"+tenantID+":"+*in.EffectKey)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, key); err != nil {
			return err
		}
	}
	return nil
}

func (k *Kernel) resolveIdempotency(ctx context.Context, tx pgx.Tx, tenantID string, mode protocol.EnforcementMode, in *protocol.ActionCheckInput, argumentsHash string) (bool, *protocol.Decision, error) {
	if in.RecheckID != "" {
		return false, nil, nil
	}
	if in.IdempotencyKey == nil {
		return false, nil, nil
	}
	var existingAction, operation, payloadHash, state string
	var contract *string
	err := tx.QueryRow(ctx, `SELECT i.action_id,i.operation,i.payload_hash,i.status,a.idempotency_identity_hash FROM idempotency_records i JOIN actions a ON a.tenant_id=i.tenant_id AND a.id=i.action_id
		WHERE i.tenant_id=$1 AND i.idempotency_key=$2 FOR UPDATE OF i`, tenantID, *in.IdempotencyKey).Scan(&existingAction, &operation, &payloadHash, &state, &contract)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil, nil
	}
	if err != nil {
		return false, nil, err
	}
	if operation != in.Operation || payloadHash != argumentsHash || (contract != nil && *contract != admissionIdentity(*in, argumentsHash, false)) {
		if err := insertAction(ctx, tx, tenantID, *in, argumentsHash); err != nil {
			return false, nil, err
		}
		if err := insertActionResources(ctx, tx, tenantID, *in); err != nil {
			return false, nil, err
		}
		if err := k.captureIntent(ctx, tx, tenantID, *in); err != nil {
			return false, nil, err
		}
		d, err := k.finish(ctx, tx, tenantID, mode, *in, draft{decision: protocol.DecisionReject, reason: protocol.ReasonIdempotencyPayloadMismatch, existingActionID: &existingAction}, nil, "")
		return false, &d, err
	}
	if mode == protocol.ModeShadow && in.ActionID != existingAction {
		// Shadow observes each real invocation while the first action keeps ownership.
		if err := insertAction(ctx, tx, tenantID, *in, argumentsHash); err != nil {
			return false, nil, err
		}
		if err := insertActionResources(ctx, tx, tenantID, *in); err != nil {
			return false, nil, err
		}
		if err := k.captureIntent(ctx, tx, tenantID, *in); err != nil {
			return false, nil, err
		}
		candidate := draft{decision: protocol.DecisionWait, reason: protocol.ReasonExactDuplicateActive, existingActionID: &existingAction}
		if state == "COMPLETED" {
			candidate.decision = protocol.DecisionDeduplicate
			candidate.reason = protocol.ReasonEffectAlreadyCommitted
		}
		d, err := k.finish(ctx, tx, tenantID, mode, *in, candidate, nil, "")
		return false, &d, err
	}
	in.ActionID = existingAction
	if state == "COMPLETED" {
		var resultID *string
		_ = tx.QueryRow(ctx, `SELECT external_result_id FROM action_results WHERE tenant_id=$1 AND action_id=$2`, tenantID, existingAction).Scan(&resultID)
		d, err := k.finish(ctx, tx, tenantID, mode, *in, draft{decision: protocol.DecisionDeduplicate, reason: protocol.ReasonEffectAlreadyCommitted, existingActionID: &existingAction, externalResultID: resultID}, nil, "")
		return false, &d, err
	}
	var actionStatus string
	if err := tx.QueryRow(ctx, `SELECT status FROM actions WHERE tenant_id=$1 AND id=$2`, tenantID, existingAction).Scan(&actionStatus); err != nil {
		return false, nil, err
	}
	if actionStatus == string(protocol.ActionAwaitingApproval) {
		var approvalStatus string
		err := tx.QueryRow(ctx, `SELECT status FROM approvals WHERE tenant_id=$1 AND action_id=$2 ORDER BY requested_at DESC LIMIT 1`, tenantID, existingAction).Scan(&approvalStatus)
		if err == nil && approvalStatus == "APPROVED" {
			return true, nil, nil
		}
		d, err := k.latestDecision(ctx, tx, tenantID, existingAction)
		return false, &d, err
	}
	if actionStatus == string(protocol.ActionReserved) || actionStatus == string(protocol.ActionActive) {
		d, err := k.latestDecision(ctx, tx, tenantID, existingAction)
		return false, &d, err
	}
	return false, nil, nil
}

func detectDuplicate(ctx context.Context, tx pgx.Tx, tenantID string, in protocol.ActionCheckInput) (draft, bool, error) {
	if in.EffectKey == nil {
		return draft{}, false, nil
	}
	var existingAction, status string
	var externalID *string
	err := tx.QueryRow(ctx, `SELECT action_id,status,external_id FROM effect_occurrences WHERE tenant_id=$1 AND (effect_key=$2 OR effect_key IN(SELECT legacy_key FROM effect_aliases WHERE tenant_id=$1 AND current_key=$2)) AND action_id<>$3 AND status IN('COMMITTED','VERIFIED') ORDER BY created_at LIMIT 1`, tenantID, *in.EffectKey, in.ActionID).Scan(&existingAction, &status, &externalID)
	if err == nil && existingAction != in.ActionID && (status == "COMMITTED" || status == "VERIFIED") {
		return draft{decision: protocol.DecisionDeduplicate, reason: protocol.ReasonEffectAlreadyCommitted, existingActionID: &existingAction, externalResultID: externalID}, true, nil
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return draft{}, false, err
	}
	var activeAction string
	err = tx.QueryRow(ctx, `SELECT id FROM actions WHERE tenant_id=$1 AND effect_key=$2 AND id<>$3
		AND status IN ('RESERVED','ACTIVE','AWAITING_APPROVAL') ORDER BY requested_at LIMIT 1`, tenantID, *in.EffectKey, in.ActionID).Scan(&activeAction)
	if err == nil {
		retry := int64(1000)
		return draft{decision: protocol.DecisionWait, reason: protocol.ReasonExactDuplicateActive, existingActionID: &activeAction, retryAfterMS: &retry}, true, nil
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return draft{}, false, err
	}
	return draft{}, false, nil
}

func detectStale(ctx context.Context, tx pgx.Tx, tenantID string, in protocol.ActionCheckInput) (draft, bool, error) {
	for _, r := range in.Resources {
		if r.ObservedVersion == nil {
			continue
		}
		var current, source *string
		if err := tx.QueryRow(ctx, `SELECT current_version,version_source FROM resources WHERE tenant_id=$1 AND id=$2`, tenantID, r.ID).Scan(&current, &source); err != nil {
			return draft{}, false, err
		}
		if current == nil {
			continue
		}
		currentSource := ""
		if source != nil {
			currentSource = *source
		}
		if currentSource != "" && r.VersionSource != "" && currentSource != r.VersionSource {
			return draft{decision: protocol.DecisionReplan, reason: protocol.ReasonVersionSourceChanged, evidence: versionEvidence(r.ID, *r.ObservedVersion, *current, r.VersionSource, currentSource)}, true, nil
		}
		if *current != *r.ObservedVersion {
			return draft{decision: protocol.DecisionReplan, reason: protocol.ReasonStaleObservation, evidence: versionEvidence(r.ID, *r.ObservedVersion, *current, r.VersionSource, currentSource)}, true, nil
		}
	}
	return draft{}, false, nil
}

func ensureResource(ctx context.Context, tx pgx.Tx, tenantID string, intent *protocol.ResourceIntent) error {
	ref := resource.Canonicalize(intent.ResourceRef)
	intent.ResourceRef = ref
	var typ, externalID, namespace string
	err := tx.QueryRow(ctx, `INSERT INTO resources (tenant_id,id,type,external_id,namespace,metadata)
		VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (tenant_id,id) DO UPDATE SET updated_at=resources.updated_at
		RETURNING type,external_id,namespace`, tenantID, ref.ID, ref.Type, ref.ExternalID, ref.Namespace, jsonOrDefault(ref.Metadata, []byte("{}"))).Scan(&typ, &externalID, &namespace)
	if err != nil {
		return err
	}
	if typ != ref.Type || externalID != ref.ExternalID || namespace != ref.Namespace {
		return fmt.Errorf("resource id %q is already bound to a different identity", ref.ID)
	}
	return nil
}

func insertAction(ctx context.Context, tx pgx.Tx, tenantID string, in protocol.ActionCheckInput, argumentsHash string) error {
	_, err := tx.Exec(ctx, `INSERT INTO actions
		(tenant_id,id,work_id,actor_id,session_id,operation,tool,source,status,risk,arguments_hash,arguments,idempotency_key,effect_key,metadata,admission_identity_hash,idempotency_identity_hash)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'PROPOSED',$9,$10,$11,$12,$13,$14,$15,$16)`,
		tenantID, in.ActionID, in.WorkID, in.ActorID, in.SessionID, in.Operation, in.Tool, in.Source, in.Risk,
		argumentsHash, jsonOrDefault(in.Arguments, []byte("{}")), in.IdempotencyKey, in.EffectKey, jsonOrDefault(in.Metadata, []byte("{}")), admissionIdentity(in, argumentsHash, true), admissionIdentity(in, argumentsHash, false))
	return err
}

func admissionIdentity(in protocol.ActionCheckInput, argumentsHash string, includeOwner bool) string {
	resources := make([]map[string]any, 0, len(in.Resources))
	for _, r := range in.Resources {
		resources = append(resources, map[string]any{"id": r.ID, "mode": r.Mode, "observed_version": r.ObservedVersion, "expected_version": r.ExpectedVersion, "version_source": r.VersionSource})
	}
	sort.Slice(resources, func(i, j int) bool {
		a, _ := json.Marshal(resources[i])
		b, _ := json.Marshal(resources[j])
		return string(a) < string(b)
	})
	value := map[string]any{"operation": in.Operation, "arguments_hash": argumentsHash, "resources": resources, "effect_key": in.EffectKey, "risk": in.Risk, "verification": in.Verification}
	if includeOwner {
		value["owner"] = []any{in.WorkID, in.ActorID, in.SessionID, in.StepID, in.Tool, in.Source, in.IdempotencyKey}
	}
	b, _ := json.Marshal(value)
	var canonical any
	_ = json.Unmarshal(b, &canonical)
	b, _ = json.Marshal(canonical)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func insertActionResources(ctx context.Context, tx pgx.Tx, tenantID string, in protocol.ActionCheckInput) error {
	for _, r := range in.Resources {
		_, err := tx.Exec(ctx, `INSERT INTO action_resources
			(tenant_id,action_id,resource_id,mode,operation,observed_version,expected_version,version_source,confidence,evidence_type,evidence,observed_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, tenantID, in.ActionID, r.ID, r.Mode, r.Operation,
			r.ObservedVersion, r.ExpectedVersion, r.VersionSource, r.Confidence, r.EvidenceType, jsonOrDefault(r.Evidence, []byte("{}")), r.ObservedAt)
		if err != nil {
			return err
		}
	}
	return nil
}

func actionExists(ctx context.Context, tx pgx.Tx, tenantID, actionID string) (bool, error) {
	var exists bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM actions WHERE tenant_id=$1 AND id=$2)`, tenantID, actionID).Scan(&exists)
	return exists, err
}

func detectConflicts(ctx context.Context, tx pgx.Tx, tenantID string, in protocol.ActionCheckInput) ([]string, protocol.ReasonCode, error) {
	var writeIDs []string
	for _, r := range in.Resources {
		if protocol.IsWrite(r.Mode) {
			writeIDs = append(writeIDs, r.ID)
		}
	}
	if len(writeIDs) == 0 {
		return nil, protocol.ReasonNoConflict, nil
	}
	rows, err := tx.Query(ctx, `SELECT DISTINCT a.id,a.operation,ar.mode,r.type
		FROM action_resources ar
		JOIN actions a ON a.tenant_id=ar.tenant_id AND a.id=ar.action_id
		JOIN resources r ON r.tenant_id=ar.tenant_id AND r.id=ar.resource_id
		WHERE ar.tenant_id=$1 AND ar.resource_id=ANY($2) AND a.id<>$3
		AND a.status IN ('RESERVED','ACTIVE') AND ar.mode IN ('WRITE','CREATE','DELETE')`, tenantID, writeIDs, in.ActionID)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	type conflictCandidate struct{ id, operation, mode, resourceType string }
	var candidates []conflictCandidate
	for rows.Next() {
		var candidate conflictCandidate
		if err := rows.Scan(&candidate.id, &candidate.operation, &candidate.mode, &candidate.resourceType); err != nil {
			return nil, "", err
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	rows.Close()
	var conflicts []string
	reason := protocol.ReasonWriteWriteConflict
	for _, candidate := range candidates {
		var configured string
		err := tx.QueryRow(ctx, `SELECT reason_code FROM conflict_rules WHERE tenant_id=$1 AND enabled
			AND (resource_type='*' OR resource_type=$2)
			AND ((left_operation=$3 AND right_operation=$4) OR (left_operation=$4 AND right_operation=$3))
			AND outcome='WAIT' LIMIT 1`, tenantID, candidate.resourceType, in.Operation, candidate.operation).Scan(&configured)
		if err == nil {
			reason = protocol.ReasonCode(configured)
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return nil, "", err
		} else if candidate.mode == "DELETE" {
			reason = protocol.ReasonDeleteWriteConflict
		}
		conflicts = append(conflicts, candidate.id)
	}
	return uniqueSorted(conflicts), reason, nil
}

func evaluatePolicy(ctx context.Context, tx pgx.Tx, tenantID string, in protocol.ActionCheckInput) (draft, bool, error) {
	rows, err := tx.Query(ctx, `SELECT id::text,operation,resource_type,minimum_risk,argument_pointer,numeric_threshold,outcome,reason_code
		FROM policy_rules WHERE tenant_id=$1 AND enabled ORDER BY version DESC,created_at`, tenantID)
	if err != nil {
		return draft{}, false, err
	}
	defer rows.Close()
	// Collect before consulting grants: pgx cannot issue another query while
	// a result stream on this transaction is open.
	type policy struct {
		id, operation, resourceType, outcome, reason string
		minimumRisk, pointer                         *string
		threshold                                    *float64
	}
	policies := []policy{}
	for rows.Next() {
		var p policy
		if err := rows.Scan(&p.id, &p.operation, &p.resourceType, &p.minimumRisk, &p.pointer, &p.threshold, &p.outcome, &p.reason); err != nil {
			return draft{}, false, err
		}
		policies = append(policies, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return draft{}, false, err
	}
	exempted := false
	var args any
	_ = json.Unmarshal(in.Arguments, &args)
	for _, p := range policies {
		id, operation, resourceType, outcome, reason := p.id, p.operation, p.resourceType, p.outcome, p.reason
		minimumRisk, pointer, threshold := p.minimumRisk, p.pointer, p.threshold
		if operation != "*" && operation != in.Operation {
			continue
		}
		if resourceType != "*" && !hasResourceType(in.Resources, resourceType) {
			continue
		}
		if minimumRisk != nil && riskRank(in.Risk) < riskRank(protocol.RiskLevel(*minimumRisk)) {
			continue
		}
		if pointer != nil && threshold != nil {
			value, ok := resource.LookupPointer(args, *pointer)
			if !ok || numeric(value) <= *threshold {
				continue
			}
		}
		d := draft{reason: protocol.ReasonCode(reason), evidence: []protocol.Evidence{{Type: "POLICY", Data: map[string]any{"policy_id": id}}}}
		switch outcome {
		case "REQUIRE_APPROVAL":
			exempt, e := autopilotExempts(ctx, tx, tenantID, in, id)
			if e != nil {
				return draft{}, false, e
			}
			if exempt {
				exempted = true
				continue
			}
			d.decision = protocol.DecisionRequireApproval
		case "REJECT":
			d.decision = protocol.DecisionReject
		default:
			d.decision = protocol.DecisionWarn
		}
		return d, true, nil
	}
	return draft{autopilot: exempted}, false, nil
}

func (k *Kernel) acquire(ctx context.Context, tx pgx.Tx, tenantID string, in protocol.ActionCheckInput) ([]protocol.LeaseGrant, string, []string, error) {
	var ids []string
	for _, r := range in.Resources {
		if protocol.IsWrite(r.Mode) {
			ids = append(ids, r.ID)
		}
	}
	ids = uniqueSorted(ids)
	if len(ids) == 0 {
		return nil, "", nil, nil
	}
	rows, err := tx.Query(ctx, `SELECT id FROM resources WHERE tenant_id=$1 AND id=ANY($2) ORDER BY id FOR UPDATE`, tenantID, ids)
	if err != nil {
		return nil, "", nil, err
	}
	locked := 0
	for rows.Next() {
		locked++
	}
	rows.Close()
	if locked != len(ids) {
		return nil, "", nil, errors.New("resource disappeared during admission")
	}
	if _, err = tx.Exec(ctx, `DELETE FROM leases WHERE tenant_id=$1 AND resource_id=ANY($2) AND expires_at<=clock_timestamp()`, tenantID, ids); err != nil {
		return nil, "", nil, err
	}
	busyRows, err := tx.Query(ctx, `SELECT DISTINCT action_id FROM leases WHERE tenant_id=$1 AND resource_id=ANY($2)
		AND expires_at>clock_timestamp() AND action_id<>$3`, tenantID, ids, in.ActionID)
	if err != nil {
		return nil, "", nil, err
	}
	var busy []string
	for busyRows.Next() {
		var id string
		if err := busyRows.Scan(&id); err != nil {
			busyRows.Close()
			return nil, "", nil, err
		}
		busy = append(busy, id)
	}
	busyRows.Close()
	if len(busy) > 0 {
		return nil, "", uniqueSorted(busy), nil
	}
	token, err := randomToken()
	if err != nil {
		return nil, "", nil, err
	}
	hash := sha256.Sum256([]byte(token))
	reservationID := uuid.NewString()
	expiresAt := k.now().Add(k.leaseTTL)
	grants := make([]protocol.LeaseGrant, 0, len(ids))
	for _, id := range ids {
		var fence int64
		if err := tx.QueryRow(ctx, `UPDATE resources SET fencing_token=fencing_token+1,updated_at=clock_timestamp()
			WHERE tenant_id=$1 AND id=$2 RETURNING fencing_token`, tenantID, id).Scan(&fence); err != nil {
			return nil, "", nil, err
		}
		_, err := tx.Exec(ctx, `INSERT INTO leases
			(tenant_id,reservation_id,resource_id,action_id,mode,fencing_token,token_hash,expires_at)
			VALUES ($1,$2,$3,$4,'WRITE',$5,$6,$7)`, tenantID, reservationID, id, in.ActionID, fence, hash[:], expiresAt)
		if err != nil {
			return nil, "", nil, err
		}
		grants = append(grants, protocol.LeaseGrant{ResourceID: id, FencingToken: fence, ExpiresAt: expiresAt})
	}
	_, err = tx.Exec(ctx, `UPDATE actions SET lease_token_hash=$3 WHERE tenant_id=$1 AND id=$2`, tenantID, in.ActionID, hash[:])
	return grants, token, nil, err
}

func (k *Kernel) finish(ctx context.Context, tx pgx.Tx, tenantID string, mode protocol.EnforcementMode, in protocol.ActionCheckInput, d draft, grants []protocol.LeaseGrant, token string) (protocol.Decision, error) {
	enforced := mode == protocol.ModeEnforce
	wire := wireDecision(mode, d.decision)
	status := protocol.ActionProposed
	if enforced {
		switch d.decision {
		case protocol.DecisionAllow:
			status = protocol.ActionReserved
		case protocol.DecisionRequireApproval:
			status = protocol.ActionAwaitingApproval
		case protocol.DecisionReject:
			status = protocol.ActionFailed
		case protocol.DecisionDeduplicate:
			status = protocol.ActionSucceeded
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE actions SET status=$3 WHERE tenant_id=$1 AND id=$2 AND status<>'SUCCEEDED'`, tenantID, in.ActionID, status); err != nil {
		return protocol.Decision{}, err
	}
	receiptID := uuid.NewString()
	now := k.now().UTC()
	evaluations := buildEvaluations(in, d, grants)
	freshness, err := freshnessDetails(ctx, tx, tenantID, in)
	if err != nil {
		return protocol.Decision{}, err
	}
	for i := range evaluations {
		if evaluations[i].Stage == "resource_versions" {
			evaluations[i].Details = freshness
		}
		if evaluations[i].Stage == "policy" && d.autopilot {
			evaluations[i].Details = map[string]any{"autopilot": true}
		}
	}
	safeIntent := in
	safeIntent.Arguments = nil
	safeIntent.Metadata = nil
	snapshot, _ := json.Marshal(DecisionSnapshot{Action: safeIntent, TenantMode: mode, CandidateDecision: d.decision, ReasonCode: d.reason, Evidence: d.evidence, Evaluations: evaluations, EvaluatedAt: now, ExistingActionID: d.existingActionID})
	evidence, _ := json.Marshal(d.evidence)
	evaluationJSON, _ := json.Marshal(evaluations)
	_, err = tx.Exec(ctx, `INSERT INTO decisions
		(id,tenant_id,action_id,decision,reason_code,enforced,confidence,retry_after_ms,evidence,evaluations,snapshot,kernel_version,resolver_version,policy_version,created_at)
		VALUES ($1,$2,$3,$4,$5,$6,1,$7,$8,$9,$10,$11,$12,$13,$14)`, receiptID, tenantID, in.ActionID, wire,
		d.reason, enforced, d.retryAfterMS, evidence, evaluationJSON, snapshot, KernelVersion, resource.ResolverVersion, PolicyVersion, now)
	if err != nil {
		return protocol.Decision{}, err
	}
	payload, _ := json.Marshal(map[string]any{"receipt_id": receiptID, "action_id": in.ActionID, "decision": wire, "reason_code": d.reason, "enforced": enforced})
	if _, err = tx.Exec(ctx, `INSERT INTO events (tenant_id,id,type,entity_type,entity_id,source,occurred_at,payload)
		VALUES ($1,$2,$3,'action',$4,'kernel',$5,$6)`, tenantID, "evt_"+uuid.NewString(), "DECISION_"+string(wire), in.ActionID, now, payload); err != nil {
		return protocol.Decision{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO outbox (tenant_id,topic,key,payload) VALUES ($1,'decision.created',$2,$3)`, tenantID, in.ActionID, payload); err != nil {
		return protocol.Decision{}, err
	}
	if in.RecheckID != "" {
		if _, err := tx.Exec(ctx, `UPDATE action_rechecks SET receipt_id=$4 WHERE tenant_id=$1 AND action_id=$2 AND id=$3`, tenantID, in.ActionID, in.RecheckID, receiptID); err != nil {
			return protocol.Decision{}, err
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO work_journal(tenant_id,work_id,step_id,type,identity,payload)VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT DO NOTHING`, tenantID, in.WorkID, in.StepID, "DECISION_"+string(wire), receiptID, payload); err != nil {
		return protocol.Decision{}, err
	}
	if d.decision != protocol.DecisionAllow {
		if _, err := tx.Exec(ctx, `INSERT INTO incidents(tenant_id,id,kind,work_id,action_id,title,evidence)VALUES($1,$2,$3,$4,$5,$3,$6) ON CONFLICT DO NOTHING`, tenantID, "finding_"+receiptID, d.reason, in.WorkID, in.ActionID, payload); err != nil {
			return protocol.Decision{}, err
		}
	}
	out := protocol.Decision{SchemaVersion: protocol.SchemaVersion, ActionID: in.ActionID, ReceiptID: receiptID,
		Decision: wire, ReasonCode: d.reason, Enforced: enforced, Confidence: 1, RetryAfterMS: d.retryAfterMS,
		Evidence: d.evidence, Evaluations: evaluations, ExistingActionID: d.existingActionID, ExternalResultID: d.externalResultID,
		ConflictingActionIDs: d.conflictingActionIDs, ApprovalID: d.approvalID, KernelVersion: KernelVersion,
		ResolverVersion: resource.ResolverVersion, PolicyVersion: PolicyVersion, CreatedAt: now, Leases: grants}
	if token != "" {
		out.LeaseToken = &token
		if len(grants) > 0 {
			expires := grants[0].ExpiresAt
			out.ExpiresAt = &expires
		}
	}
	if k.cipher != nil {
		encoded, _ := json.Marshal(out)
		cipher, err := k.cipher.Encrypt(encoded)
		if err != nil {
			return protocol.Decision{}, err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO admission_payloads(tenant_id,receipt_id,response_ciphertext)VALUES($1,$2,$3)`, tenantID, receiptID, cipher); err != nil {
			return protocol.Decision{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return protocol.Decision{}, err
	}
	return out, nil
}

func wireDecision(mode protocol.EnforcementMode, candidate protocol.DecisionType) protocol.DecisionType {
	if mode == protocol.ModeEnforce {
		return candidate
	}
	switch candidate {
	case protocol.DecisionWait:
		return protocol.DecisionWouldWait
	case protocol.DecisionReplan:
		return protocol.DecisionWouldReplan
	case protocol.DecisionDeduplicate:
		return protocol.DecisionWouldDedup
	case protocol.DecisionRequireApproval:
		return protocol.DecisionWouldApproval
	case protocol.DecisionReject:
		return protocol.DecisionWouldReject
	case protocol.DecisionAllow:
		return protocol.DecisionWouldAllow
	default:
		return candidate
	}
}

func (k *Kernel) latestDecision(ctx context.Context, tx pgx.Tx, tenantID, actionID string) (protocol.Decision, error) {
	return k.decisionReceipt(ctx, tx, tenantID, actionID, "")
}
func (k *Kernel) decisionReceipt(ctx context.Context, tx pgx.Tx, tenantID, actionID, receipt string) (protocol.Decision, error) {
	var out protocol.Decision
	var evidence, evaluations []byte
	err := tx.QueryRow(ctx, `SELECT id::text,action_id,decision,reason_code,enforced,confidence,retry_after_ms,evidence,evaluations,
		kernel_version,resolver_version,policy_version,created_at FROM decisions
		WHERE tenant_id=$1 AND action_id=$2 AND ($3='' OR id::text=$3) ORDER BY created_at DESC LIMIT 1`, tenantID, actionID, receipt).Scan(&out.ReceiptID,
		&out.ActionID, &out.Decision, &out.ReasonCode, &out.Enforced, &out.Confidence, &out.RetryAfterMS, &evidence, &evaluations,
		&out.KernelVersion, &out.ResolverVersion, &out.PolicyVersion, &out.CreatedAt)
	if err != nil {
		return protocol.Decision{}, err
	}
	out.SchemaVersion = protocol.SchemaVersion
	_ = json.Unmarshal(evidence, &out.Evidence)
	_ = json.Unmarshal(evaluations, &out.Evaluations)
	if k.cipher != nil {
		var encrypted []byte
		e := tx.QueryRow(ctx, `SELECT response_ciphertext FROM admission_payloads WHERE tenant_id=$1 AND receipt_id=$2`, tenantID, out.ReceiptID).Scan(&encrypted)
		if e == nil && len(encrypted) > 0 {
			data, e := k.cipher.Decrypt(encrypted)
			if e != nil {
				return out, e
			}
			if e = json.Unmarshal(data, &out); e != nil {
				return out, e
			}
		} else if e != nil && !errors.Is(e, pgx.ErrNoRows) {
			return out, e
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return protocol.Decision{}, err
	}
	return out, nil
}

func buildEvaluations(in protocol.ActionCheckInput, d draft, grants []protocol.LeaseGrant) []protocol.DecisionEvaluation {
	stages := []protocol.DecisionEvaluation{
		{Stage: "idempotency", Outcome: "PASS"},
		{Stage: "exact_effect", Outcome: "PASS"},
		{Stage: "resource_versions", Outcome: "PASS"},
		{Stage: "conflicts", Outcome: "PASS"},
		{Stage: "policy", Outcome: "PASS"},
		{Stage: "leases", Outcome: "NOT_REQUIRED"},
	}
	stop := func(index int, outcome string) {
		stages[index].Outcome = outcome
		stages[index].ReasonCode = d.reason
		for next := index + 1; next < len(stages); next++ {
			stages[next].Outcome = "SKIPPED"
		}
	}
	switch d.reason {
	case protocol.ReasonIdempotencyPayloadMismatch:
		stop(0, "REJECT")
	case protocol.ReasonEffectAlreadyCommitted, protocol.ReasonExactDuplicateActive:
		stop(1, string(d.decision))
	case protocol.ReasonStaleObservation, protocol.ReasonVersionUnavailable, protocol.ReasonVersionSourceChanged:
		stop(2, string(d.decision))
	case protocol.ReasonConflictingActiveMutation, protocol.ReasonWriteWriteConflict, protocol.ReasonDeleteWriteConflict, protocol.ReasonDomainOperationConflict:
		stop(3, string(d.decision))
	case protocol.ReasonApprovalRequired, protocol.ReasonApprovalPending, protocol.ReasonApprovalDenied, protocol.ReasonApprovalExpired, protocol.ReasonPolicyRejected:
		stop(4, string(d.decision))
	case protocol.ReasonLeaseBusy:
		stop(5, string(d.decision))
	case protocol.ReasonActorPaused, protocol.ReasonConcurrencyLimit, protocol.ReasonRateLimited, protocol.ReasonBudgetPacing:
		stop(4, string(d.decision))
	default:
		if len(grants) > 0 {
			stages[5].Outcome = "ACQUIRED"
			stages[5].Details = map[string]any{"resource_count": len(grants)}
		}
	}
	for index := range stages {
		stages[index].SubjectID = in.ActionID
	}
	return stages
}

func (k *Kernel) Start(ctx context.Context, tenantID, actionID, token string) error {
	return k.withLease(ctx, tenantID, actionID, token, func(tx pgx.Tx, hasLease bool) error {
		if !hasLease {
			var enforced bool
			if err := tx.QueryRow(ctx, `SELECT enforced FROM decisions WHERE tenant_id=$1 AND action_id=$2 ORDER BY created_at DESC LIMIT 1`, tenantID, actionID).Scan(&enforced); err != nil {
				return err
			}
			if enforced {
				var writeCount int
				if err := tx.QueryRow(ctx, `SELECT count(*) FROM action_resources WHERE tenant_id=$1 AND action_id=$2 AND mode IN ('WRITE','CREATE','DELETE')`, tenantID, actionID).Scan(&writeCount); err != nil {
					return err
				}
				if writeCount > 0 {
					return ErrInvalidLease
				}
			}
		}
		tag, err := tx.Exec(ctx, `UPDATE actions SET status='ACTIVE',started_at=COALESCE(started_at,clock_timestamp())
			WHERE tenant_id=$1 AND id=$2 AND status IN ('PROPOSED','RESERVED','ACTIVE')`, tenantID, actionID)
		if err == nil && tag.RowsAffected() == 0 {
			return errors.New("ACTION_NOT_STARTABLE")
		}
		return err
	})
}

func (k *Kernel) Heartbeat(ctx context.Context, tenantID, actionID, token string) (time.Time, error) {
	expires := k.now().Add(k.leaseTTL)
	err := k.withLease(ctx, tenantID, actionID, token, func(tx pgx.Tx, hasLease bool) error {
		if !hasLease {
			return ErrInvalidLease
		}
		_, err := tx.Exec(ctx, `UPDATE leases SET heartbeat_at=clock_timestamp(),expires_at=$3
			WHERE tenant_id=$1 AND action_id=$2 AND expires_at>clock_timestamp()`, tenantID, actionID, expires)
		return err
	})
	return expires, err
}

func (k *Kernel) withLease(ctx context.Context, tenantID, actionID, token string, fn func(pgx.Tx, bool) error) error {
	tx, err := k.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	hash := sha256.Sum256([]byte(token))
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM leases WHERE tenant_id=$1 AND action_id=$2 AND token_hash=$3 AND expires_at>clock_timestamp()`, tenantID, actionID, hash[:]).Scan(&count); err != nil {
		return err
	}
	if err := fn(tx, count > 0); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (k *Kernel) RecordResult(ctx context.Context, tenantID, actionID string, in protocol.ActionResultInput) error {
	tx, err := k.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = k.RecordResultTx(ctx, tx, tenantID, actionID, in); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// RecordResultTx lets native step completion share the action result transaction.
func (k *Kernel) RecordResultTx(ctx context.Context, tx pgx.Tx, tenantID, actionID string, in protocol.ActionResultInput) error {
	var currentStatus, effectKey, operation string
	var storedHash []byte
	if err := tx.QueryRow(ctx, `SELECT status,COALESCE(effect_key,''),operation,COALESCE(lease_token_hash,decode('','hex'))
		FROM actions WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, tenantID, actionID).Scan(&currentStatus, &effectKey, &operation, &storedHash); err != nil {
		return err
	}
	terminal := protocol.ActionFailed
	effectStatus := "FAILED"
	switch strings.ToUpper(in.Status) {
	case "SUCCEEDED", "SUCCESS", "COMMITTED":
		terminal, effectStatus = protocol.ActionSucceeded, "COMMITTED"
	case "UNKNOWN", "UNKNOWN_EFFECT":
		terminal, effectStatus = protocol.ActionUnknown, "UNKNOWN"
	}
	versions, _ := json.Marshal(in.ResourceVersions)
	identityInput := in
	identityInput.LeaseToken = nil
	identityInput.Status = string(terminal)
	identityJSON, _ := json.Marshal(identityInput)
	identityHash := sha256.Sum256(identityJSON)
	identity := hex.EncodeToString(identityHash[:])
	var resultMatches bool
	err := tx.QueryRow(ctx, `SELECT status=$3 AND external_result_id IS NOT DISTINCT FROM $4
		AND resource_versions=$5::jsonb AND (result_identity_hash IS NULL OR result_identity_hash=$6) FROM action_results WHERE tenant_id=$1 AND action_id=$2`, tenantID, actionID, terminal, in.ExternalResultID, versions, identity).Scan(&resultMatches)
	if err == nil {
		if !resultMatches {
			return ErrResultPayloadMismatch
		}
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if len(storedHash) > 0 {
		if in.LeaseToken == nil {
			return ErrInvalidLease
		}
		hash := sha256.Sum256([]byte(*in.LeaseToken))
		if !equalBytes(hash[:], storedHash) {
			return ErrInvalidLease
		}
		if currentStatus != string(protocol.ActionActive) && currentStatus != string(protocol.ActionUnknown) {
			return ErrInvalidActionTransition
		}
	} else {
		var enforced bool
		var decision protocol.DecisionType
		if err := tx.QueryRow(ctx, `SELECT enforced,decision FROM decisions WHERE tenant_id=$1 AND action_id=$2 ORDER BY created_at DESC LIMIT 1`, tenantID, actionID).Scan(&enforced, &decision); err != nil {
			return err
		}
		if enforced && (decision != protocol.DecisionAllow && decision != protocol.DecisionWarn) {
			return ErrInvalidActionTransition
		}
		if enforced && currentStatus != string(protocol.ActionActive) {
			return ErrInvalidActionTransition
		}
	}
	created, _ := json.Marshal(in.CreatedResources)
	_, err = tx.Exec(ctx, `INSERT INTO action_results
		(tenant_id,action_id,status,external_result_id,retryable,resource_versions,created_resources,compensation,result_metadata)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT (tenant_id,action_id) DO NOTHING`, tenantID, actionID, terminal,
		in.ExternalResultID, in.Retryable, versions, created, jsonOrDefault(in.Compensation, []byte("{}")), jsonOrDefault(in.ResultMetadata, []byte("{}")))
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE action_results SET result_identity_hash=$3 WHERE tenant_id=$1 AND action_id=$2`, tenantID, actionID, identity); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE actions SET status=$3,finished_at=clock_timestamp() WHERE tenant_id=$1 AND id=$2`, tenantID, actionID, terminal); err != nil {
		return err
	}
	if effectKey != "" {
		_, err = tx.Exec(ctx, `INSERT INTO effects (tenant_id,id,action_id,operation,effect_key,status,external_id,occurred_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,clock_timestamp()) ON CONFLICT (tenant_id,effect_key) DO UPDATE
			SET status=EXCLUDED.status,external_id=EXCLUDED.external_id,occurred_at=EXCLUDED.occurred_at,updated_at=clock_timestamp()
			WHERE effects.action_id=EXCLUDED.action_id`, tenantID, "eff_"+uuid.NewString(), actionID, operation, effectKey, effectStatus, in.ExternalResultID)
		if err != nil {
			return err
		}
	}
	for resourceID, version := range in.ResourceVersions {
		var source string
		if err := tx.QueryRow(ctx, `SELECT COALESCE(version_source,'result') FROM resources WHERE tenant_id=$1 AND id=$2`, tenantID, resourceID).Scan(&source); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE resources SET current_version=$3,version_source=$4,version_confidence=1,
			version_observed_at=clock_timestamp(),updated_at=clock_timestamp() WHERE tenant_id=$1 AND id=$2`, tenantID, resourceID, version, source)
		if err != nil {
			return err
		}
		_, _ = tx.Exec(ctx, `INSERT INTO resource_versions
			(tenant_id,resource_id,version,version_source,observed_at,source,confidence,authoritative,action_id,actor_id,summary)
			SELECT $1,$2,$3,$4,clock_timestamp(),'action_result',1,true,a.id,a.actor_id,a.metadata->>'summary' FROM actions a WHERE a.tenant_id=$1 AND a.id=$5 ON CONFLICT DO NOTHING`, tenantID, resourceID, version, source, actionID)
	}
	if err := k.recordDurableResult(ctx, tx, tenantID, actionID, in, effectStatus); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM leases WHERE tenant_id=$1 AND action_id=$2`, tenantID, actionID); err != nil {
		return err
	}
	idemStatus := string(terminal)
	if terminal == protocol.ActionSucceeded {
		idemStatus = "COMPLETED"
	}
	_, _ = tx.Exec(ctx, `UPDATE idempotency_records SET status=$3,updated_at=clock_timestamp() WHERE tenant_id=$1 AND action_id=$2`, tenantID, actionID, idemStatus)
	payload, _ := json.Marshal(map[string]any{"action_id": actionID, "status": terminal})
	_, _ = tx.Exec(ctx, `INSERT INTO events (tenant_id,id,type,entity_type,entity_id,source,occurred_at,payload)
		VALUES ($1,$2,$3,'action',$4,'api',clock_timestamp(),$5)`, tenantID, "evt_"+uuid.NewString(), "ACTION_"+string(terminal), actionID, payload)
	_, _ = tx.Exec(ctx, `INSERT INTO outbox (tenant_id,topic,key,payload) VALUES ($1,'action.result',$2,$3)`, tenantID, actionID, payload)
	return nil
}

func (k *Kernel) ResolveApproval(ctx context.Context, tenantID, approvalID, actor, resolution string) error {
	return k.ResolveApprovalWithNote(ctx, tenantID, approvalID, actor, resolution, "")
}
func (k *Kernel) ResolveApprovalWithNote(ctx context.Context, tenantID, approvalID, actor, resolution, note string) error {
	resolution = strings.ToUpper(resolution)
	if resolution != "APPROVED" && resolution != "DENIED" {
		return errors.New("invalid approval resolution")
	}
	tag, err := k.pool.Exec(ctx, `UPDATE approvals SET status=$4,resolved_at=clock_timestamp(),resolved_by=$3,metadata=metadata||jsonb_build_object('note',$5::text)
		WHERE tenant_id=$1 AND id::text=$2 AND status='PENDING' AND (expires_at IS NULL OR expires_at>clock_timestamp())`, tenantID, approvalID, actor, resolution, note)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// CancelReservation releases a reservation only before execution has started.
// Active or terminal actions cannot be force-released through this operator API.
func (k *Kernel) CancelReservation(ctx context.Context, tenantID, actionID string) error {
	tx, err := k.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `UPDATE actions SET status='EXPIRED',finished_at=clock_timestamp()
		WHERE tenant_id=$1 AND id=$2 AND status='RESERVED' AND started_at IS NULL`, tenantID, actionID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrInvalidActionTransition
	}
	if _, err := tx.Exec(ctx, `DELETE FROM leases WHERE tenant_id=$1 AND action_id=$2`, tenantID, actionID); err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]any{"action_id": actionID, "status": protocol.ActionExpired, "reason": "OPERATOR_CANCELLED_UNSTARTED_RESERVATION"})
	if _, err := tx.Exec(ctx, `INSERT INTO events(tenant_id,id,type,entity_type,entity_id,source,occurred_at,payload)
		VALUES($1,$2,'ACTION_EXPIRED','action',$3,'operator',clock_timestamp(),$4)`, tenantID, "evt_"+uuid.NewString(), actionID, payload); err != nil {
		return err
	}
	_, _ = tx.Exec(ctx, `INSERT INTO outbox(tenant_id,topic,key,payload)VALUES($1,'action.reservation.cancelled',$2,$3)`, tenantID, actionID, payload)
	return tx.Commit(ctx)
}

func (k *Kernel) ExpireLeases(ctx context.Context) (int64, error) {
	tx, err := k.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT tenant_id::text,action_id FROM leases WHERE expires_at<=clock_timestamp() FOR UPDATE SKIP LOCKED`)
	if err != nil {
		return 0, err
	}
	type expiredAction struct{ tenantID, actionID string }
	seen := make(map[expiredAction]struct{})
	var actions []expiredAction
	for rows.Next() {
		var action expiredAction
		if err := rows.Scan(&action.tenantID, &action.actionID); err != nil {
			rows.Close()
			return 0, err
		}
		if _, ok := seen[action]; !ok {
			seen[action] = struct{}{}
			actions = append(actions, action)
		}
	}
	rows.Close()
	var count int64
	for _, action := range actions {
		tag, err := tx.Exec(ctx, `UPDATE actions SET status=CASE WHEN started_at IS NULL THEN 'EXPIRED' ELSE 'UNKNOWN' END,
			finished_at=clock_timestamp() WHERE tenant_id=$1 AND id=$2 AND status IN ('RESERVED','ACTIVE')`, action.tenantID, action.actionID)
		if err != nil {
			return 0, err
		}
		count += tag.RowsAffected()
	}
	if _, err = tx.Exec(ctx, `INSERT INTO effect_holds(tenant_id,action_id,reason) SELECT tenant_id,id,'UNKNOWN_EFFECT' FROM actions WHERE status='UNKNOWN' ON CONFLICT DO NOTHING`); err != nil {
		return 0, err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM leases WHERE expires_at<=clock_timestamp()`); err != nil {
		return 0, err
	}
	return count, tx.Commit(ctx)
}

func versionEvidence(resourceID, observed, current, observedSource, currentSource string) []protocol.Evidence {
	return []protocol.Evidence{{Type: "RESOURCE_VERSION", Data: map[string]any{"resource_id": resourceID, "observed_version": observed,
		"current_version": current, "observed_source": observedSource, "current_source": currentSource}}}
}

func hasResourceType(resources []protocol.ResourceIntent, typ string) bool {
	for _, r := range resources {
		if r.Type == typ {
			return true
		}
	}
	return false
}

func riskRank(r protocol.RiskLevel) int {
	switch r {
	case protocol.RiskCritical:
		return 4
	case protocol.RiskHigh:
		return 3
	case protocol.RiskMedium:
		return 2
	default:
		return 1
	}
}

func numeric(value any) float64 {
	switch n := value.(type) {
	case float64:
		return n
	case json.Number:
		f, _ := n.Float64()
		return f
	case int:
		return float64(n)
	case int64:
		return float64(n)
	default:
		return 0
	}
}

func jsonOrDefault(raw json.RawMessage, fallback []byte) []byte {
	if len(raw) == 0 {
		return fallback
	}
	return raw
}

func uniqueSorted(values []string) []string {
	sort.Strings(values)
	out := values[:0]
	for _, value := range values {
		if len(out) == 0 || out[len(out)-1] != value {
			out = append(out, value)
		}
	}
	return out
}

func randomToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return "lease_" + base64.RawURLEncoding.EncodeToString(raw), nil
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := range a {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}
