package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	agentpkg "github.com/local/swe-agent/internal/agent"
	"github.com/local/swe-agent/internal/core"
	"github.com/local/swe-agent/internal/problemtrace"
)

func TestTraceTreeBuildsParentChildRows(t *testing.T) {
	nodes := []problemtrace.TraceNode{
		{ID: "node-root", Kind: "problem", Title: "Problem", Status: "running"},
		{ID: "node-dir", ParentID: "node-root", Kind: "direction", Title: "Fix import cycle", Status: "active"},
		{ID: "node-ev", ParentID: "node-dir", Kind: "evidence", Title: "import cycle detected", Status: "ok"},
	}

	vm := buildTraceTreeVM(nodes)
	rows := flattenTraceTree(vm, map[string]bool{
		"node-root": true,
		"node-dir":  true,
	})
	got := renderTraceTreeASCII(rows, traceWorkspaceState{Cursor: 0}, 100)

	for _, want := range []string{"Problem", "`--", "Fix import cycle", "import cycle detected"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected tree to contain %q, got:\n%s", want, got)
		}
	}
}

func TestTraceTreeWeakensGenericObservationDirection(t *testing.T) {
	nodes := []problemtrace.TraceNode{
		{ID: "node-root", Kind: "problem", Title: "Problem", Status: "running"},
		{ID: "node-dir-collect-current-repository-evidence", ParentID: "node-root", Kind: "direction", Title: "Collect current repository evidence", Status: "active"},
	}

	vm := buildTraceTreeVM(nodes)
	rows := flattenTraceTree(vm, map[string]bool{
		"node-root": true,
	})
	got := renderTraceTreeASCII(rows, traceWorkspaceState{Cursor: 1}, 100)

	if !strings.Contains(got, "Observation captured") || !strings.Contains(got, "observation") {
		t.Fatalf("expected generic direction to render as an observation, got:\n%s", got)
	}
	if strings.Contains(got, "Collect current repository evidence") {
		t.Fatalf("expected generic direction title to be hidden, got:\n%s", got)
	}
}

func TestTraceTreeCollapseHidesChildren(t *testing.T) {
	nodes := []problemtrace.TraceNode{
		{ID: "node-root", Kind: "problem", Title: "Problem"},
		{ID: "node-child", ParentID: "node-root", Kind: "direction", Title: "Child"},
	}

	vm := buildTraceTreeVM(nodes)
	rows := flattenTraceTree(vm, map[string]bool{
		"node-root": false,
	})

	if len(rows) != 1 {
		t.Fatalf("expected only root row, got %d", len(rows))
	}
	if rows[0].NodeID != "node-root" {
		t.Fatalf("expected root row, got %#v", rows[0])
	}
}

func TestTraceNarrativeBuildsVisiblePlanActionObservation(t *testing.T) {
	record := taskRecord{
		Task: core.Task{Text: "review current code", Repo: "/repo"},
		Events: []core.Event{
			{Type: "user_task", Data: map[string]any{"task": "review current code", "repo": "/repo"}},
			{Type: "model_response", Data: map[string]any{
				"step":    1,
				"content": "I will inspect the repository before answering.\n```shell\nrg --files\n```",
			}},
			{Type: "tool_call", Data: map[string]any{"tool": "shell", "args": map[string]any{"command": "rg --files"}}},
			{Type: "tool_result", Data: map[string]any{"tool": "shell", "code": 0, "output_preview": "internal/tui/trace_tree.go\n"}},
		},
	}

	tree := buildTraceTreeVM(buildTraceNarrativeNodes(record, problemtrace.Replay(record.Events)))
	rows := flattenTraceTree(tree, map[string]bool{
		"node-root":        true,
		"node-step-1-note": true,
		"node-action-2":    true,
	})
	got := renderTraceTreeASCII(rows, traceWorkspaceState{Cursor: 1}, 120)

	for _, want := range []string{
		"Step 1: AI plan",
		"I will inspect the repository before answering.",
		"List repository files",
		"command succeeded",
		"reason",
		"action",
		"observation",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected narrative tree to contain %q, got:\n%s", want, got)
		}
	}
	if strings.Contains(got, "```") {
		t.Fatalf("expected fenced tool block to be stripped from visible note, got:\n%s", got)
	}
	if strings.Contains(got, "shell: rg --files") {
		t.Fatalf("expected tree action title to hide raw command transcript, got:\n%s", got)
	}
}

func TestActionTitleClassifiesCommonCommands(t *testing.T) {
	tests := []struct {
		name    string
		tool    string
		command string
		want    string
	}{
		{
			name:    "review threads",
			tool:    "shell",
			command: "gh api graphql -f query='reviewThreads { nodes { id } }'",
			want:    "Fetch unresolved PR review threads",
		},
		{
			name:    "metadata",
			tool:    "shell",
			command: "gh pr view 42 --json title",
			want:    "Read PR metadata",
		},
		{
			name:    "diff",
			tool:    "shell",
			command: "git diff -- internal/tui/trace_tree.go",
			want:    "Review local diff",
		},
		{
			name:    "patch tool",
			tool:    "apply_patch",
			command: "",
			want:    "Apply patch",
		},
		{
			name:    "verification",
			tool:    "shell",
			command: "go test ./internal/tui",
			want:    "Run verification",
		},
		{
			name:    "read text",
			tool:    "shell",
			command: "python - <<'PY'\nfrom pathlib import Path\nprint(Path('internal/tui/trace_tree.go').read_text())\nPY",
			want:    "Inspect referenced code",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := actionTitle(tt.tool, tt.command); got != tt.want {
				t.Fatalf("expected %q, got %q", tt.want, got)
			}
		})
	}
}

func TestTraceNarrativeHidesPromptSnapshotsUnlessDebug(t *testing.T) {
	record := taskRecord{
		Task: core.Task{Text: "fix it", Repo: "/repo"},
		Events: []core.Event{
			{Type: "user_task", Data: map[string]any{"task": "fix it", "repo": "/repo"}},
			{Type: "prompt_snapshot", Data: map[string]any{"snapshot": problemtrace.PromptSnapshot{
				ID:           "prompt-1",
				Step:         1,
				MessageCount: 3,
				ToolCount:    1,
			}}},
		},
	}

	compact := traceWorkspaceViewWidth(record, traceWorkspaceState{Tab: traceTabTrace}, 100, "trace.jsonl")
	if strings.Contains(compact, "Prompt snapshot 1") {
		t.Fatalf("expected compact trace to hide prompt snapshots, got:\n%s", compact)
	}

	debug := traceWorkspaceViewWidth(record, traceWorkspaceState{Tab: traceTabTrace, Debug: true, Expanded: map[string]bool{"node-root": true}}, 100, "trace.jsonl")
	if !strings.Contains(debug, "Prompt snapshot 1") || !strings.Contains(debug, "prompt") {
		t.Fatalf("expected debug trace to show raw prompt snapshot, got:\n%s", debug)
	}
}

func TestTraceNarrativeCompactsRepeatedEvidence(t *testing.T) {
	trace := problemtrace.ProblemTrace{
		Problem: problemtrace.ProblemContext{UserTask: "inspect repo"},
		Directions: []problemtrace.InvestigationDirection{{
			ID:         "dir-collect-current-repository-evidence",
			Hypothesis: "Collect current repository evidence",
			Rationale:  "Tool observations should be interpreted before patching.",
			Status:     problemtrace.DirectionSupported,
			SupportingEvidence: []problemtrace.Evidence{
				{ID: "evidence-1", Summary: "shell observation captured", Detail: "first output", EventIDs: []int{1}},
				{ID: "evidence-2", Summary: "shell observation captured", Detail: "second output", EventIDs: []int{2}},
			},
		}},
	}
	record := taskRecord{Task: core.Task{Text: "inspect repo", Repo: "/repo"}}

	tree := buildTraceTreeVM(buildTraceNarrativeNodes(record, trace))
	rows := flattenTraceTree(tree, map[string]bool{
		"node-root": true,
		"node-dir-collect-current-repository-evidence": true,
	})
	got := renderTraceTreeASCII(rows, traceWorkspaceState{Cursor: 2}, 120)

	if !strings.Contains(got, "shell observations captured x2") {
		t.Fatalf("expected repeated evidence to be grouped, got:\n%s", got)
	}
	if strings.Count(got, "shell observation captured") > 1 {
		t.Fatalf("expected repeated evidence to appear once, got:\n%s", got)
	}
}

func TestTraceDetailDefaultsFailedActionToOutput(t *testing.T) {
	record := taskRecord{
		Task: core.Task{Text: "fix failing tests", Repo: "/repo"},
		Events: []core.Event{
			{Type: "user_task", Data: map[string]any{"task": "fix failing tests", "repo": "/repo"}},
			{Type: "tool_call", Data: map[string]any{"tool": "shell", "args": map[string]any{"command": "go test ./..."}}},
			{Type: "tool_result", Data: map[string]any{"tool": "shell", "code": 1, "output_preview": "FAIL ./internal/tui"}},
		},
	}
	state := traceWorkspaceState{
		Tab:        traceTabTrace,
		SelectedID: "node-action-1",
		Expanded: map[string]bool{
			"node-root":     true,
			"node-action-1": true,
		},
	}

	rendered := traceWorkspaceView(record, state, 120, 18, "trace.jsonl")

	for _, want := range []string{
		"Run verification",
		"Overview  [Output]  Events  Debug",
		"Result: exit code 1",
		"FAIL ./internal/tui",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("expected failed action detail to contain %q, got:\n%s", want, rendered)
		}
	}
}

func TestTraceDetailDebugShowsRawCommand(t *testing.T) {
	record := taskRecord{
		Task: core.Task{Text: "inspect tests", Repo: "/repo"},
		Events: []core.Event{
			{Type: "user_task", Data: map[string]any{"task": "inspect tests", "repo": "/repo"}},
			{Type: "tool_call", Data: map[string]any{"tool": "shell", "args": map[string]any{"command": "go test ./internal/tui -run TestTrace"}}},
		},
	}
	state := traceWorkspaceState{
		Tab:        traceTabTrace,
		Pane:       tracePaneDetail,
		DetailTab:  traceDetailDebug,
		SelectedID: "node-action-1",
		Expanded: map[string]bool{
			"node-root": true,
		},
	}

	rendered := traceWorkspaceView(record, state, 120, 18, "trace.jsonl")

	if !strings.Contains(rendered, "Raw command:") || !strings.Contains(rendered, "go test ./internal/tui -run TestTrace") {
		t.Fatalf("expected debug detail to show raw command, got:\n%s", rendered)
	}
}

func TestTraceSplitKeepsSelectedTreeRowVisible(t *testing.T) {
	rows := make([]TraceTreeRow, 20)
	for i := range rows {
		rows[i] = TraceTreeRow{
			NodeID: fmt.Sprintf("node-%02d", i),
			Title:  fmt.Sprintf("Child %02d", i),
			Kind:   "action",
			Status: "ok",
		}
	}
	content := renderTraceTreeASCIIOptions(rows, traceWorkspaceState{Cursor: 18}, 80, false)

	got := fitTraceTreeAroundCursor(content, 8, 18)

	if !strings.Contains(got, "Child 18") {
		t.Fatalf("expected selected row to stay visible, got:\n%s", got)
	}
	if strings.Contains(got, "Child 00") {
		t.Fatalf("expected tree body to scroll around the selected row, got:\n%s", got)
	}
}

func TestTraceCursorMovesWithinRows(t *testing.T) {
	m := newLoopModel(NewSession(), &agentpkg.Agent{}, "/repo", context.Background())
	m.detail.Width = 100
	taskIndex := m.createTaskRecord(core.Task{Text: "fix it", Repo: "/repo"}, "running", time.Time{})
	m.tasks[taskIndex].Events = []core.Event{
		traceNodeEvent(problemtrace.TraceNode{ID: "node-root", Kind: "problem", Title: "Problem"}),
		traceNodeEvent(problemtrace.TraceNode{ID: "node-child", ParentID: "node-root", Kind: "direction", Title: "Child"}),
	}
	m.setSelectedTask(taskIndex)
	m.view = viewTrace
	m.traceView = traceWorkspaceState{
		Tab: traceTabTrace,
		Expanded: map[string]bool{
			"node-root": true,
		},
	}

	m.moveTraceCursor(1)

	if m.traceView.Cursor != 1 {
		t.Fatalf("expected cursor=1, got %d", m.traceView.Cursor)
	}
	if m.traceView.SelectedID != "node-child" {
		t.Fatalf("expected selected node-child, got %q", m.traceView.SelectedID)
	}
}

func traceNodeEvent(node problemtrace.TraceNode) core.Event {
	return core.Event{
		Type: "trace_node_added",
		Data: map[string]any{"node": node},
	}
}
