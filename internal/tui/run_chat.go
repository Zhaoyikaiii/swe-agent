package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	agentpkg "github.com/local/swe-agent/internal/agent"
	"github.com/local/swe-agent/internal/core"
	"github.com/local/swe-agent/internal/problemtrace"
)

type chatReadyMsg struct {
	taskID int
	user   string
	answer string
	err    error
}

func runChatCmd(parent context.Context, ag *agentpkg.Agent, taskID int, record taskRecord, question string, selectedTraceNodeID string) tea.Cmd {
	return func() tea.Msg {
		answer, err := answerRunQuestion(parent, ag, record, question, selectedTraceNodeID)
		return chatReadyMsg{
			taskID: taskID,
			user:   question,
			answer: answer,
			err:    err,
		}
	}
}

func answerRunQuestion(ctx context.Context, ag *agentpkg.Agent, record taskRecord, question string, selectedTraceNodeID string) (string, error) {
	if ag == nil || ag.Model == nil {
		return "", errors.New("chat model is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	snapshot := BuildRunSnapshot(record, record.Result.TrajectoryPath)
	trace := problemtrace.Replay(record.Events)
	runContext := buildRunChatContext(record, snapshot, trace, selectedTraceNodeID)

	resp, err := ag.Model.Complete(ctx, core.ModelRequest{
		Messages: []core.Message{
			{
				Role:    core.RoleSystem,
				Content: runChatSystemPrompt,
			},
			{
				Role:    core.RoleUser,
				Content: runContext + "\n\nQuestion:\n" + strings.TrimSpace(question),
			},
		},
		Temperature: 0.1,
		MaxTokens:   700,
		WorkingDir:  record.Task.Repo,
		Mode:        core.ModelModeChat,
	})
	if err != nil {
		return "", err
	}
	answer := strings.TrimSpace(resp.Message.Content)
	if answer == "" {
		return "", errors.New("chat model returned an empty answer")
	}
	return answer, nil
}

const runChatSystemPrompt = `You answer questions about a completed SWE-agent run.

Rules:
- Use only the provided run context.
- Do not claim unseen facts.
- Do not emit tool calls.
- Do not use hidden reasoning.
- If the run lacks evidence, say what evidence is missing.`

func buildFollowupTask(record taskRecord, instruction string, selectedTraceNodeID string) core.Task {
	snapshot := BuildRunSnapshot(record, record.Result.TrajectoryPath)
	trace := problemtrace.Replay(record.Events)
	context := buildRunChatContext(record, snapshot, trace, selectedTraceNodeID)
	text := fmt.Sprintf(`Continue from the previous SWE-agent run.

Previous run context:
%s

Follow-up instruction:
%s`, context, strings.TrimSpace(instruction))

	return core.Task{
		Text: strings.TrimSpace(text),
		Repo: record.Task.Repo,
	}
}

func buildRunChatContext(record taskRecord, snapshot RunSnapshot, trace problemtrace.ProblemTrace, selectedTraceNodeID string) string {
	var b strings.Builder
	b.WriteString("Run Context\n")
	writeField(&b, "Task", record.Task.Text, 100)
	writeField(&b, "Repository", record.Task.Repo, 100)
	writeField(&b, "Status", valueOrDefault(record.Status, snapshot.Status), 100)
	if duration := taskDuration(record); duration != "" {
		writeField(&b, "Duration", duration, 100)
	}
	if conclusion := taskConclusion(record); conclusion != "" {
		writeField(&b, "Outcome", conclusion, 100)
	}
	if errText := lastErrorText(record); errText != "" {
		writeField(&b, "Error", errText, 100)
	}

	writeSection(&b, "Evidence")
	writeField(&b, "Diff", runChatDiffSummary(record, snapshot), 100)
	writeField(&b, "Validation", validationSummary(snapshot), 100)
	writeField(&b, "Trajectory", availabilityText(snapshot.FinalReview.Trajectory != ""), 100)

	nodes := buildTraceNarrativeNodes(record, trace)
	if selected := selectedTraceNodeSummary(nodes, selectedTraceNodeID); selected != "" {
		writeSection(&b, "Selected Trace Node")
		b.WriteString(wrapText(selected, 100))
		b.WriteByte('\n')
	}

	writeSection(&b, "Trace Summary")
	if len(nodes) == 0 {
		b.WriteString("No trace nodes recorded.\n")
	} else {
		wrote := 0
		for _, node := range nodes {
			if node.Kind == "prompt" {
				continue
			}
			line := fmt.Sprintf("- %s: %s", valueOrDefault(node.Kind, "node"), valueOrDefault(node.Title, node.Summary))
			if node.Status != "" {
				line += " [" + node.Status + "]"
			}
			b.WriteString(wrapText(line, 100))
			b.WriteByte('\n')
			wrote++
			if wrote >= 14 {
				break
			}
		}
	}

	writeSection(&b, "Key Events")
	rows := buildTraceEventRows(record.Events, false)
	if len(rows) == 0 {
		b.WriteString("No key events recorded.\n")
	} else {
		start := max(0, len(rows)-18)
		for _, row := range rows[start:] {
			line := fmt.Sprintf("#%d step=%d %s %s: %s", row.Index+1, row.Step, row.Icon, row.Title, row.Summary)
			b.WriteString(wrapText(line, 100))
			b.WriteByte('\n')
		}
	}

	gaps := runChatKnownGaps(record, snapshot)
	writeSection(&b, "Known Gaps")
	for _, gap := range gaps {
		b.WriteString("- ")
		b.WriteString(wrapText(gap, 98))
		b.WriteByte('\n')
	}

	return strings.TrimSpace(b.String())
}

func runChatDiffSummary(record taskRecord, snapshot RunSnapshot) string {
	if snapshot.FinalReview.ChangedFiles > 0 || strings.TrimSpace(record.Result.Diff) != "" {
		return summarizeDiff(record.Result.Diff)
	}
	return "not available"
}

func availabilityText(ok bool) string {
	if ok {
		return "available"
	}
	return "not available"
}

func selectedTraceNodeSummary(nodes []problemtrace.TraceNode, selectedTraceNodeID string) string {
	selectedTraceNodeID = strings.TrimSpace(selectedTraceNodeID)
	if selectedTraceNodeID == "" {
		return ""
	}
	for _, node := range nodes {
		if node.ID != selectedTraceNodeID {
			continue
		}
		parts := []string{valueOrDefault(node.Kind, "node")}
		if node.Title != "" {
			parts = append(parts, node.Title)
		}
		if node.Summary != "" {
			parts = append(parts, node.Summary)
		}
		if node.Status != "" {
			parts = append(parts, "status="+node.Status)
		}
		return strings.Join(parts, " - ")
	}
	return ""
}

func runChatKnownGaps(record taskRecord, snapshot RunSnapshot) []string {
	var gaps []string
	if len(record.Events) == 0 {
		gaps = append(gaps, "The TUI has no recorded events for this run.")
	}
	if snapshot.FinalReview.TestsRun == 0 {
		gaps = append(gaps, "No validation command or test result is recorded.")
	}
	if snapshot.FinalReview.ChangedFiles == 0 && strings.TrimSpace(record.Result.Diff) == "" {
		gaps = append(gaps, "No workspace diff is available in the run result.")
	}
	if strings.TrimSpace(taskConclusion(record)) == "" {
		gaps = append(gaps, "No final submission or assistant conclusion is recorded.")
	}
	if snapshot.FinalReview.Trajectory == "" {
		gaps = append(gaps, "No trajectory path is recorded.")
	}
	if len(gaps) == 0 {
		gaps = append(gaps, "No obvious evidence gaps from the projected context.")
	}
	return gaps
}
