package tool

import (
	"context"

	"github.com/local/swe-agent/internal/core"
)

type RunTests struct{}

func (RunTests) Spec() core.ToolSpec {
	return core.ToolSpec{
		Name:        "run_tests",
		Description: "Run a test command in the workspace.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{"type": "string"},
			},
		},
	}
}

func (RunTests) Risk() core.RiskLevel { return core.RiskExec }

func (RunTests) Execute(ctx context.Context, input core.ToolInput) (core.ToolResult, error) {
	cmd := stringArg(input.Call.Args, "command")
	if cmd == "" {
		cmd = "go test ./..."
	}
	res, err := input.Runtime.Execute(ctx, core.ExecRequest{
		Command: cmd,
		Cwd:     input.WorkspaceRoot,
		Timeout: input.Timeout,
	})
	if err != nil {
		return core.ToolResult{}, err
	}
	out := res.Stdout
	if res.Stderr != "" {
		out += res.Stderr
	}
	return core.ToolResult{Output: out, Code: res.Code, TimedOut: res.TimedOut}, nil
}
