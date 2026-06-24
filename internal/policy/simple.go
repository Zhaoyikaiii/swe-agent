package policy

import (
	"context"
	"fmt"
	"strings"

	"github.com/local/swe-agent/internal/agent"
	"github.com/local/swe-agent/internal/core"
)

type Simple struct {
	Config agent.PolicyConfig
}

func NewSimple(cfg agent.PolicyConfig) Simple {
	return Simple{Config: cfg}
}

func (p Simple) AllowTool(ctx context.Context, call core.ToolCall, spec core.ToolSpec, risk core.RiskLevel) (core.Decision, error) {
	if isObviouslyDangerous(call) {
		return core.Decision{Allowed: false, Reason: "dangerous command or path"}, nil
	}
	switch risk {
	case core.RiskRead:
		return core.Decision{Allowed: p.Config.AutoApproveRead, Reason: "read tools require approval"}, nil
	case core.RiskWrite:
		return core.Decision{Allowed: p.Config.AutoApproveWrite, Reason: "write tools require approval"}, nil
	case core.RiskExec:
		return core.Decision{Allowed: p.Config.AutoApproveExec, Reason: "exec tools require approval"}, nil
	case core.RiskDanger:
		return core.Decision{Allowed: false, Reason: "danger tools are disabled"}, nil
	default:
		return core.Decision{Allowed: false, Reason: fmt.Sprintf("unknown risk level %q", risk)}, nil
	}
}

func (p Simple) FilterObservation(ctx context.Context, result core.ToolResult) core.ToolResult {
	result.Output = redactSecrets(limitOutput(result.Output, 20_000))
	return result
}

func (p Simple) ValidateUserInput(ctx context.Context, input string) error {
	lower := strings.ToLower(input)
	blocked := []string{
		"ignore previous instructions",
		"reveal system prompt",
		"show developer instructions",
	}
	for _, phrase := range blocked {
		if strings.Contains(lower, phrase) {
			return fmt.Errorf("input rejected by prompt-injection guard: %s", phrase)
		}
	}
	return nil
}

func isObviouslyDangerous(call core.ToolCall) bool {
	command := fmt.Sprint(call.Args["command"])
	lower := strings.ToLower(command)
	dangerous := []string{
		"rm -rf /",
		"mkfs",
		":(){",
		"chmod -r 777 /",
		"dd if=",
	}
	for _, phrase := range dangerous {
		if strings.Contains(lower, phrase) {
			return true
		}
	}
	for _, value := range call.Args {
		s := strings.ToLower(fmt.Sprint(value))
		if strings.Contains(s, ".ssh/id_") || strings.Contains(s, "/etc/shadow") {
			return true
		}
	}
	return false
}

func limitOutput(s string, max int) string {
	if len(s) <= max {
		return s
	}
	head := max / 2
	tail := max - head
	return s[:head] + "\n<output truncated>\n" + s[len(s)-tail:]
}

func redactSecrets(s string) string {
	replacements := []string{
		"OPENAI_API_KEY=", "ANTHROPIC_API_KEY=", "GITHUB_TOKEN=", "GH_TOKEN=",
	}
	for _, marker := range replacements {
		if idx := strings.Index(s, marker); idx >= 0 {
			end := strings.IndexByte(s[idx:], '\n')
			if end < 0 {
				s = s[:idx+len(marker)] + "[REDACTED]"
			} else {
				start := idx + len(marker)
				s = s[:start] + "[REDACTED]" + s[idx+end:]
			}
		}
	}
	return s
}
