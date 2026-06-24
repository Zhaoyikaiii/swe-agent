package policy

import (
	"context"
	"testing"

	agentpkg "github.com/local/swe-agent/internal/agent"
	"github.com/local/swe-agent/internal/core"
)

type approvalRecorder struct {
	decision policyDecision
	requests []ApprovalRequest
}

type policyDecision struct {
	allowed      bool
	rememberRisk bool
}

func (r *approvalRecorder) RequestApproval(ctx context.Context, req ApprovalRequest) (ApprovalDecision, error) {
	r.requests = append(r.requests, req)
	return ApprovalDecision{
		Allowed:      r.decision.allowed,
		RememberRisk: r.decision.rememberRisk,
	}, nil
}

func TestInteractivePolicyRequestsApprovalAndRemembersRisk(t *testing.T) {
	recorder := &approvalRecorder{decision: policyDecision{allowed: true, rememberRisk: true}}
	p := NewInteractive(agentpkg.PolicyConfig{}, recorder)
	call := core.ToolCall{Name: "shell", Args: map[string]any{"command": "go test ./..."}}
	spec := core.ToolSpec{Name: "shell", Description: "Run a shell command."}

	first, err := p.AllowTool(context.Background(), call, spec, core.RiskExec)
	if err != nil {
		t.Fatalf("AllowTool returned error: %v", err)
	}
	if !first.Allowed {
		t.Fatalf("expected first call to be approved, got %#v", first)
	}
	if len(recorder.requests) != 1 {
		t.Fatalf("expected one approval request, got %d", len(recorder.requests))
	}

	second, err := p.AllowTool(context.Background(), call, spec, core.RiskExec)
	if err != nil {
		t.Fatalf("AllowTool returned error: %v", err)
	}
	if !second.Allowed {
		t.Fatalf("expected remembered risk to be approved, got %#v", second)
	}
	if len(recorder.requests) != 1 {
		t.Fatalf("expected remembered risk to avoid another request, got %d requests", len(recorder.requests))
	}
}

func TestInteractivePolicyDoesNotPromptForDangerousCommand(t *testing.T) {
	recorder := &approvalRecorder{decision: policyDecision{allowed: true}}
	p := NewInteractive(agentpkg.PolicyConfig{}, recorder)
	call := core.ToolCall{Name: "shell", Args: map[string]any{"command": "rm -rf /"}}
	spec := core.ToolSpec{Name: "shell", Description: "Run a shell command."}

	decision, err := p.AllowTool(context.Background(), call, spec, core.RiskExec)
	if err != nil {
		t.Fatalf("AllowTool returned error: %v", err)
	}
	if decision.Allowed {
		t.Fatalf("expected dangerous command to be denied, got %#v", decision)
	}
	if len(recorder.requests) != 0 {
		t.Fatalf("expected dangerous command not to request approval, got %d requests", len(recorder.requests))
	}
}
