package tool

import (
	"context"
	"fmt"
	"strings"

	"github.com/local/swe-agent/internal/core"
)

type ReadFile struct{}

func (ReadFile) Spec() core.ToolSpec {
	return core.ToolSpec{
		Name:        "read_file",
		Description: "Read a UTF-8 text file from the workspace. Optional start_line and end_line are 1-based.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":       map[string]any{"type": "string"},
				"start_line": map[string]any{"type": "integer"},
				"end_line":   map[string]any{"type": "integer"},
			},
			"required": []string{"path"},
		},
	}
}

func (ReadFile) Risk() core.RiskLevel { return core.RiskRead }

func (ReadFile) Execute(ctx context.Context, input core.ToolInput) (core.ToolResult, error) {
	path, err := safePath(input.WorkspaceRoot, stringArg(input.Call.Args, "path"))
	if err != nil {
		return core.ToolResult{}, err
	}
	text, err := readTextFile(path, 200_000)
	if err != nil {
		return core.ToolResult{}, err
	}
	start := intArg(input.Call.Args, "start_line", 1)
	end := intArg(input.Call.Args, "end_line", 0)
	lines := strings.Split(text, "\n")
	if start < 1 {
		start = 1
	}
	if end <= 0 || end > len(lines) {
		end = len(lines)
	}
	if start > end {
		return core.ToolResult{Output: ""}, nil
	}
	var b strings.Builder
	for i := start; i <= end; i++ {
		fmt.Fprintf(&b, "%6d\t%s\n", i, lines[i-1])
	}
	return core.ToolResult{Output: b.String()}, nil
}
