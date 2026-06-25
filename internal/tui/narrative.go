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
)

func generateNarrativeCmd(parent context.Context, ag *agentpkg.Agent, taskID int, snapshot RunSnapshot, events []core.Event) tea.Cmd {
	return func() tea.Msg {
		body, err := generateNarrative(parent, ag, snapshot, events)
		return narrativeReadyMsg{
			taskID: taskID,
			body:   body,
			err:    err,
		}
	}
}

func generateNarrative(parent context.Context, ag *agentpkg.Agent, snapshot RunSnapshot, events []core.Event) (string, error) {
	if ag == nil || ag.Model == nil {
		return "", errors.New("narrative model is not configured")
	}
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, 45*time.Second)
	defer cancel()

	resp, err := ag.Model.Complete(ctx, core.ModelRequest{
		Messages: []core.Message{
			{
				Role:    core.RoleSystem,
				Content: narrativeSystemPrompt,
			},
			{
				Role:    core.RoleUser,
				Content: buildNarrativeFacts(snapshot, events),
			},
		},
		Temperature: 0.2,
		MaxTokens:   320,
		WorkingDir:  snapshot.Task.Repo,
	})
	if err != nil {
		return "", err
	}
	body := cleanNarrative(resp.Message.Content)
	if body == "" {
		return "", errors.New("narrative model returned an empty review")
	}
	return body, nil
}

const narrativeSystemPrompt = `You are writing a concise terminal review for a SWE-agent run.

Rules:
- Use only the facts provided.
- Do not invent changed files, tests, commands, or results.
- If the final summary is missing, say that no final summary was recorded.
- Keep it under 12 lines.
- Prefer natural prose over rigid templates.
- Include evidence only when useful.`

func buildNarrativeFacts(snapshot RunSnapshot, events []core.Event) string {
	var b strings.Builder
	b.WriteString("Facts:\n")
	writeFact(&b, "Task", snapshot.Task.Text)
	writeFact(&b, "Status", snapshot.FinalReview.Status)
	writeFact(&b, "Repository", snapshot.Task.Repo)
	writeFact(&b, "Changed files", fmt.Sprintf("%d", snapshot.FinalReview.ChangedFiles))
	writeFact(&b, "Diff", diffFact(snapshot))
	writeFact(&b, "Validation", validationSummary(snapshot))
	writeFact(&b, "Trace", availabilityFact(snapshot.FinalReview.Trajectory != ""))
	writeFact(&b, "Submission", submissionFact(snapshot.FinalReview.Submission))
	writeFact(&b, "Errors", errorsFact(snapshot))
	if steps := importantStepFacts(snapshot.Steps); len(steps) > 0 {
		b.WriteString("Important steps:\n")
		for _, step := range steps {
			b.WriteString("- ")
			b.WriteString(step)
			b.WriteByte('\n')
		}
	}
	if finalEvent := finalEventFact(events); finalEvent != "" {
		writeFact(&b, "Final event", finalEvent)
	}
	return strings.TrimRight(b.String(), "\n")
}

func writeFact(b *strings.Builder, key string, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	fmt.Fprintf(b, "%s: %s\n", key, value)
}

func diffFact(snapshot RunSnapshot) string {
	if snapshot.FinalReview.ChangedFiles > 0 {
		return fmt.Sprintf("available, %d files changed", snapshot.FinalReview.ChangedFiles)
	}
	return "none recorded"
}

func availabilityFact(ok bool) string {
	if ok {
		return "available"
	}
	return "not available"
}

func submissionFact(submission string) string {
	submission = strings.TrimSpace(submission)
	if submission == "" {
		return "missing"
	}
	return shortString(submission, 400)
}

func errorsFact(snapshot RunSnapshot) string {
	var errors []string
	for _, artifact := range snapshot.Artifacts {
		if artifact.Kind == "error" && strings.TrimSpace(artifact.Body) != "" {
			errors = append(errors, shortString(artifact.Body, 200))
		}
	}
	if len(errors) == 0 {
		return "none"
	}
	return strings.Join(errors, "; ")
}

func importantStepFacts(steps []StepCard) []string {
	if len(steps) == 0 {
		return nil
	}
	start := max(0, len(steps)-6)
	facts := make([]string, 0, len(steps)-start)
	for _, step := range steps[start:] {
		label := strings.TrimSpace(step.Command)
		if label == "" {
			label = strings.TrimSpace(step.Tool)
		}
		if label == "" {
			label = "step"
		}
		facts = append(facts, fmt.Sprintf("%s: %s -> %s", step.Phase, shortString(label, 80), valueOrDefault(step.Outcome, "unknown")))
	}
	return facts
}

func finalEventFact(events []core.Event) string {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type != "final" {
			continue
		}
		status := strings.TrimSpace(fmt.Sprint(events[i].Data["status"]))
		steps := intValue(events[i].Data["steps"])
		if status == "" && steps == 0 {
			return ""
		}
		return fmt.Sprintf("status=%s steps=%d", valueOrDefault(status, "unknown"), steps)
	}
	return ""
}

func cleanNarrative(body string) string {
	body = strings.TrimSpace(body)
	body = strings.TrimPrefix(body, "```text")
	body = strings.TrimPrefix(body, "```markdown")
	body = strings.TrimPrefix(body, "```")
	body = strings.TrimSuffix(body, "```")
	body = strings.TrimSpace(body)
	lines := strings.Split(body, "\n")
	if len(lines) > 12 {
		lines = lines[:12]
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}
