package tool

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/local/swe-agent/internal/core"
)

type ApplyPatch struct{}

func (ApplyPatch) Spec() core.ToolSpec {
	return core.ToolSpec{
		Name:        "apply_patch",
		Description: "Apply a unified diff patch to the workspace using git apply.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"patch": map[string]any{"type": "string"},
			},
			"required": []string{"patch"},
		},
	}
}

func (ApplyPatch) Risk() core.RiskLevel { return core.RiskWrite }

func (ApplyPatch) Execute(ctx context.Context, input core.ToolInput) (core.ToolResult, error) {
	patch := stringArg(input.Call.Args, "patch")
	if patch == "" {
		return core.ToolResult{}, errors.New("patch is required")
	}
	tmp, err := os.CreateTemp("", "swe-agent-*.patch")
	if err != nil {
		return core.ToolResult{}, err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.WriteString(patch); err != nil {
		tmp.Close()
		return core.ToolResult{}, err
	}
	if err := tmp.Close(); err != nil {
		return core.ToolResult{}, err
	}
	absTmp, err := filepath.Abs(tmpPath)
	if err != nil {
		return core.ToolResult{}, err
	}
	res, err := input.Runtime.Execute(ctx, core.ExecRequest{
		Command: "git apply --whitespace=nowarn " + shellQuote(absTmp),
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

func shellQuote(s string) string {
	out := "'"
	for _, r := range s {
		if r == '\'' {
			out += "'\\''"
		} else {
			out += string(r)
		}
	}
	out += "'"
	return out
}
