package tui

import (
	"context"
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
