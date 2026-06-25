package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	agentpkg "github.com/local/swe-agent/internal/agent"
	"github.com/local/swe-agent/internal/core"
	mockmodel "github.com/local/swe-agent/internal/model"
	"github.com/local/swe-agent/internal/policy"
	"github.com/local/swe-agent/internal/problemtrace"
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

func TestFinalReviewUsesNarrativeBeforeFallback(t *testing.T) {
	record := taskRecord{
		Task:      core.Task{Text: "fix it", Repo: "/repo"},
		Status:    "submitted",
		Narrative: RunNarrative{Status: "generated", Body: "The run completed successfully.\n\nEvidence: validation passed."},
		Result:    agentpkg.Result{Status: "submitted"},
	}
	snapshot := BuildRunSnapshot(record, "")

	detail := finalReviewWidth(record, snapshot, 80)

	if !strings.Contains(detail, "The run completed successfully.") {
		t.Fatalf("expected generated narrative, got:\n%s", detail)
	}
	if strings.Contains(detail, "Status: submitted") || strings.Contains(detail, "Need attention") {
		t.Fatalf("expected narrative to replace template fields, got:\n%s", detail)
	}
}

func TestFinalReviewShowsPendingNarrativeWithFallback(t *testing.T) {
	record := taskRecord{
		Task:      core.Task{Text: "fix it", Repo: "/repo"},
		Status:    "submitted",
		Narrative: RunNarrative{Status: "pending"},
		Result:    agentpkg.Result{Status: "submitted"},
	}
	snapshot := BuildRunSnapshot(record, "")

	detail := finalReviewWidth(record, snapshot, 80)

	if !strings.Contains(detail, "Status: submitted") || !strings.Contains(detail, "Generating review...") {
		t.Fatalf("expected fallback plus pending marker, got:\n%s", detail)
	}
}

func TestOverviewBodyUsesSingleFullWidthPanel(t *testing.T) {
	model := newLoopModel(NewSession(), &agentpkg.Agent{}, "/repo", context.Background())
	model.width = 100
	model.height = 30
	model.createTaskRecord(core.Task{Text: "fix it", Repo: "/repo"}, "submitted", time.Now())
	model.setSelectedTask(0)
	model.resize()

	body := model.bodyView()

	if strings.Contains(body, "Run #1") {
		t.Fatalf("expected overview body to omit run sidebar, got:\n%s", body)
	}
	if model.detail.Width != 96 {
		t.Fatalf("expected full-width detail viewport, got %d", model.detail.Width)
	}
}

func TestRunDoneStartsAsyncNarrativeGeneration(t *testing.T) {
	session := NewSession()
	ag := &agentpkg.Agent{Model: mockmodel.NewMock([]string{"The generated review."})}
	model := newLoopModel(session, ag, "/repo", context.Background())
	model.detail.Width = 80
	model.startRun(core.Task{Text: "fix it"})
	defer model.cancel()

	_, cmd := model.Update(runDoneMsg{result: agentpkg.Result{Status: "submitted"}, err: nil})
	if cmd == nil {
		t.Fatal("expected batched command after run done")
	}
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		t.Fatalf("expected batch message, got %T", msg)
	}
	var narrative narrativeReadyMsg
	found := false
	for _, batchCmd := range batch {
		if ready, ok := batchCmd().(narrativeReadyMsg); ok {
			narrative = ready
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected narrative generation command in batch")
	}
	if narrative.err != nil || narrative.body != "The generated review." {
		t.Fatalf("unexpected narrative result: body=%q err=%v", narrative.body, narrative.err)
	}

	model.Update(narrative)
	if got := model.tasks[0].Narrative.Status; got != "generated" {
		t.Fatalf("expected generated narrative status, got %q", got)
	}
	if !strings.Contains(model.detailContent(), "The generated review.") {
		t.Fatalf("expected detail to render generated narrative, got:\n%s", model.detailContent())
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

func TestTraceWorkspaceRendersPromptAndCards(t *testing.T) {
	manager := problemtrace.NewManager()
	task := core.Task{Text: "fix tests", Repo: "/repo"}
	var events []core.Event
	events = append(events, core.Event{Type: "user_task", Data: map[string]any{"task": task.Text, "repo": task.Repo}})
	events = append(events, manager.StartRun(context.Background(), task, problemtrace.TraceResource{RepoPath: "/repo", ModelProvider: "mock", Model: "mock"})...)
	prompt := manager.BuildPrompt(context.Background(), problemtrace.PromptInput{
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
	events = append(events, manager.FinishRun(context.Background(), "submitted", "done")...)

	record := taskRecord{Task: task, Events: events, Result: agentpkg.Result{TrajectoryPath: "trace.jsonl"}}
	promptView := traceWorkspaceViewWidth(record, traceWorkspaceState{Tab: traceTabPrompt}, 80, "trace.jsonl")
	if !strings.Contains(promptView, "prompt-1 step=1") || !strings.Contains(promptView, "Investigation Frontier") {
		t.Fatalf("expected prompt workspace content, got:\n%s", promptView)
	}
	cardsView := traceWorkspaceViewWidth(record, traceWorkspaceState{Tab: traceTabCards}, 80, "trace.jsonl")
	if !strings.Contains(cardsView, "run_summary") || !strings.Contains(cardsView, "draft") {
		t.Fatalf("expected draft cards workspace content, got:\n%s", cardsView)
	}
}

func TestTraceWorkspaceCompactTabsHideDebugDump(t *testing.T) {
	record := taskRecord{
		Task:   core.Task{Text: "fix go test import cycle", Repo: "/repo"},
		Events: mockImportCycleProblemTraceEvents(),
		Result: agentpkg.Result{TrajectoryPath: "trajectories/run-import-cycle.jsonl"},
	}

	frontier := traceWorkspaceViewWidth(record, traceWorkspaceState{Tab: traceTabFrontier}, 100, record.Result.TrajectoryPath)
	for _, want := range []string{"Active: Resolve the Go import cycle", "Next", "Open", "Which shared type"} {
		if !strings.Contains(frontier, want) {
			t.Fatalf("expected compact frontier to contain %q, got:\n%s", want, frontier)
		}
	}
	for _, unwanted := range []string{"Directions", "Stop Conditions", "Risks"} {
		if strings.Contains(frontier, unwanted) {
			t.Fatalf("expected compact frontier to hide %q, got:\n%s", unwanted, frontier)
		}
	}
	frontierDebug := traceWorkspaceViewWidth(record, traceWorkspaceState{Tab: traceTabFrontier, Debug: true}, 100, record.Result.TrajectoryPath)
	for _, want := range []string{"Directions", "Stop Conditions", "Risks"} {
		if !strings.Contains(frontierDebug, want) {
			t.Fatalf("expected debug frontier to contain %q, got:\n%s", want, frontierDebug)
		}
	}

	memory := traceWorkspaceViewWidth(record, traceWorkspaceState{Tab: traceTabMemory}, 100, record.Result.TrajectoryPath)
	if !strings.Contains(memory, "No memory used in this run.") || strings.Contains(memory, "Policy") {
		t.Fatalf("expected compact memory tab to show one-line empty state, got:\n%s", memory)
	}
	memoryDebug := traceWorkspaceViewWidth(record, traceWorkspaceState{Tab: traceTabMemory, Debug: true}, 100, record.Result.TrajectoryPath)
	if !strings.Contains(memoryDebug, "Policy") {
		t.Fatalf("expected debug memory tab to show policy, got:\n%s", memoryDebug)
	}

	events := traceWorkspaceViewWidth(record, traceWorkspaceState{Tab: traceTabEvents}, 100, record.Result.TrajectoryPath)
	for _, want := range []string{"symptom_detected", "direction_created", "evidence_added"} {
		if !strings.Contains(events, want) {
			t.Fatalf("expected compact events to contain %q, got:\n%s", want, events)
		}
	}
	for _, unwanted := range []string{"trace_span_ended", "prompt_snapshot", "frontier_updated"} {
		if strings.Contains(events, unwanted) {
			t.Fatalf("expected compact events to hide %q, got:\n%s", unwanted, events)
		}
	}
	eventsDebug := traceWorkspaceViewWidth(record, traceWorkspaceState{Tab: traceTabEvents, Debug: true}, 100, record.Result.TrajectoryPath)
	for _, want := range []string{"trace_span_ended", "prompt_snapshot", "frontier_updated"} {
		if !strings.Contains(eventsDebug, want) {
			t.Fatalf("expected debug events to contain %q, got:\n%s", want, eventsDebug)
		}
	}

	prompt := traceWorkspaceViewWidth(record, traceWorkspaceState{Tab: traceTabPrompt}, 100, record.Result.TrajectoryPath)
	for _, want := range []string{"Latest Prompt: prompt-1 step=1", "Context", "Investigation Frontier: yes"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("expected compact prompt to contain %q, got:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "Model: mock") || strings.Contains(prompt, "included=yes") {
		t.Fatalf("expected compact prompt to hide verbose block metadata, got:\n%s", prompt)
	}
	promptDebug := traceWorkspaceViewWidth(record, traceWorkspaceState{Tab: traceTabPrompt, Debug: true}, 100, record.Result.TrajectoryPath)
	for _, want := range []string{"Model: mock", "included=yes"} {
		if !strings.Contains(promptDebug, want) {
			t.Fatalf("expected debug prompt to contain %q, got:\n%s", want, promptDebug)
		}
	}
}

func TestTraceWorkspaceRendersConcreteTraceTreeExample(t *testing.T) {
	record := taskRecord{
		Task:   core.Task{Text: "fix go test import cycle", Repo: "/repo"},
		Events: mockImportCycleProblemTraceEvents(),
		Result: agentpkg.Result{TrajectoryPath: "trajectories/run-import-cycle.jsonl"},
	}

	state := traceWorkspaceState{
		Tab: traceTabTrace,
		Expanded: map[string]bool{
			"node-root":                true,
			"node-dir-go-import-cycle": true,
		},
	}
	rendered := traceWorkspaceViewWidth(record, state, 120, record.Result.TrajectoryPath)
	t.Logf("rendered trace tree:\n%s", rendered)

	for _, want := range []string{
		"Problem Trace Workspace",
		"[1 Trace]  2 Next  3 Memory  4 Events  5 Prompt  6 Learn",
		"Task: fix go test import cycle",
		"Status: running",
		"Validation: not recorded",
		"Active: Resolve the Go import cycle",
		"Symptom: Go compile failed with import cycle not allowed: go test ./...",
		"Trace Tree",
		"> [-] * Task  task  running",
		"+-- [ ] + Go compile failed with import cycle not allowed: go test ./...  symptom  observed",
		"+-- [-] + Resolve the Go import cycle  direction  supported",
		"|   `-- [ ] + package service imports handler and handler imports service  evidence  supports",
		"`-- [ ] * Review  verify  running",
		"Selected",
		"What: Task",
		"Why: fix go test import cycle",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("expected rendered trace tree to contain %q, got:\n%s", want, rendered)
		}
	}
	for _, unwanted := range []string{
		"Prompt snapshot 1",
		"Trace ID:",
		"Trajectory:",
		"Repository:",
		"Selected Node",
		"ID: node-root",
		"Span Graph",
	} {
		if strings.Contains(rendered, unwanted) {
			t.Fatalf("expected compact trace view to hide %q, got:\n%s", unwanted, rendered)
		}
	}
}

func TestTraceWorkspaceDebugRevealsTraceMetadata(t *testing.T) {
	record := taskRecord{
		Task:   core.Task{Text: "fix go test import cycle", Repo: "/repo"},
		Events: mockImportCycleProblemTraceEvents(),
		Result: agentpkg.Result{TrajectoryPath: "trajectories/run-import-cycle.jsonl"},
	}

	state := traceWorkspaceState{
		Tab:   traceTabTrace,
		Debug: true,
		Expanded: map[string]bool{
			"node-root": true,
		},
	}
	rendered := traceWorkspaceViewWidth(record, state, 120, record.Result.TrajectoryPath)

	for _, want := range []string{
		"Debug",
		"Trace ID: trace-import-cycle",
		"Trajectory: trajectories/run-import-cycle.jsonl",
		"Repository: /repo",
		"ID: node-root",
		"Kind: problem",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("expected debug trace view to contain %q, got:\n%s", want, rendered)
		}
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

func mockImportCycleProblemTraceEvents() []core.Event {
	traceID := "trace-import-cycle"
	task := "fix go test import cycle"
	repo := "/repo"
	runSpan := problemtrace.TraceSpan{
		TraceID: traceID,
		SpanID:  "span-1",
		Name:    problemtrace.SpanProblemRun,
		Kind:    "run",
		Status:  problemtrace.SpanStatusOK,
		Attributes: map[string]any{
			problemtrace.AttrRepoPath:      repo,
			problemtrace.AttrModelProvider: "mock",
			problemtrace.AttrModelName:     "mock",
		},
	}
	promptSpan := problemtrace.TraceSpan{
		TraceID:      traceID,
		SpanID:       "span-2",
		ParentSpanID: "span-1",
		Name:         problemtrace.SpanPromptBuild,
		Kind:         "prompt",
		Status:       problemtrace.SpanStatusOK,
	}
	modelSpan := problemtrace.TraceSpan{
		TraceID:      traceID,
		SpanID:       "span-3",
		ParentSpanID: "span-1",
		Name:         problemtrace.SpanModelCall,
		Kind:         "model",
		Status:       problemtrace.SpanStatusOK,
	}
	testSpan := problemtrace.TraceSpan{
		TraceID:      traceID,
		SpanID:       "span-4",
		ParentSpanID: "span-3",
		Name:         problemtrace.SpanTestRun,
		Kind:         "tool",
		Status:       problemtrace.SpanStatusError,
		Attributes: map[string]any{
			problemtrace.AttrToolName:           "run_tests",
			problemtrace.AttrTestCommand:        "go test ./...",
			problemtrace.AttrToolExitCode:       1,
			problemtrace.AttrTestErrorSignature: "go_import_cycle",
		},
		Links: []problemtrace.TraceLink{{
			TraceID: traceID,
			FromID:  "span-4",
			ToID:    "dir-go-import-cycle",
			Kind:    problemtrace.LinkSupports,
			Attributes: map[string]any{
				problemtrace.AttrDirectionID:    "dir-go-import-cycle",
				problemtrace.AttrErrorSignature: "go_import_cycle",
			},
		}},
	}
	prompt := problemtrace.PromptSnapshot{
		ID:           "prompt-1",
		Step:         1,
		Model:        "mock",
		MessageCount: 3,
		ToolCount:    1,
		Blocks: []problemtrace.PromptBlock{
			{Kind: "problem_context", Title: "Problem Context", Included: true, Content: task},
			{Kind: "frontier", Title: "Investigation Frontier", Included: true, Summary: "verify current import cycle before patching"},
		},
	}
	symptom := problemtrace.Symptom{
		ID:         "symptom-1",
		Kind:       "compile_error",
		Summary:    "Go compile failed with import cycle not allowed: go test ./...",
		ErrorType:  "go_import_cycle",
		Command:    "go test ./...",
		RawExcerpt: "package repo/service imports repo/handler imports repo/service: import cycle not allowed",
		Packages:   []string{"repo/service", "repo/handler"},
	}
	direction := problemtrace.InvestigationDirection{
		ID:         "dir-go-import-cycle",
		Hypothesis: "Resolve the Go import cycle",
		Rationale:  "The test output reports import cycle not allowed, so the next useful evidence is the exact dependency edge.",
		Status:     problemtrace.DirectionActive,
		Priority:   100,
		NextActions: []problemtrace.NextAction{{
			ID:          "next-go-import-cycle",
			Action:      "Inspect imports for repo/service and repo/handler.",
			Tool:        "grep",
			DirectionID: "dir-go-import-cycle",
			Priority:    100,
		}},
	}
	evidence := problemtrace.Evidence{
		ID:      "evidence-1",
		Summary: "package service imports handler and handler imports service",
		Detail:  "go test output closes the dependency cycle through repo/service and repo/handler",
		Source:  "tool_result",
	}
	frontier := problemtrace.InvestigationFrontier{
		ActiveDirectionID:   "dir-go-import-cycle",
		CandidateDirections: []string{"dir-go-import-cycle"},
		RecommendedActions:  direction.NextActions,
		OpenQuestions:       []string{"Which shared type or interface closes the cycle?"},
		StopConditions:      []string{"Focused go test command passes after the dependency edge is removed."},
		Risks:               []string{"Do not move code before confirming the exact import edge."},
	}
	traceContext := problemtrace.TraceContext{
		TraceID:          traceID,
		SpanID:           "span-4",
		ParentSpanID:     "span-3",
		DirectionID:      "dir-go-import-cycle",
		PromptSnapshotID: "prompt-1",
		Flags:            problemtrace.TraceFlags{Recording: true, Sampled: true},
	}

	return []core.Event{
		{Type: "user_task", Data: map[string]any{"task": task, "repo": repo}},
		{Type: "problem_trace_initialized", Data: map[string]any{
			"trace_id": traceID,
			"problem": problemtrace.ProblemContext{
				UserTask:     task,
				Repo:         repo,
				ErrorSummary: "Go compile failed with import cycle not allowed: go test ./...",
			},
			"resource": problemtrace.TraceResource{RepoPath: repo, ModelProvider: "mock", Model: "mock"},
		}},
		{Type: "trace_span_ended", Data: map[string]any{"trace_context": problemtrace.TraceContext{TraceID: traceID, SpanID: "span-1"}, "span": runSpan}},
		{Type: "trace_span_ended", Data: map[string]any{"trace_context": problemtrace.TraceContext{TraceID: traceID, SpanID: "span-2"}, "span": promptSpan}},
		{Type: "prompt_snapshot", Data: map[string]any{"trace_context": problemtrace.TraceContext{TraceID: traceID, SpanID: "span-2", PromptSnapshotID: "prompt-1"}, "snapshot": prompt}},
		{Type: "trace_span_ended", Data: map[string]any{"trace_context": problemtrace.TraceContext{TraceID: traceID, SpanID: "span-3", PromptSnapshotID: "prompt-1"}, "span": modelSpan}},
		{Type: "trace_span_ended", Data: map[string]any{"trace_context": traceContext, "span": testSpan}},
		{Type: "symptom_detected", Data: map[string]any{"trace_context": traceContext, "symptom": symptom}},
		{Type: "direction_created", Data: map[string]any{"trace_context": traceContext, "direction": direction}},
		{Type: "evidence_added", Data: map[string]any{"trace_context": traceContext, "direction_id": direction.ID, "evidence": evidence}},
		{Type: "frontier_updated", Data: map[string]any{"trace_context": traceContext, "frontier": frontier}},
	}
}

func scrollableTraceWorkspaceEvents() []core.Event {
	events := []core.Event{
		{Type: "user_task", Data: map[string]any{"task": "fix it", "repo": "/repo"}},
	}
	for i := 0; i < 30; i++ {
		events = append(events, core.Event{
			Type: "tool_call",
			Data: map[string]any{
				"tool": "shell",
				"args": map[string]any{"command": fmt.Sprintf("echo step-%02d", i)},
			},
		})
	}
	for i := 0; i < 12; i++ {
		events = append(events, core.Event{
			Type: "prompt_snapshot",
			Data: map[string]any{"snapshot": problemtrace.PromptSnapshot{
				ID:            fmt.Sprintf("prompt-%02d", i),
				Step:          i + 1,
				Model:         "mock",
				MessageCount:  3,
				ToolCount:     1,
				TokenEstimate: 100 + i,
				Blocks: []problemtrace.PromptBlock{{
					Kind:     "frontier",
					Title:    "Investigation Frontier",
					Included: true,
					Count:    i + 1,
					Summary:  fmt.Sprintf("follow-up trace context line %02d", i),
				}},
			}},
		})
	}
	for i := 0; i < 12; i++ {
		events = append(events, core.Event{
			Type: "memory_card_generated",
			Data: map[string]any{"card": problemtrace.MemoryCard{
				ID:           fmt.Sprintf("card-%02d", i),
				Kind:         "run_summary",
				Status:       "draft",
				Summary:      fmt.Sprintf("trace memory card summary %02d", i),
				FixPattern:   "keep the failing path focused",
				Verification: "rerun the focused command",
			}},
		})
	}
	return events
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

func TestSlashHelpShowsTutorialOverlay(t *testing.T) {
	model := newLoopModel(NewSession(), &agentpkg.Agent{}, "/repo", context.Background())
	model.width = 100
	model.height = 32
	model.mode = modeTask
	model.resize()

	model.executeSlashCommand("/help")

	if model.mode != modeHelp {
		t.Fatalf("expected help mode, got %v", model.mode)
	}
	view := model.View()
	if got := lipgloss.Height(view); got > model.height {
		t.Fatalf("help overlay height=%d exceeds terminal height=%d\n%s", got, model.height, view)
	}
	for _, want := range []string{"swe-agent", "Help", "Close: esc/q/?", "Composer", "Slash commands", "/history"} {
		if !strings.Contains(view, want) {
			t.Fatalf("expected help overlay to contain %q, got:\n%s", want, view)
		}
	}

	model.handleHelpKey(tea.KeyMsg{Type: tea.KeyEsc})
	if model.mode != modeTask {
		t.Fatalf("expected help close to restore task mode, got %v", model.mode)
	}
}

func TestHelpOverlayFitsTinyTerminal(t *testing.T) {
	for _, size := range []struct{ w, h int }{
		{20, 8},
		{30, 10},
		{40, 12},
	} {
		model := newLoopModel(NewSession(), &agentpkg.Agent{}, "/repo", context.Background())
		model.width = size.w
		model.height = size.h
		model.mode = modeTask
		model.resize()
		model.executeSlashCommand("/help")

		view := model.View()
		if got := lipgloss.Height(view); got > size.h {
			t.Fatalf("height=%d exceeds terminal height=%d\n%s", got, size.h, view)
		}
		for _, line := range strings.Split(view, "\n") {
			if got := lipgloss.Width(line); got > size.w {
				t.Fatalf("line width=%d exceeds terminal width=%d\n%s", got, size.w, view)
			}
		}
	}
}

func TestHelpOverlayRestoresApprovalMode(t *testing.T) {
	model := newLoopModel(NewSession(), &agentpkg.Agent{}, "/repo", context.Background())
	response := make(chan policy.ApprovalDecision, 1)

	model.Update(approvalMsg{
		request: policy.ApprovalRequest{
			Call: core.ToolCall{Name: "apply_patch"},
			Spec: core.ToolSpec{Name: "apply_patch"},
			Risk: core.RiskWrite,
		},
		response: response,
	})

	model.openHelp()
	if model.mode != modeHelp {
		t.Fatalf("expected help mode, got %v", model.mode)
	}

	model.handleHelpKey(tea.KeyMsg{Type: tea.KeyEsc})
	if model.mode != modeApproval {
		t.Fatalf("expected approval mode after closing help, got %v", model.mode)
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
	if !strings.Contains(detail, "Timeline") || !strings.Contains(detail, "go test ./...") {
		t.Fatalf("expected default detail to render timeline tool summaries, got:\n%s", detail)
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

func TestWideRunBodyShowsTimelineAndInspector(t *testing.T) {
	model := newLoopModel(NewSession(), &agentpkg.Agent{}, "/repo", context.Background())
	model.width = 120
	model.height = 30
	taskIndex := model.createTaskRecord(core.Task{Text: "fix it", Repo: "/repo"}, "submitted", time.Now())
	model.tasks[taskIndex].Events = []core.Event{
		{Type: "tool_call", Data: map[string]any{"tool": "run_tests", "args": map[string]any{"command": "go test ./..."}}},
		{Type: "tool_result", Data: map[string]any{"tool": "run_tests", "code": 0, "output": "ok"}},
	}
	model.setSelectedTask(taskIndex)
	model.resize()

	body := model.bodyView()

	if !strings.Contains(body, "Timeline") || !strings.Contains(body, "Inspector") || !strings.Contains(body, "[Plan]") {
		t.Fatalf("expected wide cockpit body to include timeline and inspector, got:\n%s", body)
	}
}

func TestWideTraceUsesFullWidthWorkspace(t *testing.T) {
	model := newLoopModel(NewSession(), &agentpkg.Agent{}, "/repo", context.Background())
	model.width = 120
	model.height = 30
	taskIndex := model.createTaskRecord(core.Task{Text: "fix go test import cycle", Repo: "/repo"}, "running", time.Now())
	model.tasks[taskIndex].Events = mockImportCycleProblemTraceEvents()
	model.setSelectedTask(taskIndex)

	model.openTraceWorkspace()
	model.resize()
	body := model.bodyView()

	if strings.Contains(body, "Inspector") || strings.Contains(body, "Timeline") {
		t.Fatalf("expected trace focus body to omit cockpit panes, got:\n%s", body)
	}
	if !strings.Contains(body, "Problem Trace Workspace") || !strings.Contains(body, "Trace Tree") {
		t.Fatalf("expected trace workspace body, got:\n%s", body)
	}
	if model.detail.Width != 116 {
		t.Fatalf("expected full-width trace detail viewport, got %d", model.detail.Width)
	}
	if model.focus != "detail" || model.sidebar != sidebarRun || model.mode != modeNormal {
		t.Fatalf("expected trace focus state, got focus=%q sidebar=%v mode=%v", model.focus, model.sidebar, model.mode)
	}
	if footer := model.footerView(); !strings.Contains(footer, "trace tabs") {
		t.Fatalf("expected trace-specific footer help, got:\n%s", footer)
	}
	if footer := model.footerView(); !strings.Contains(footer, "inspect") {
		t.Fatalf("expected trace footer inspect help, got:\n%s", footer)
	}
}

func TestTraceWorkspaceJKScrollsNonTreeTabs(t *testing.T) {
	model := newLoopModel(NewSession(), &agentpkg.Agent{}, "/repo", context.Background())
	model.width = 120
	model.height = 12
	taskIndex := model.createTaskRecord(core.Task{Text: "fix it", Repo: "/repo"}, "running", time.Now())
	model.tasks[taskIndex].Events = scrollableTraceWorkspaceEvents()
	model.setSelectedTask(taskIndex)
	model.openTraceWorkspace()
	model.setTraceTab("4")

	before := model.detail.YOffset
	model.handleNormalKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})

	if model.detail.YOffset <= before {
		t.Fatalf("expected j to scroll events tab, before=%d after=%d\n%s", before, model.detail.YOffset, model.detail.View())
	}

	model.handleNormalKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	if model.detail.YOffset != before {
		t.Fatalf("expected k to scroll events tab back to %d, got %d", before, model.detail.YOffset)
	}
}

func TestTraceWorkspaceDTogglesDebug(t *testing.T) {
	model := newLoopModel(NewSession(), &agentpkg.Agent{}, "/repo", context.Background())
	model.width = 120
	model.height = 12
	taskIndex := model.createTaskRecord(core.Task{Text: "fix go test import cycle", Repo: "/repo"}, "running", time.Now())
	model.tasks[taskIndex].Events = mockImportCycleProblemTraceEvents()
	model.setSelectedTask(taskIndex)
	model.openTraceWorkspace()
	model.detail.LineDown(3)

	model.handleNormalKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})

	if !model.traceView.Debug {
		t.Fatalf("expected trace debug mode to be enabled")
	}
	if model.detail.YOffset != 0 {
		t.Fatalf("expected debug toggle to reset viewport, got %d", model.detail.YOffset)
	}
	if detail := model.detailContent(); !strings.Contains(detail, "Trace ID: trace-import-cycle") {
		t.Fatalf("expected debug detail to reveal trace metadata, got:\n%s", detail)
	}

	model.handleNormalKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	if model.traceView.Debug {
		t.Fatalf("expected trace debug mode to be disabled")
	}
	if detail := model.detailContent(); strings.Contains(detail, "Trace ID: trace-import-cycle") {
		t.Fatalf("expected compact detail to hide trace metadata, got:\n%s", detail)
	}
}

func TestTraceWorkspaceTabNavigationResetsViewport(t *testing.T) {
	model := newLoopModel(NewSession(), &agentpkg.Agent{}, "/repo", context.Background())
	model.width = 120
	model.height = 12
	taskIndex := model.createTaskRecord(core.Task{Text: "fix it", Repo: "/repo"}, "running", time.Now())
	model.tasks[taskIndex].Events = scrollableTraceWorkspaceEvents()
	model.setSelectedTask(taskIndex)
	model.traceView.Debug = true

	model.detail.YOffset = 4
	model.openTraceWorkspace()
	if model.detail.YOffset != 0 {
		t.Fatalf("expected open trace workspace to reset viewport, got %d", model.detail.YOffset)
	}

	model.setTraceTab("5")
	model.detail.LineDown(5)
	if model.detail.YOffset == 0 {
		t.Fatalf("test setup expected prompt tab to overflow:\n%s", model.detail.View())
	}

	model.cycleTraceTab(1)
	if model.detail.YOffset != 0 {
		t.Fatalf("expected cycleTraceTab to reset viewport, got %d", model.detail.YOffset)
	}

	model.setTraceTab("6")
	model.detail.LineDown(5)
	if model.detail.YOffset == 0 {
		t.Fatalf("test setup expected cards tab to overflow:\n%s", model.detail.View())
	}

	model.setTraceTab("6")
	if model.detail.YOffset != 0 {
		t.Fatalf("expected setTraceTab to reset viewport, got %d", model.detail.YOffset)
	}
}

func TestWideTimelineShowsLatestItemsWhenOverflowing(t *testing.T) {
	model := newLoopModel(NewSession(), &agentpkg.Agent{}, "/repo", context.Background())
	model.width = 120
	model.height = 12
	taskIndex := model.createTaskRecord(core.Task{Text: "fix it", Repo: "/repo"}, "running", time.Now())
	for i := 0; i < 18; i++ {
		command := fmt.Sprintf("echo step-%02d", i)
		model.tasks[taskIndex].Events = append(model.tasks[taskIndex].Events,
			core.Event{Type: "tool_call", Data: map[string]any{"tool": "shell", "args": map[string]any{"command": command}}},
			core.Event{Type: "tool_result", Data: map[string]any{"tool": "shell", "code": 0, "output": command}},
		)
	}
	model.setSelectedTask(taskIndex)
	model.resize()

	body := model.bodyView()

	if !strings.Contains(body, "Timeline") || !strings.Contains(body, "...") || !strings.Contains(body, "echo step-17") {
		t.Fatalf("expected overflowing wide timeline to preserve header and latest item, got:\n%s", body)
	}
	if strings.Contains(body, "echo step-00") {
		t.Fatalf("expected overflowing wide timeline to omit oldest items, got:\n%s", body)
	}
}

func TestTimelineViewportFollowsLatestEvent(t *testing.T) {
	model := newLoopModel(NewSession(), &agentpkg.Agent{}, "/repo", context.Background())
	model.width = 80
	model.height = 12
	model.resize()
	model.startRun(core.Task{Text: "fix it"})
	defer model.cancel()

	model.addEvent(core.Event{Type: "user_task", Data: map[string]any{"task": "fix it", "repo": "/repo"}})
	for i := 0; i < 18; i++ {
		command := fmt.Sprintf("echo step-%02d", i)
		model.addEvent(core.Event{Type: "tool_call", Data: map[string]any{"tool": "shell", "args": map[string]any{"command": command}}})
		model.addEvent(core.Event{Type: "tool_result", Data: map[string]any{"tool": "shell", "code": 0, "output": command}})
	}

	if model.detail.YOffset == 0 {
		t.Fatalf("expected detail viewport to follow overflowing timeline, got yoffset=0\n%s", model.detail.View())
	}
	if !strings.Contains(model.detail.View(), "echo step-17") {
		t.Fatalf("expected viewport to show latest timeline item, got:\n%s", model.detail.View())
	}
}

func TestComposerStaysVisibleWithinTerminalHeight(t *testing.T) {
	for _, tc := range []struct {
		width  int
		height int
		mode   uiMode
	}{
		{80, 10, modeTask},
		{80, 12, modeNormal},
		{120, 10, modeTask},
		{120, 12, modeNormal},
	} {
		model := newLoopModel(NewSession(), &agentpkg.Agent{}, "/repo", context.Background())
		model.width = tc.width
		model.height = tc.height
		model.mode = tc.mode
		model.running = tc.mode == modeNormal
		model.resize()

		view := model.View()
		if got := lipgloss.Height(view); got > tc.height {
			t.Fatalf("view height=%d exceeds terminal height=%d for width=%d mode=%v\n%s", got, tc.height, tc.width, tc.mode, view)
		}
		if !strings.Contains(view, "Task") && !strings.Contains(view, "Busy") && !strings.Contains(view, "Message") {
			t.Fatalf("composer not visible for width=%d height=%d mode=%v\n%s", tc.width, tc.height, tc.mode, view)
		}
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
