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

func TestManagerEmitsTraceNodeAddedEvents(t *testing.T) {
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

	var nodeIDs []string
	for _, event := range events {
		if event.Type != "trace_node_added" {
			continue
		}
		var node TraceNode
		if !decodeInto(event.Data["node"], &node) {
			t.Fatalf("invalid trace node event payload: %#v", event.Data["node"])
		}
		nodeIDs = append(nodeIDs, node.ID)
	}

	for _, want := range []string{"node-root", "node-prompt-1", "node-dir-go-import-cycle", "node-evidence-1"} {
		if !contains(nodeIDs, want) {
			t.Fatalf("expected trace_node_added for %s, got %#v", want, nodeIDs)
		}
	}

	replayed := Replay(events)
	if len(replayed.History) != len(nodeIDs) {
		t.Fatalf("expected replayed history from trace_node_added events, got history=%d events=%d", len(replayed.History), len(nodeIDs))
	}
}

func TestManagerStartRunResetsPerRunState(t *testing.T) {
	ctx := context.Background()
	manager := NewManager()
	manager.StartRun(ctx, core.Task{Text: "first", Repo: "/repo"}, TraceResource{RepoPath: "/repo"})

	failCall := core.ToolCall{Name: "run_tests", Args: map[string]any{"command": "go test ./..."}}
	failTC, _ := manager.StartToolCall(ctx, 1, failCall)
	manager.ObserveToolResult(ctx, failTC, failCall, core.ToolResult{Code: 1, Output: "FAIL ./pkg"})

	patchCall := core.ToolCall{Name: "apply_patch", Args: map[string]any{"patch": "diff --git a/a b/a"}}
	patchTC, _ := manager.StartToolCall(ctx, 1, patchCall)
	manager.ObserveToolResult(ctx, patchTC, patchCall, core.ToolResult{Code: 0, Output: "ok"})

	passTC, _ := manager.StartToolCall(ctx, 1, failCall)
	manager.ObserveToolResult(ctx, passTC, failCall, core.ToolResult{Code: 0, Output: "ok"})

	manager.StartRun(ctx, core.Task{Text: "second", Repo: "/repo"}, TraceResource{RepoPath: "/repo"})
	prompt := manager.BuildPrompt(ctx, PromptInput{
		Step:       1,
		Model:      "mock",
		Provider:   "mock",
		WorkingDir: "/repo",
		Messages: []core.Message{
			{Role: core.RoleSystem, Content: "system"},
			{Role: core.RoleUser, Content: "second"},
		},
	})
	if prompt.Snapshot.ID != "prompt-1" {
		t.Fatalf("expected prompt sequence to reset, got %q", prompt.Snapshot.ID)
	}
	events := manager.FinishRun(ctx, "limit_reached", "second submission")
	for _, event := range events {
		if event.Type != "memory_card_generated" {
			continue
		}
		card, ok := event.Data["card"].(MemoryCard)
		if !ok {
			t.Fatalf("unexpected card payload: %#v", event.Data["card"])
		}
		if card.Kind == "fix_pattern" || card.Kind == "verification" {
			t.Fatalf("run-level state leaked into second run: %#v", card)
		}
	}
}

func TestPromptContextInsertedBeforeUserVisibleConversation(t *testing.T) {
	ctx := context.Background()
	manager := NewManager()
	manager.StartRun(ctx, core.Task{Text: "fix tests", Repo: "/repo"}, TraceResource{RepoPath: "/repo"})

	prompt := manager.BuildPrompt(ctx, PromptInput{
		Step:       1,
		Model:      "mock",
		Provider:   "mock",
		WorkingDir: "/repo",
		Messages: []core.Message{
			{Role: core.RoleSystem, Content: "system"},
			{Role: core.RoleUser, Content: "fix tests"},
		},
	})
	if len(prompt.Messages) != 2 {
		t.Fatalf("expected trace context to merge into existing system message, got %d messages", len(prompt.Messages))
	}
	if prompt.Messages[0].Role != core.RoleSystem || !strings.Contains(prompt.Messages[0].Content, "Problem Trace Context") {
		t.Fatalf("expected first system message to contain trace context, got %#v", prompt.Messages[0])
	}
	if prompt.Messages[len(prompt.Messages)-1].Role == core.RoleSystem {
		t.Fatalf("trace context should not be appended as a trailing system message: %#v", prompt.Messages)
	}

	prompt = manager.BuildPrompt(ctx, PromptInput{
		Step:       2,
		Model:      "mock",
		Provider:   "mock",
		WorkingDir: "/repo",
		Messages: []core.Message{
			{Role: core.RoleUser, Content: "fix tests"},
		},
	})
	if prompt.Messages[0].Role != core.RoleSystem || !strings.Contains(prompt.Messages[0].Content, "Problem Trace Context") {
		t.Fatalf("expected trace context to be prepended when no leading system exists, got %#v", prompt.Messages)
	}
}

func TestNonZeroToolResultCreatesSymptomDirectionEvidence(t *testing.T) {
	ctx := context.Background()
	manager := NewManager()
	manager.StartRun(ctx, core.Task{Text: "fix tests", Repo: "/repo"}, TraceResource{RepoPath: "/repo"})

	call := core.ToolCall{Name: "run_tests", Args: map[string]any{"command": "go test ./..."}}
	tc, _ := manager.StartToolCall(ctx, 1, call)
	manager.ObserveToolResult(ctx, tc, call, core.ToolResult{Code: 1, Output: "FAIL TestX\npanic: boom"})

	trace := manager.Snapshot()
	if len(trace.Symptoms) != 1 {
		t.Fatalf("expected one symptom, got %d", len(trace.Symptoms))
	}
	if len(trace.Directions) != 1 {
		t.Fatalf("expected one direction, got %d", len(trace.Directions))
	}
	if len(trace.Directions[0].SupportingEvidence) != 1 {
		t.Fatalf("expected supporting evidence, got %#v", trace.Directions[0])
	}
	ev := trace.Directions[0].SupportingEvidence[0]
	if ev.Relation != EvidenceSupports {
		t.Fatalf("expected supports relation, got %q", ev.Relation)
	}
	if ev.SourceSpanID == "" {
		t.Fatal("expected evidence to keep source span id")
	}
	if len(trace.Frontier.RecommendedActions) == 0 {
		t.Fatal("expected frontier recommendation")
	}
}

func TestRefutingEvidenceCanRefuteDirection(t *testing.T) {
	ctx := context.Background()
	manager := NewManager()
	manager.StartRun(ctx, core.Task{Text: "fix tests", Repo: "/repo"}, TraceResource{RepoPath: "/repo"})

	call := core.ToolCall{Name: "run_tests", Args: map[string]any{"command": "go test ./..."}}
	tc, _ := manager.StartToolCall(ctx, 1, call)
	manager.ObserveToolResult(ctx, tc, call, core.ToolResult{Code: 1, Output: "FAIL ./pkg"})

	tc, _ = manager.StartToolCall(ctx, 2, call)
	manager.ObserveToolResult(ctx, tc, call, core.ToolResult{Code: 0, Output: "ok"})

	trace := manager.Snapshot()
	if len(trace.Directions) != 1 {
		t.Fatalf("expected one direction, got %d", len(trace.Directions))
	}
	direction := trace.Directions[0]
	if direction.Status != DirectionRefuted {
		t.Fatalf("expected direction refuted, got %q", direction.Status)
	}
	if len(direction.RefutingEvidence) != 1 || direction.RefutingEvidence[0].Relation != EvidenceRefutes {
		t.Fatalf("expected refuting evidence, got %#v", direction.RefutingEvidence)
	}
}

func TestVerificationDoesNotFixUnrelatedDirection(t *testing.T) {
	ctx := context.Background()
	manager := NewManager()
	manager.StartRun(ctx, core.Task{Text: "fix tests", Repo: "/repo"}, TraceResource{RepoPath: "/repo"})

	failCall := core.ToolCall{Name: "run_tests", Args: map[string]any{"command": "go test ./pkg/a"}}
	tc, _ := manager.StartToolCall(ctx, 1, failCall)
	manager.ObserveToolResult(ctx, tc, failCall, core.ToolResult{Code: 1, Output: "FAIL ./pkg/a"})

	passCall := core.ToolCall{Name: "run_tests", Args: map[string]any{"command": "go test ./pkg/b"}}
	tc, _ = manager.StartToolCall(ctx, 2, passCall)
	manager.ObserveToolResult(ctx, tc, passCall, core.ToolResult{Code: 0, Output: "ok"})

	trace := manager.Snapshot()
	if len(trace.Directions) != 1 {
		t.Fatalf("expected one direction, got %d", len(trace.Directions))
	}
	if trace.Directions[0].Status == DirectionFixed {
		t.Fatalf("unrelated validation should not mark direction fixed: %#v", trace.Directions[0])
	}
	if len(trace.Directions[0].RefutingEvidence) != 0 {
		t.Fatalf("unrelated validation should not refute direction: %#v", trace.Directions[0].RefutingEvidence)
	}
}
