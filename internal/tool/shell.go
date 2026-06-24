package tool

import (
	"context"
	"errors"

	"github.com/local/swe-agent/internal/core"
)

type Shell struct{}

func (Shell) Spec() core.ToolSpec {
	return core.ToolSpec{
		Name:        "shell",
		Description: "Run a shell command in the workspace.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{"type": "string"},
			},
			"required": []string{"command"},
		},
	}
}

func (Shell) Risk() core.RiskLevel { return core.RiskExec }

func (Shell) Execute(ctx context.Context, input core.ToolInput) (core.ToolResult, error) {
	cmd := stringArg(input.Call.Args, "command")
	if cmd == "" {
		return core.ToolResult{}, errors.New("shell command is required")
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
