package tool

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/local/swe-agent/internal/core"
)

type Grep struct{}

func (Grep) Spec() core.ToolSpec {
	return core.ToolSpec{
		Name:        "grep",
		Description: "Search text files in the workspace for a literal query.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query":       map[string]any{"type": "string"},
				"path":        map[string]any{"type": "string"},
				"max_matches": map[string]any{"type": "integer"},
			},
			"required": []string{"query"},
		},
	}
}

func (Grep) Risk() core.RiskLevel { return core.RiskRead }

func (Grep) Execute(ctx context.Context, input core.ToolInput) (core.ToolResult, error) {
	query := stringArg(input.Call.Args, "query")
	if query == "" {
		return core.ToolResult{}, fmt.Errorf("query is required")
	}
	relPath := stringArg(input.Call.Args, "path")
	if relPath == "" {
		relPath = "."
	}
	root, err := safePath(input.WorkspaceRoot, relPath)
	if err != nil {
		return core.ToolResult{}, err
	}
	maxMatches := intArg(input.Call.Args, "max_matches", 100)
	var b strings.Builder
	matches := 0
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if isSkippableDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if matches >= maxMatches {
			return filepath.SkipAll
		}
		file, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer file.Close()
		scanner := bufio.NewScanner(file)
		lineNo := 0
		for scanner.Scan() {
			lineNo++
			line := scanner.Text()
			if strings.Contains(line, query) {
				rel, _ := filepath.Rel(input.WorkspaceRoot, path)
				fmt.Fprintf(&b, "%s:%d:%s\n", rel, lineNo, line)
				matches++
				if matches >= maxMatches {
					break
				}
			}
		}
		return nil
	})
	return core.ToolResult{Output: b.String(), Metadata: map[string]any{"matches": matches}}, err
}
