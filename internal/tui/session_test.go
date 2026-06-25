package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	agentpkg "github.com/local/swe-agent/internal/agent"
	"github.com/local/swe-agent/internal/core"
	"github.com/local/swe-agent/internal/policy"
)

func TestLoopSlashClearResetsVisibleSession(t *testing.T) {
	session := NewSession()
	model := newLoopModel(session, &agentpkg.Agent{}, "/repo", context.Background())
	taskIndex := model.createTaskRecord(core.Task{Text: "old", Repo: "/repo"}, "submitted", time.Now())
	model.tasks[taskIndex].Events = []core.Event{{Type: "user_task", Time: time.Now(), Data: map[string]any{"task": "old"}}}
	model.tasks[taskIndex].Chat = []chatEntry{{Role: "user", Title: "Task", Body: "old"}}
	model.setSelectedTask(taskIndex)
	model.chatOffset = 3
	model.result = agentpkg.Result{Status: "submitted", Steps: 1}
	model.done = true
	model.query = "old"
	session.events <- eventMsg{event: core.Event{Type: "tool_result"}}

	model.executeSlashCommand("/clear")

	if len(model.tasks) != 0 {
		t.Fatalf("expected tasks to be cleared, got %d", len(model.tasks))
	}
	if model.selected != -1 {
		t.Fatalf("expected selected=-1, got %d", model.selected)
	}
	if model.selectedTask != -1 {
		t.Fatalf("expected selectedTask=-1, got %d", model.selectedTask)
	}
	if model.chatOffset != 0 {
		t.Fatalf("expected chatOffset=0, got %d", model.chatOffset)
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
	if len(model.tasks) != 1 {
		t.Fatalf("expected one task record, got %d", len(model.tasks))
	}
	if model.activeTask != 0 || model.selectedTask != 0 {
		t.Fatalf("expected active and selected task to be 0, got active=%d selected=%d", model.activeTask, model.selectedTask)
	}
	if model.tasks[0].Task.Text != "fix failing test" {
		t.Fatalf("expected task record to store trimmed task text, got %q", model.tasks[0].Task.Text)
	}
}

func TestLateFinalEventStaysOnActiveTaskAfterRunDone(t *testing.T) {
	model := newLoopModel(NewSession(), &agentpkg.Agent{}, "/repo", context.Background())
	model.detail.Width = 80
	cmd := model.startRun(core.Task{Text: "fix it"})
	if cmd == nil {
		t.Fatal("expected startRun to return a command")
	}
	defer model.cancel()

	model.finishActiveTask(agentpkg.Result{Status: "submitted", Steps: 2, Submission: "done"}, nil)
	model.addEvent(core.Event{Type: "final", Data: map[string]any{"status": "submitted", "steps": 2, "submission": "done"}})

	if len(model.tasks) != 1 {
		t.Fatalf("expected late final event to stay on existing task, got %d tasks", len(model.tasks))
	}
	if got := len(model.tasks[0].Chat); got != 1 {
		t.Fatalf("expected summary to be upserted once, got %d chat entries", got)
	}
	if !strings.Contains(model.detailContent(), "Review") || !strings.Contains(model.detailContent(), "done") {
		t.Fatalf("expected review detail, got:\n%s", model.detailContent())
	}
}

func TestSummaryFallsBackToAssistantProseWhenSubmitIsBare(t *testing.T) {
	model := newLoopModel(NewSession(), &agentpkg.Agent{}, "/repo", context.Background())
	model.detail.Width = 80

	model.addEvent(core.Event{Type: "model_response", Data: map[string]any{
		"content": "Fixed the issue and verified with go test.\n\n```swe_shell\nsubmit\n```",
	}})
	model.addEvent(core.Event{Type: "tool_call", Data: map[string]any{"tool": "submit", "args": map[string]any{}}})
	model.addEvent(core.Event{Type: "final", Data: map[string]any{"status": "submitted", "steps": 1, "submission": "submitted"}})

	detail := model.detailContent()
	if !strings.Contains(detail, "Fixed the") || !strings.Contains(detail, "issue and verified with go test.") {
		t.Fatalf("expected assistant prose as final conclusion, got:\n%s", detail)
	}
	if strings.Contains(detail, "Agent summary: submitted") {
		t.Fatalf("expected submitted placeholder to be ignored, got:\n%s", detail)
	}
}

func TestSummaryDoesNotTreatSubmittedPlaceholderAsConclusion(t *testing.T) {
	model := newLoopModel(NewSession(), &agentpkg.Agent{}, "/repo", context.Background())
	model.detail.Width = 80

	model.addEvent(core.Event{Type: "final", Data: map[string]any{"status": "submitted", "steps": 1, "submission": "submitted"}})

	detail := model.detailContent()
	if !strings.Contains(detail, "Review") || !strings.Contains(detail, "Status: submitted") {
		t.Fatalf("expected review without placeholder submission, got:\n%s", detail)
	}
	if strings.Contains(detail, "Agent summary: submitted") {
		t.Fatalf("expected submitted placeholder to be ignored, got:\n%s", detail)
	}
}

func TestErrorDetailRendersMultilineErrorText(t *testing.T) {
	event := core.Event{
		Type: "error",
		Data: map[string]any{
			"error": "model complete: codex exec failed\nstderr=OpenAI Codex\nERROR: unsupported model",
		},
	}

	detail := eventDetailWidth(event, 40)

	if !strings.Contains(detail, "Error:\n  model complete: codex exec failed\n  stderr=OpenAI Codex\n  ERROR: unsupported model") {
		t.Fatalf("expected rendered multiline error text, got:\n%s", detail)
	}
	if strings.Contains(detail, "\\n") {
		t.Fatalf("expected error newlines to be rendered, got escaped data:\n%s", detail)
	}
}

func TestErrorFinalSelectsSummaryWithErrorText(t *testing.T) {
	model := newLoopModel(NewSession(), &agentpkg.Agent{}, "/repo", context.Background())
	model.detail.Width = 80

	model.addEvent(core.Event{Type: "error", Data: map[string]any{"error": "codex failed with stderr"}})
	model.addEvent(core.Event{Type: "final", Data: map[string]any{"status": "error", "steps": 1}})

	if model.selected != -1 {
		t.Fatalf("expected final review to be selected, got index %d", model.selected)
	}
	if !strings.Contains(model.detailContent(), "codex failed with stderr") {
		t.Fatalf("expected final review to include error text, got:\n%s", model.detailContent())
	}
}

func TestModelLabelShowsProviderDefaultWhenModelIsEmpty(t *testing.T) {
	model := newLoopModel(NewSession(), &agentpkg.Agent{}, "/repo", context.Background())
	model.agent.Config.Model.Provider = "codex-cli"
	model.agent.Config.Model.Model = ""

	if got := model.modelLabel(); got != "codex-cli:default" {
		t.Fatalf("expected codex-cli default label, got %q", got)
	}
}

func TestSlashHistoryShowsTaskHistoryAndTaskDetail(t *testing.T) {
	model := newLoopModel(NewSession(), &agentpkg.Agent{}, "/repo", context.Background())
	model.detail.Width = 80
	first := model.createTaskRecord(core.Task{Text: "first task", Repo: "/repo"}, "submitted", time.Now())
	model.tasks[first].Events = []core.Event{{Type: "final", Data: map[string]any{"status": "submitted", "steps": 1}}}
	second := model.createTaskRecord(core.Task{Text: "second task", Repo: "/repo"}, "error", time.Now())
	model.tasks[second].Events = []core.Event{{Type: "error", Data: map[string]any{"error": "failed"}}}
	model.tasks[second].RunErr = context.Canceled
	model.setSelectedTask(second)

	model.executeSlashCommand("/history")

	if model.sidebar != sidebarHistory {
		t.Fatalf("expected history sidebar, got %v", model.sidebar)
	}
	if model.mode != modeNormal {
		t.Fatalf("expected normal mode so history can be navigated, got %v", model.mode)
	}
	detail := model.detailContent()
	if !strings.Contains(detail, "Task #2") || !strings.Contains(detail, "Task: second task") || !strings.Contains(detail, "Status: error") {
		t.Fatalf("expected structured task detail, got:\n%s", detail)
	}
	if strings.Contains(detail, "{") || strings.Contains(detail, "\"task\"") {
		t.Fatalf("expected task detail to avoid raw JSON, got:\n%s", detail)
	}
}

func TestHistoryEnterSwitchesSelectedTaskWorkbench(t *testing.T) {
	model := newLoopModel(NewSession(), &agentpkg.Agent{}, "/repo", context.Background())
	model.detail.Width = 80
	first := model.createTaskRecord(core.Task{Text: "first task", Repo: "/repo"}, "submitted", time.Now())
	model.tasks[first].Events = []core.Event{{Type: "tool_call", Data: map[string]any{"tool": "grep"}}}
	model.tasks[first].Chat = []chatEntry{{Role: "summary", Title: "Summary", Body: "first summary"}}
	second := model.createTaskRecord(core.Task{Text: "second task", Repo: "/repo"}, "submitted", time.Now())
	model.tasks[second].Events = []core.Event{{Type: "tool_call", Data: map[string]any{"tool": "shell"}}}
	model.tasks[second].Chat = []chatEntry{{Role: "summary", Title: "Summary", Body: "second summary"}}

	model.showHistory()
	model.selectTaskIndex(first)
	model.focus = "sidebar"
	model.handleNormalKey(tea.KeyMsg{Type: tea.KeyEnter})

	if model.sidebar != sidebarRun {
		t.Fatalf("expected run sidebar after enter, got %v", model.sidebar)
	}
	if model.selectedTask != first {
		t.Fatalf("expected first task to remain selected, got %d", model.selectedTask)
	}
	if model.selected != -1 {
		t.Fatalf("expected final review selection after task switch, got %d", model.selected)
	}
	if detail := model.detailContent(); !strings.Contains(detail, "Review") || !strings.Contains(detail, "Status: submitted") {
		t.Fatalf("expected first task workbench detail, got:\n%s", detail)
	}
}

func TestEventDetailRendersStructuredDataInsteadOfRawJSON(t *testing.T) {
	event := core.Event{
		Type: "tool_call",
		Data: map[string]any{
			"tool": "shell",
			"args": map[string]any{
				"command": "go test ./...",
				"env":     []any{"A=B"},
			},
		},
	}

	detail := eventDetailWidth(event, 80)

	for _, want := range []string{"Tool Call", "Tool: shell", "Arguments", "command: go test ./...", "env:", "- A=B"} {
		if !strings.Contains(detail, want) {
			t.Fatalf("expected detail to contain %q, got:\n%s", want, detail)
		}
	}
	if strings.Contains(detail, "\"command\"") || strings.Contains(detail, "{") || strings.Contains(detail, "}") {
		t.Fatalf("expected structured rendering without raw JSON, got:\n%s", detail)
	}
}

func TestDefaultRunSummaryHidesInternalStepTranscript(t *testing.T) {
	model := newLoopModel(NewSession(), &agentpkg.Agent{}, "/repo", context.Background())
	model.detail.Width = 80
	model.startRun(core.Task{Text: "fix it"})
	defer model.cancel()

	model.addEvent(core.Event{Type: "user_task", Data: map[string]any{"task": "fix it", "repo": "/repo"}})
	model.addEvent(core.Event{Type: "model_response", Data: map[string]any{"content": "I will inspect the tests."}})
	model.addEvent(core.Event{Type: "tool_call", Data: map[string]any{"tool": "shell", "args": map[string]any{"command": "go test ./..."}}})
	model.addEvent(core.Event{Type: "tool_result", Data: map[string]any{"tool": "shell", "code": 0, "output": "ok"}})

	chat := model.selectedChat()
	if len(chat) != 1 || chat[0].Title != "Task" {
		t.Fatalf("expected default run summary to show only task before outcome/attention, got %#v", chat)
	}
	detail := model.detailContent()
	if strings.Contains(detail, "Step 1") || strings.Contains(detail, "Tool Call") || strings.Contains(detail, "model_response") {
		t.Fatalf("expected default detail to hide internal steps, got:\n%s", detail)
	}

	model.executeSlashCommand("/steps")
	if model.view != viewSteps {
		t.Fatalf("expected steps view, got %v", model.view)
	}
	steps := model.detailContent()
	if !strings.Contains(steps, "Step 1") || !strings.Contains(steps, "go test ./...") {
		t.Fatalf("expected explicit steps view to preserve drill-down, got:\n%s", steps)
	}
}

func TestPhaseHintsFollowRunLifecycle(t *testing.T) {
	model := newLoopModel(NewSession(), &agentpkg.Agent{}, "/repo", context.Background())
	model.width = 100

	if model.phase != phaseReady || model.phaseLabel() != "Ready" {
		t.Fatalf("expected ready phase, got %v %q", model.phase, model.phaseLabel())
	}

	model.startRun(core.Task{Text: "fix it"})
	defer model.cancel()
	if model.phase != phaseThinking {
		t.Fatalf("expected thinking phase after start, got %v", model.phase)
	}

	model.addEvent(core.Event{Type: "tool_call", Data: map[string]any{"tool": "shell"}})
	if model.phase != phaseTool {
		t.Fatalf("expected tool phase, got %v", model.phase)
	}
	if !strings.Contains(model.phaseLabel(), "tool") || !strings.Contains(model.statusHint(), "shell") {
		t.Fatalf("expected tool phase hint for shell, got label=%q hint=%q", model.phaseLabel(), model.statusHint())
	}

	response := make(chan policy.ApprovalDecision, 1)
	model.Update(approvalMsg{
		request: policy.ApprovalRequest{
			Call: core.ToolCall{Name: "apply_patch"},
			Spec: core.ToolSpec{Name: "apply_patch"},
			Risk: core.RiskWrite,
		},
		response: response,
	})
	if model.phase != phaseApproval {
		t.Fatalf("expected approval phase, got %v", model.phase)
	}
	if !strings.Contains(model.phaseLabel(), "approval") || !strings.Contains(model.statusHint(), "apply_patch") {
		t.Fatalf("expected approval phase hint for apply_patch, got label=%q hint=%q", model.phaseLabel(), model.statusHint())
	}

	model.answerApproval(policy.ApprovalDecision{Allowed: true})
	model.Update(runDoneMsg{result: agentpkg.Result{Status: "submitted", Steps: 3}, err: nil})
	if model.phase != phaseFinished {
		t.Fatalf("expected finished phase, got %v", model.phase)
	}
	if !strings.Contains(model.footerView(), "Finished") {
		t.Fatalf("expected footer to expose finished phase, got %q", model.footerView())
	}
}

func TestBuildRunSnapshotGroupsActionObservationAndArtifacts(t *testing.T) {
	start := time.Unix(100, 0)
	record := taskRecord{
		Task:   core.Task{Text: "fix failing test", Repo: "/repo"},
		Status: "submitted",
		Events: []core.Event{
			{Type: "model_response", Time: start, Data: map[string]any{"content": "I will run the focused test."}},
			{Type: "tool_call", Time: start.Add(time.Second), Data: map[string]any{"tool": "run_tests", "args": map[string]any{"command": "go test ./internal/tui"}}},
			{Type: "tool_result", Time: start.Add(3 * time.Second), Data: map[string]any{"tool": "run_tests", "code": 1, "output": "FAIL TestX"}},
		},
		Result: agentpkg.Result{
			Status: "submitted",
			Steps:  1,
			Diff:   "diff --git a/internal/tui/session.go b/internal/tui/session.go\n",
		},
	}

	snapshot := BuildRunSnapshot(record, "/tmp/run.jsonl")

	if len(snapshot.Steps) != 1 {
		t.Fatalf("expected one grouped step, got %#v", snapshot.Steps)
	}
	step := snapshot.Steps[0]
	if step.Phase != "validate" || step.Command != "go test ./internal/tui" || step.Outcome != "exit 1" {
		t.Fatalf("unexpected step summary: %#v", step)
	}
	if !strings.Contains(step.Why, "focused test") || !strings.Contains(step.Output, "FAIL TestX") {
		t.Fatalf("expected why and observation to be preserved: %#v", step)
	}
	if snapshot.FinalReview.ChangedFiles != 1 || snapshot.FinalReview.TestsRun != 1 || snapshot.FinalReview.TestsPassed != 0 {
		t.Fatalf("unexpected final review counters: %#v", snapshot.FinalReview)
	}
	if snapshot.FinalReview.Trajectory != "/tmp/run.jsonl" {
		t.Fatalf("expected trajectory path, got %q", snapshot.FinalReview.Trajectory)
	}
}

func TestBuildRunSnapshotInfersShellValidationCommands(t *testing.T) {
	record := taskRecord{
		Task:   core.Task{Text: "fix failing test", Repo: "/repo"},
		Status: "submitted",
		Events: []core.Event{
			{Type: "tool_call", Data: map[string]any{"tool": "shell", "args": map[string]any{"command": "go test ./..."}}},
			{Type: "tool_result", Data: map[string]any{"tool": "shell", "code": 0, "output": "ok"}},
		},
		Result: agentpkg.Result{Status: "submitted"},
	}

	snapshot := BuildRunSnapshot(record, "")

	if len(snapshot.Steps) != 1 {
		t.Fatalf("expected one shell step, got %#v", snapshot.Steps)
	}
	if snapshot.Steps[0].Phase != "validate" {
		t.Fatalf("expected shell test command to be validation, got %#v", snapshot.Steps[0])
	}
	if snapshot.FinalReview.TestsRun != 1 || snapshot.FinalReview.TestsPassed != 1 {
		t.Fatalf("expected shell test command in validation counters, got %#v", snapshot.FinalReview)
	}
}

func TestSlashTestsShowsValidationView(t *testing.T) {
	model := newLoopModel(NewSession(), &agentpkg.Agent{}, "/repo", context.Background())
	model.detail.Width = 80
	taskIndex := model.createTaskRecord(core.Task{Text: "fix it", Repo: "/repo"}, "submitted", time.Now())
	model.tasks[taskIndex].Events = []core.Event{
		{Type: "tool_call", Data: map[string]any{"tool": "run_tests", "args": map[string]any{"command": "go test ./..."}}},
		{Type: "tool_result", Data: map[string]any{"tool": "run_tests", "code": 0, "output": "ok"}},
	}
	model.setSelectedTask(taskIndex)

	model.executeSlashCommand("/tests")

	if model.view != viewTests {
		t.Fatalf("expected tests view, got %v", model.view)
	}
	detail := model.detailContent()
	if !strings.Contains(detail, "Validation") || !strings.Contains(detail, "go test ./...") || !strings.Contains(detail, "Status: ok") {
		t.Fatalf("expected validation detail, got:\n%s", detail)
	}
}
