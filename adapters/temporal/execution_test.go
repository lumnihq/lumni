package temporal

import (
	"encoding/json"
	"github.com/lumnihq/lumni/pkg/protocol"
	lumni "github.com/lumnihq/lumni/sdk/go"
	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
	"testing"
)

func adapterWorkflow(ctx workflow.Context) (json.RawMessage, error) {
	return Execute(ctx, "refund", protocol.ActionCheckInput{WorkID: "work", ActorID: "agent", Operation: "refund"})
}
func TestRealWorkflowSuspendsThenVerifies(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	a := &Activities{}
	env.RegisterActivity(a)
	env.OnActivity(a.Admit, mock.Anything, mock.Anything).Return(protocol.Decision{Decision: protocol.DecisionWait, Enforced: true}, nil).Once()
	env.OnActivity(a.Admit, mock.Anything, mock.Anything).Return(protocol.Decision{Decision: protocol.DecisionAllow, Enforced: true}, nil).Once()
	env.OnActivity(a.Execute, mock.Anything, mock.Anything).Return(lumni.ExternalResult{Status: "PENDING"}, nil).Once()
	env.OnActivity(a.Outcome, mock.Anything, mock.Anything).Return(lumni.ExternalResult{Status: "COMPLETED", Output: json.RawMessage(`{"id":"re_1"}`)}, nil).Once()
	env.ExecuteWorkflow(adapterWorkflow)
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	var result json.RawMessage
	if err := env.GetWorkflowResult(&result); err != nil || string(result) != `{"id":"re_1"}` {
		t.Fatalf("%s %v", result, err)
	}
	env.AssertExpectations(t)
}
func TestUnregisteredActivityFailureTerminatesWorkflow(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	a := &Activities{}
	env.RegisterActivity(a)
	env.OnActivity(a.Admit, mock.Anything, mock.Anything).Return(protocol.Decision{Decision: protocol.DecisionAllow, Enforced: true}, nil).Once()
	env.OnActivity(a.Execute, mock.Anything, mock.Anything).Return(lumni.ExternalResult{}, temporal.NewNonRetryableApplicationError("missing tool", "UNREGISTERED_TOOL", nil)).Once()
	env.ExecuteWorkflow(adapterWorkflow)
	if env.GetWorkflowError() == nil {
		t.Fatal("missing handler silently polled forever")
	}
	env.AssertExpectations(t)
}
