package problemtrace

import (
	"context"
	"strings"
	"testing"

	"github.com/local/swe-agent/internal/core"
)

func TestManagerTurnsImportCycleIntoFrontier(t *testing.T) {
	ctx := context.Background()
	manager := NewManager()
	events := manager.StartRun(ctx, core.Task{Text: "fix tests", Repo: "/repo"}, TraceResource{RepoPath: "/repo", ModelProvider: "mock", Model: "mock"})

	prompt := manager.BuildPrompt(ctx, PromptInput{
		Step:       1,
		Model:      "mock",
		Provider:   "mock",
		WorkingDir: "/repo",
		Messages: []core.Message{
			{Role: core.RoleSystem, Content: "system"},
			{Role: core.RoleUser, Content: "fix tests"},
		},
		Tools: []core.ToolSpec{{Name: "run_tests", Description: "run tests"}},
	})
	events = append(events, prompt.Events...)

	call := core.ToolCall{Name: "run_tests", Args: map[string]any{"command": "go test ./..."}}
	tc, startEvents := manager.StartToolCall(ctx, 1, call)
	events = append(events, startEvents...)
	events = append(events, manager.ObserveToolResult(ctx, tc, call, core.ToolResult{
		Code:   1,
		Output: "package example\n\timports example/internal/a\n\timports example/internal/b: import cycle not allowed\nFAIL ./...",
	})...)

	trace := manager.Snapshot()
	if len(trace.Symptoms) != 1 {
		t.Fatalf("expected one symptom, got %d", len(trace.Symptoms))
	}
	if trace.Symptoms[0].ErrorType != "go_import_cycle" {
		t.Fatalf("expected go_import_cycle, got %q", trace.Symptoms[0].ErrorType)
	}
	if trace.Frontier.ActiveDirectionID != "dir-go-import-cycle" {
		t.Fatalf("expected import-cycle direction active, got %q", trace.Frontier.ActiveDirectionID)
	}
	if len(trace.Frontier.RecommendedActions) == 0 {
		t.Fatal("expected recommended next action")
	}
	if len(trace.Links) == 0 || trace.Links[0].Kind != LinkSupports {
		t.Fatalf("expected support link, got %#v", trace.Links)
	}

	replayed := FromEvents(events)
	if replayed.Problem.ErrorSummary == "" {
		t.Fatal("expected replayed trace to keep error summary")
	}
	if len(replayed.Prompts) != 1 {
		t.Fatalf("expected one replayed prompt snapshot, got %d", len(replayed.Prompts))
	}
	if !strings.Contains(replayed.Frontier.RecommendedActions[0].Action, "import") {
		t.Fatalf("expected replayed frontier import action, got %#v", replayed.Frontier.RecommendedActions)
	}
}
