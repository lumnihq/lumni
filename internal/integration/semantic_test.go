//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lumnihq/lumni/internal/semantic"
	"github.com/lumnihq/lumni/pkg/protocol"
)

type fakeSemanticEvaluator struct {
	mu       sync.Mutex
	requests []semantic.Request
}

func (*fakeSemanticEvaluator) Provider() string       { return "fixture" }
func (*fakeSemanticEvaluator) RequestedModel() string { return "typesafe/jev-fixture" }
func (*fakeSemanticEvaluator) PrivacyEligible(context.Context) (bool, error) {
	return true, nil
}
func (f *fakeSemanticEvaluator) Evaluate(_ context.Context, request semantic.Request) (semantic.Response, error) {
	f.mu.Lock()
	f.requests = append(f.requests, request)
	f.mu.Unlock()
	answers := make(map[string]semantic.Answer, len(request.Questions))
	for name, question := range request.Questions {
		switch {
		case question.Type == semantic.QuestionChoice:
			answers[name] = semantic.Answer{Choice: "HIGH", Probabilities: map[string]float64{"LOW": 0.02, "MEDIUM": 0.08, "HIGH": 0.85, "CRITICAL": 0.05}}
		case strings.HasPrefix(name, "same_goal_"):
			answers[name] = noulAnswer(0.95)
		case strings.HasPrefix(name, "supersedes_"):
			answers[name] = noulAnswer(0.10)
		case name == "repetition_without_progress":
			answers[name] = noulAnswer(0.90)
		case name == "outcome_supported":
			answers[name] = noulAnswer(0.10)
		case name == "goal_action_alignment" && request.Questions["apparent_risk"].Type == semantic.QuestionChoice:
			answers[name] = noulAnswer(0.90)
		default:
			answers[name] = noulAnswer(0.10)
		}
	}
	return semantic.Response{Model: "typesafe/jev-1.13", Answers: answers, Usage: semantic.Usage{InputTokens: 100}, CostMicros: 4}, nil
}

func noulAnswer(value float64) semantic.Answer { return semantic.Answer{Noul: &value} }

func TestSemanticWorkerIsAdvisoryAndMinimizesState(t *testing.T) {
	f := setup(t, protocol.ModeShadow)
	ctx := t.Context()
	if _, err := f.pool.Exec(ctx, `UPDATE tenants SET settings=jsonb_set(settings,'{semantic_enabled}','true'::jsonb,true) WHERE id=$1`, f.tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE work SET goal='Refund customer@example.com using Bearer abc.def-secret-token',risk='LOW' WHERE tenant_id=$1 AND id=$2`, f.tenant, f.work); err != nil {
		t.Fatal(err)
	}
	secondWork := "semantic-comparison"
	if _, err := f.pool.Exec(ctx, `INSERT INTO work(tenant_id,id,actor_id,goal,status,risk)VALUES($1,$2,$3,'Refund the duplicate charge','RUNNING','MEDIUM')`, f.tenant, secondWork, f.actor); err != nil {
		t.Fatal(err)
	}
	actionID := "semantic-action"
	if _, err := f.pool.Exec(ctx, `INSERT INTO actions(tenant_id,id,work_id,actor_id,operation,tool,status,risk,arguments_hash,arguments)VALUES($1,$2,$3,$4,'payment.refund','refund_payment','SUCCEEDED','LOW','hash','{"secret":"argument-secret"}')`, f.tenant, actionID, f.work, f.actor); err != nil {
		t.Fatal(err)
	}
	resourceID := "semantic-resource"
	if _, err := f.pool.Exec(ctx, `INSERT INTO resources(tenant_id,id,type,external_id)VALUES($1,$2,'payment','resource-secret')`, f.tenant, resourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO action_resources(tenant_id,action_id,resource_id,mode)VALUES($1,$2,$3,'WRITE')`, f.tenant, actionID, resourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO action_results(tenant_id,action_id,status,retryable)VALUES($1,$2,'SUCCEEDED',false)`, f.tenant, actionID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO steps(tenant_id,id,work_id,step_key,type,name,status,input_hash,attempt,error)VALUES($1,'semantic-step',$2,'refund','TOOL','refund_payment','FAILED','hash',2,'database password argument-secret timed out')`, f.tenant, f.work); err != nil {
		t.Fatal(err)
	}

	evaluator := &fakeSemanticEvaluator{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	service := semantic.NewService(f.pool, evaluator, 20, logger)
	runContext, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	stopped := false
	go func() {
		service.Run(runContext, 10*time.Millisecond)
		close(done)
	}()
	t.Cleanup(func() {
		if !stopped {
			cancel()
			<-done
		}
	})

	deadline := time.Now().Add(3 * time.Second)
	for {
		var pending int
		if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM semantic_jobs WHERE tenant_id=$1 AND status NOT IN ('COMPLETED','FAILED')`, f.tenant).Scan(&pending); err != nil {
			t.Fatal(err)
		}
		var evaluations int
		if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM semantic_evaluations WHERE tenant_id=$1`, f.tenant).Scan(&evaluations); err != nil {
			t.Fatal(err)
		}
		if pending == 0 && evaluations >= 4 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("semantic jobs did not complete: pending=%d evaluations=%d", pending, evaluations)
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	<-done
	stopped = true

	evaluator.mu.Lock()
	requests := append([]semantic.Request(nil), evaluator.requests...)
	evaluator.mu.Unlock()
	if len(requests) < 4 {
		t.Fatalf("requests=%d, want at least 4", len(requests))
	}
	for _, request := range requests {
		raw, err := json.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		payload := string(raw)
		for _, secret := range []string{"customer@example.com", "abc.def-secret-token", "argument-secret", "resource-secret", "database password"} {
			if strings.Contains(payload, secret) {
				t.Fatalf("outbound state leaked %q: %s", secret, payload)
			}
		}
	}

	var risk, status string
	if err := f.pool.QueryRow(ctx, `SELECT risk,status FROM actions WHERE tenant_id=$1 AND id=$2`, f.tenant, actionID).Scan(&risk, &status); err != nil {
		t.Fatal(err)
	}
	if risk != "LOW" || status != "SUCCEEDED" {
		t.Fatalf("semantic worker changed authoritative action state: risk=%s status=%s", risk, status)
	}
	var semanticEdges, findings int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM work_edges WHERE tenant_id=$1 AND evidence_type='SEMANTIC_INFERENCE'`, f.tenant).Scan(&semanticEdges); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM incidents WHERE tenant_id=$1 AND evidence->>'evidence_type'='SEMANTIC_INFERENCE'`, f.tenant).Scan(&findings); err != nil {
		t.Fatal(err)
	}
	if semanticEdges == 0 || findings < 3 {
		t.Fatalf("semantic recommendations missing: edges=%d findings=%d", semanticEdges, findings)
	}
}
