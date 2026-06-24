package policy

import (
	"context"
	"errors"
	"sync"

	"github.com/local/swe-agent/internal/agent"
	"github.com/local/swe-agent/internal/core"
)

type ApprovalRequest struct {
	ID     int
	Call   core.ToolCall
	Spec   core.ToolSpec
	Risk   core.RiskLevel
	Reason string
}

type ApprovalDecision struct {
	Allowed      bool
	Reason       string
	RememberRisk bool
}

type ApprovalRequester interface {
	RequestApproval(ctx context.Context, req ApprovalRequest) (ApprovalDecision, error)
}

type Interactive struct {
	base      Simple
	requester ApprovalRequester

	mu             sync.Mutex
	nextID         int
	rememberedRisk map[core.RiskLevel]bool
}

func NewInteractive(cfg agent.PolicyConfig, requester ApprovalRequester) *Interactive {
	return &Interactive{
		base:           NewSimple(cfg),
		requester:      requester,
		rememberedRisk: map[core.RiskLevel]bool{},
	}
}

func (p *Interactive) AllowTool(ctx context.Context, call core.ToolCall, spec core.ToolSpec, risk core.RiskLevel) (core.Decision, error) {
	if p.isRemembered(risk) {
		return core.Decision{Allowed: true, Reason: "approved for this run"}, nil
	}

	decision, err := p.base.AllowTool(ctx, call, spec, risk)
	if err != nil {
		return decision, err
	}
	if decision.Allowed || !requiresApproval(decision) {
		return decision, nil
	}
	if p.requester == nil {
		return decision, nil
	}

	req := ApprovalRequest{
		ID:     p.nextRequestID(),
		Call:   call,
		Spec:   spec,
		Risk:   risk,
		Reason: decision.Reason,
	}
	answer, err := p.requester.RequestApproval(ctx, req)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return core.Decision{}, err
		}
		return core.Decision{Allowed: false, Reason: err.Error()}, nil
	}
	reason := answer.Reason
	if reason == "" {
		if answer.Allowed {
			reason = "approved by user"
		} else {
			reason = "denied by user"
		}
	}
	if answer.Allowed && answer.RememberRisk {
		p.rememberRisk(risk)
	}
	return core.Decision{Allowed: answer.Allowed, Reason: reason}, nil
}

func (p *Interactive) FilterObservation(ctx context.Context, result core.ToolResult) core.ToolResult {
	return p.base.FilterObservation(ctx, result)
}

func (p *Interactive) ValidateUserInput(ctx context.Context, input string) error {
	return p.base.ValidateUserInput(ctx, input)
}

func (p *Interactive) nextRequestID() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.nextID++
	return p.nextID
}

func (p *Interactive) isRemembered(risk core.RiskLevel) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.rememberedRisk[risk]
}

func (p *Interactive) rememberRisk(risk core.RiskLevel) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.rememberedRisk[risk] = true
}

func requiresApproval(decision core.Decision) bool {
	return decision.Reason == "read tools require approval" ||
		decision.Reason == "write tools require approval" ||
		decision.Reason == "exec tools require approval"
}
