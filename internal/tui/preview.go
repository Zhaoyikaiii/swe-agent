package tui

import (
	"context"
	"errors"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	agentpkg "github.com/local/swe-agent/internal/agent"
	"github.com/local/swe-agent/internal/core"
	"github.com/local/swe-agent/internal/problemtrace"
)

func (s *Session) PreviewTrace(ctx context.Context, events []core.Event, trajectoryPath string, repo string) error {
	m := newTracePreviewModel(s, events, trajectoryPath, repo, ctx)
	program := tea.NewProgram(m, tea.WithAltScreen())
	_, err := program.Run()
	if m.cancel != nil {
		m.cancel()
	}
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

func RenderTracePreview(events []core.Event, trajectoryPath string, repo string, width int, height int) string {
	if width <= 0 {
		width = 120
	}
	m := newTracePreviewModel(NewSession(), events, trajectoryPath, repo, context.Background())
	record := m.selectedTaskRecord()
	if record == nil {
		return "Problem Trace\n\nNo run selected."
	}
	return traceWorkspaceView(*record, m.traceView, width, height, trajectoryPath)
}

func newTracePreviewModel(session *Session, events []core.Event, trajectoryPath string, repo string, parent context.Context) *model {
	if session == nil {
		session = NewSession()
	}
	m := newModel(session, nil, core.Task{Text: "Trace preview", Repo: repo}, parent)
	m.result = agentpkg.Result{Status: "loaded", TrajectoryPath: trajectoryPath}
	m.done = true
	m.running = false
	m.status = "trace preview"
	m.setPhase(phaseFinished, "loaded trace preview")

	for _, event := range events {
		m.addEvent(event)
	}
	if len(m.tasks) == 0 {
		idx := m.createTaskRecord(core.Task{Text: "Trace preview", Repo: repo}, "loaded", time.Now())
		m.tasks[idx].Events = append([]core.Event(nil), events...)
		m.setSelectedTask(idx)
	}

	for i := range m.tasks {
		hydrateTracePreviewRecord(&m.tasks[i], events, trajectoryPath, repo)
	}
	if m.selectedTask < 0 && len(m.tasks) > 0 {
		m.setSelectedTask(len(m.tasks) - 1)
	}
	m.activeTask = -1
	m.loop = false
	m.mode = modeNormal
	m.openTraceWorkspace()
	m.status = "trace preview: " + trajectoryPath
	m.setPhase(phaseFinished, "loaded trace preview")
	return m
}

func hydrateTracePreviewRecord(record *taskRecord, events []core.Event, trajectoryPath string, repo string) {
	trace := problemtrace.Replay(events)
	if strings.TrimSpace(record.Task.Text) == "" {
		record.Task.Text = valueOrDefault(trace.Problem.UserTask, "Trace preview")
	}
	if strings.TrimSpace(record.Task.Repo) == "" {
		record.Task.Repo = valueOrDefault(trace.Problem.Repo, repo)
	}
	if strings.TrimSpace(record.Status) == "" || record.Status == "running" {
		record.Status = "loaded"
	}
	record.Result.TrajectoryPath = trajectoryPath
	if strings.TrimSpace(record.Result.Status) == "" {
		record.Result.Status = record.Status
	}
	if record.StartedAt.IsZero() && !trace.CreatedAt.IsZero() {
		record.StartedAt = trace.CreatedAt
	}
	if record.FinishedAt.IsZero() && !trace.UpdatedAt.IsZero() {
		record.FinishedAt = trace.UpdatedAt
	}
}
