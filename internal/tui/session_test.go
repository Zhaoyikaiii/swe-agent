package tui

import (
	"context"
	"testing"
	"time"

	agentpkg "github.com/local/swe-agent/internal/agent"
	"github.com/local/swe-agent/internal/core"
)

func TestLoopSlashClearResetsVisibleSession(t *testing.T) {
	session := NewSession()
	model := newLoopModel(session, &agentpkg.Agent{}, "/repo", context.Background())
	model.events = []core.Event{{Type: "user_task", Time: time.Now(), Data: map[string]any{"task": "old"}}}
	model.selected = 0
	model.timelineOffset = 3
	model.result = agentpkg.Result{Status: "submitted", Steps: 1}
	model.done = true
	model.query = "old"
	session.events <- eventMsg{event: core.Event{Type: "tool_result"}}

	model.executeSlashCommand("/clear")

	if len(model.events) != 0 {
		t.Fatalf("expected events to be cleared, got %d", len(model.events))
	}
	if model.selected != -1 {
		t.Fatalf("expected selected=-1, got %d", model.selected)
	}
	if model.timelineOffset != 0 {
		t.Fatalf("expected timelineOffset=0, got %d", model.timelineOffset)
	}
	if model.done {
		t.Fatal("expected done=false after clear")
	}
	if model.result.Status != "" {
		t.Fatalf("expected result to be reset, got %q", model.result.Status)
	}
	if model.query != "" {
		t.Fatalf("expected query to be reset, got %q", model.query)
	}
	if len(session.events) != 0 {
		t.Fatalf("expected queued events to be drained, got %d", len(session.events))
	}
	if model.mode != modeTask {
		t.Fatalf("expected task input mode after /clear, got %v", model.mode)
	}
}

func TestStartRunFromLoopPreparesActiveTask(t *testing.T) {
	model := newLoopModel(NewSession(), &agentpkg.Agent{}, "/repo", context.Background())

	cmd := model.startRun(core.Task{Text: "  fix failing test  "})
	defer model.cancel()

	if cmd == nil {
		t.Fatal("expected startRun to return a command")
	}
	if !model.running {
		t.Fatal("expected running=true")
	}
	if model.done {
		t.Fatal("expected done=false while run is active")
	}
	if model.mode != modeNormal {
		t.Fatalf("expected normal mode while run is active, got %v", model.mode)
	}
	if model.task.Text != "fix failing test" {
		t.Fatalf("expected trimmed task text, got %q", model.task.Text)
	}
	if model.task.Repo != "/repo" {
		t.Fatalf("expected repo to be preserved, got %q", model.task.Repo)
	}
	if model.cancel == nil {
		t.Fatal("expected active cancel function")
	}
}
