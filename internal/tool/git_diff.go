package tool

import (
	"context"

	"github.com/local/swe-agent/internal/core"
)

type GitDiff struct{}

func (GitDiff) Spec() core.ToolSpec {
	return core.ToolSpec{Name: "git_diff", Description: "Show current git diff for the workspace."}
}

func (GitDiff) Risk() core.RiskLevel { return core.RiskRead }

func (GitDiff) Execute(ctx context.Context, input core.ToolInput) (core.ToolResult, error) {
	res, err := input.Runtime.Execute(ctx, core.ExecRequest{
		Command: "git diff -- .",
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
