package tool

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/local/swe-agent/internal/core"
)

type ListFiles struct{}

func (ListFiles) Spec() core.ToolSpec {
	return core.ToolSpec{
		Name:        "list_files",
		Description: "List files under a workspace directory.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":      map[string]any{"type": "string"},
				"max_files": map[string]any{"type": "integer"},
			},
		},
	}
}

func (ListFiles) Risk() core.RiskLevel { return core.RiskRead }

func (ListFiles) Execute(ctx context.Context, input core.ToolInput) (core.ToolResult, error) {
	relPath := stringArg(input.Call.Args, "path")
	if relPath == "" {
		relPath = "."
	}
	root, err := safePath(input.WorkspaceRoot, relPath)
	if err != nil {
		return core.ToolResult{}, err
	}
	maxFiles := intArg(input.Call.Args, "max_files", 200)
	var b strings.Builder
	count := 0
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && isSkippableDir(d.Name()) {
			return filepath.SkipDir
		}
		if path == root {
			return nil
		}
		if count >= maxFiles {
			return filepath.SkipAll
		}
		rel, _ := filepath.Rel(input.WorkspaceRoot, path)
		if d.IsDir() {
			fmt.Fprintf(&b, "%s/\n", rel)
		} else {
			fmt.Fprintf(&b, "%s\n", rel)
		}
		count++
		return nil
	})
	return core.ToolResult{Output: b.String(), Metadata: map[string]any{"count": count}}, err
}
