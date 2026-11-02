package durable

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/lumnihq/lumni/pkg/protocol"
)

// StartStepOnce consumes the external-call boundary once, including a lost response.
// It is used by backend-owned compiled execution; existing SDK endpoints are unchanged.
func (s *Service) StartStepOnce(ctx context.Context, tenant, work, key string, in protocol.StepResult) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	if err = authorize(ctx, tx, tenant, work, in.RuntimeAuth); err != nil {
		return false, err
	}
	st, _, admission, err := loadStep(ctx, tx, tenant, work, key)
	if err != nil {
		return false, err
	}
	if st.Attempt != in.Attempt || st.Status != "READY" {
		return false, nil
	}
	if _, err = tx.Exec(ctx, `UPDATE steps SET status='RUNNING',updated_at=now() WHERE tenant_id=$1 AND id=$2`, tenant, st.ID); err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO step_attempts(tenant_id,step_id,attempt,claim_fence,status)VALUES($1,$2,$3,$4,'RUNNING')ON CONFLICT DO NOTHING`, tenant, st.ID, st.Attempt, in.Fence); err != nil {
		return false, err
	}
	if err = journal(ctx, tx, tenant, work, st.ID, "STEP_STARTED", fmt.Sprintf("start:%s:%d", st.ID, st.Attempt), map[string]any{"step_id": st.ID, "attempt": st.Attempt}); err != nil {
		return false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return false, err
	}
	if st.ActionID == nil {
		return true, nil
	}
	data, err := s.cipher.Decrypt(admission)
	if err != nil {
		return false, err
	}
	var d protocol.Decision
	if err = json.Unmarshal(data, &d); err != nil {
		return false, err
	}
	token := ""
	if d.LeaseToken != nil {
		token = *d.LeaseToken
	}
	return s.kernel.StartOnce(ctx, tenant, *st.ActionID, token)
}
