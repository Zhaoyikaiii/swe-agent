package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/local/swe-agent/internal/core"
)

type RunSnapshot struct {
	Task        core.Task
	Status      string
	Steps       []StepCard
	Artifacts   []ArtifactCard
	FinalReview FinalReview
}

type StepCard struct {
	Index    int
	Phase    string
	Tool     string
	Command  string
	Outcome  string
	Risk     string
	Started  time.Time
	Duration time.Duration
	Why      string
	Action   string
	Output   string
	EventIDs []int
}

type ArtifactCard struct {
	Kind  string
	Title string
	Body  string
}

type FinalReview struct {
	Status       string
	Steps        int
	ChangedFiles int
	TestsRun     int
	TestsPassed  int
	Trajectory   string
	Submission   string
}

func BuildRunSnapshot(record taskRecord, trajectoryPath string) RunSnapshot {
	snapshot := RunSnapshot{
		Task:   record.Task,
		Status: valueOrDefault(record.Status, "pending"),
	}
	var pendingWhy string
	var pendingWhyEvent int
	for i, event := range record.Events {
		switch event.Type {
		case "model_response":
			pendingWhy = stripFencedBlocks(strings.TrimSpace(fmt.Sprint(event.Data["content"])))
			pendingWhyEvent = i
		case "tool_call":
			step := StepCard{
				Index:    len(snapshot.Steps) + 1,
				Phase:    phaseForTool(event.Data["tool"]),
				Tool:     strings.TrimSpace(fmt.Sprint(event.Data["tool"])),
				Command:  commandFromArgs(event.Data["args"]),
				Outcome:  "running",
				Started:  event.Time,
				Why:      pendingWhy,
				Action:   formatToolAction(event),
				EventIDs: []int{i},
			}
			if pendingWhyEvent >= 0 && pendingWhy != "" {
				step.EventIDs = append([]int{pendingWhyEvent}, step.EventIDs...)
			}
			snapshot.Steps = append(snapshot.Steps, step)
			pendingWhy = ""
			pendingWhyEvent = -1
		case "tool_result":
			stepIndex := findOpenStep(snapshot.Steps, fmt.Sprint(event.Data["tool"]))
			if stepIndex < 0 {
				stepIndex = len(snapshot.Steps)
				snapshot.Steps = append(snapshot.Steps, StepCard{
					Index:    len(snapshot.Steps) + 1,
					Phase:    phaseForTool(event.Data["tool"]),
					Tool:     strings.TrimSpace(fmt.Sprint(event.Data["tool"])),
					Outcome:  "ok",
					EventIDs: []int{},
				})
			}
			step := &snapshot.Steps[stepIndex]
			step.EventIDs = append(step.EventIDs, i)
			step.Outcome = outcomeFromResult(event)
			step.Output = strings.TrimSpace(fmt.Sprint(event.Data["output"]))
			if !step.Started.IsZero() && !event.Time.IsZero() && event.Time.After(step.Started) {
				step.Duration = event.Time.Sub(step.Started).Round(time.Second)
			}
		case "tool_denied":
			snapshot.Steps = append(snapshot.Steps, StepCard{
				Index:    len(snapshot.Steps) + 1,
				Phase:    phaseForTool(event.Data["tool"]),
				Tool:     strings.TrimSpace(fmt.Sprint(event.Data["tool"])),
				Outcome:  "denied",
				Output:   strings.TrimSpace(fmt.Sprint(event.Data["reason"])),
				EventIDs: []int{i},
			})
		case "error":
			snapshot.Artifacts = append(snapshot.Artifacts, ArtifactCard{
				Kind:  "error",
				Title: "Error",
				Body:  strings.TrimSpace(fmt.Sprint(event.Data["error"])),
			})
		}
	}

	if record.Result.Diff != "" {
		snapshot.Artifacts = append(snapshot.Artifacts, ArtifactCard{
			Kind:  "diff",
			Title: "Workspace Diff",
			Body:  record.Result.Diff,
		})
	}
	if trajectoryPath != "" {
		snapshot.Artifacts = append(snapshot.Artifacts, ArtifactCard{
			Kind:  "trace",
			Title: "Trajectory",
			Body:  trajectoryPath,
		})
	}

	snapshot.FinalReview = FinalReview{
		Status:       valueOrDefault(record.Result.Status, snapshot.Status),
		Steps:        finalStepCount(record, len(snapshot.Steps)),
		ChangedFiles: countChangedFiles(record.Result.Diff),
		TestsRun:     countValidationSteps(snapshot.Steps),
		TestsPassed:  countPassedValidationSteps(snapshot.Steps),
		Trajectory:   trajectoryPath,
		Submission:   taskConclusion(record),
	}
	return snapshot
}

func findOpenStep(steps []StepCard, tool string) int {
	tool = strings.TrimSpace(tool)
	for i := len(steps) - 1; i >= 0; i-- {
		if steps[i].Outcome == "running" && strings.TrimSpace(steps[i].Tool) == tool {
			return i
		}
	}
	return -1
}

func phaseForTool(tool any) string {
	switch strings.TrimSpace(fmt.Sprint(tool)) {
	case "read_file", "list_files", "grep":
		return "search"
	case "apply_patch":
		return "edit"
	case "run_tests":
		return "validate"
	case "git_diff":
		return "review"
	case "submit":
		return "submit"
	case "shell":
		return "shell"
	default:
		return "tool"
	}
}

func commandFromArgs(args any) string {
	normalized := normalizeValue(args)
	if m, ok := normalized.(map[string]any); ok {
		for _, key := range []string{"command", "path", "pattern", "submission"} {
			if value := strings.TrimSpace(fmt.Sprint(m[key])); value != "" && value != "<nil>" {
				return value
			}
		}
	}
	return ""
}

func formatToolAction(event core.Event) string {
	tool := strings.TrimSpace(fmt.Sprint(event.Data["tool"]))
	args := strings.TrimSpace(formatArgsMap(event.Data["args"]))
	if args == "" || args == "empty" {
		return tool
	}
	return tool + "\n\n" + args
}

func outcomeFromResult(event core.Event) string {
	if timedOut, ok := normalizeValue(event.Data["timed_out"]).(bool); ok && timedOut {
		return "timeout"
	}
	code := intValue(event.Data["code"])
	if code != 0 {
		return fmt.Sprintf("exit %d", code)
	}
	return "ok"
}

func finalStepCount(record taskRecord, fallback int) int {
	if record.Result.Steps > 0 {
		return record.Result.Steps
	}
	if steps := intValue(lastEventData(record.Events, "final", "steps")); steps > 0 {
		return steps
	}
	return fallback
}

func countValidationSteps(steps []StepCard) int {
	total := 0
	for _, step := range steps {
		if step.Phase == "validate" || step.Tool == "run_tests" {
			total++
		}
	}
	return total
}

func countPassedValidationSteps(steps []StepCard) int {
	total := 0
	for _, step := range steps {
		if (step.Phase == "validate" || step.Tool == "run_tests") && step.Outcome == "ok" {
			total++
		}
	}
	return total
}

func countChangedFiles(diff string) int {
	files := map[string]struct{}{}
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "diff --git ") {
			parts := strings.Fields(line)
			if len(parts) >= 4 {
				files[strings.TrimPrefix(parts[3], "b/")] = struct{}{}
			}
		}
	}
	return len(files)
}
