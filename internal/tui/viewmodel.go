package tui

import (
	"fmt"
	"sort"
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

type ItemKind string

const (
	itemUser     ItemKind = "user"
	itemAgent    ItemKind = "agent"
	itemTool     ItemKind = "tool"
	itemFile     ItemKind = "file"
	itemTest     ItemKind = "test"
	itemApproval ItemKind = "approval"
	itemFinal    ItemKind = "final"
)

type ItemStatus string

const (
	itemRunning ItemStatus = "running"
	itemOK      ItemStatus = "ok"
	itemFailed  ItemStatus = "failed"
	itemWaiting ItemStatus = "waiting"
	itemSkipped ItemStatus = "skipped"
)

type RiskLevel string

const (
	riskNone   RiskLevel = ""
	riskLow    RiskLevel = "low"
	riskMedium RiskLevel = "medium"
	riskHigh   RiskLevel = "high"
)

type ArtifactRef struct {
	Kind  string
	Title string
}

type TimelineItem struct {
	ID        string
	Kind      ItemKind
	Title     string
	Summary   string
	Detail    string
	Status    ItemStatus
	Risk      RiskLevel
	StartedAt time.Time
	Duration  time.Duration
	Artifacts []ArtifactRef
	Collapsed bool
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
			tool := strings.TrimSpace(fmt.Sprint(event.Data["tool"]))
			command := commandFromArgs(event.Data["args"])
			step := StepCard{
				Index:    len(snapshot.Steps) + 1,
				Phase:    phaseForStep(tool, command),
				Tool:     tool,
				Command:  command,
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
				tool := strings.TrimSpace(fmt.Sprint(event.Data["tool"]))
				snapshot.Steps = append(snapshot.Steps, StepCard{
					Index:    len(snapshot.Steps) + 1,
					Phase:    phaseForStep(tool, ""),
					Tool:     tool,
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

func BuildTimeline(record taskRecord, snapshot RunSnapshot) []TimelineItem {
	items := make([]TimelineItem, 0, len(snapshot.Steps)+4)
	if text := strings.TrimSpace(record.Task.Text); text != "" {
		items = append(items, TimelineItem{
			ID:        fmt.Sprintf("task-%d", record.ID),
			Kind:      itemUser,
			Title:     "You",
			Summary:   text,
			Detail:    text,
			Status:    itemOK,
			StartedAt: record.StartedAt,
			Collapsed: true,
		})
	}

	for i, event := range record.Events {
		switch event.Type {
		case "model_response":
			content := stripFencedBlocks(strings.TrimSpace(fmt.Sprint(event.Data["content"])))
			if content == "" {
				continue
			}
			items = append(items, TimelineItem{
				ID:        fmt.Sprintf("event-%d", i),
				Kind:      itemAgent,
				Title:     "Agent",
				Summary:   shortString(content, 120),
				Detail:    content,
				Status:    itemOK,
				StartedAt: event.Time,
				Collapsed: true,
			})
		case "tool_denied":
			tool := strings.TrimSpace(fmt.Sprint(event.Data["tool"]))
			if tool == "" {
				tool = "tool"
			}
			items = append(items, TimelineItem{
				ID:        fmt.Sprintf("event-%d", i),
				Kind:      itemApproval,
				Title:     tool,
				Summary:   "denied",
				Detail:    strings.TrimSpace(fmt.Sprint(event.Data["reason"])),
				Status:    itemSkipped,
				Risk:      riskMedium,
				StartedAt: event.Time,
				Collapsed: true,
			})
		case "error":
			body := strings.TrimSpace(fmt.Sprint(event.Data["error"]))
			items = append(items, TimelineItem{
				ID:        fmt.Sprintf("event-%d", i),
				Kind:      itemFinal,
				Title:     "Error",
				Summary:   shortString(body, 120),
				Detail:    body,
				Status:    itemFailed,
				StartedAt: event.Time,
				Collapsed: true,
			})
		}
	}

	for _, step := range snapshot.Steps {
		kind := itemTool
		if step.Phase == "validate" {
			kind = itemTest
		} else if step.Phase == "edit" {
			kind = itemFile
		}
		items = append(items, TimelineItem{
			ID:        fmt.Sprintf("step-%d", step.Index),
			Kind:      kind,
			Title:     valueOrDefault(step.Tool, step.Phase),
			Summary:   timelineStepSummary(step),
			Detail:    stepDetailWidth(step, 0),
			Status:    timelineStatus(step.Outcome),
			Risk:      timelineRisk(step),
			StartedAt: step.Started,
			Duration:  step.Duration,
			Artifacts: timelineArtifacts(step),
			Collapsed: true,
		})
	}

	if snapshot.FinalReview.Status != "" && snapshot.FinalReview.Status != "running" {
		summary := valueOrDefault(snapshot.FinalReview.Submission, "Status: "+snapshot.FinalReview.Status)
		if body := strings.TrimSpace(record.Narrative.Body); body != "" {
			summary = body
		}
		items = append(items, TimelineItem{
			ID:      fmt.Sprintf("final-%d", record.ID),
			Kind:    itemFinal,
			Title:   "Review",
			Summary: summary,
			Detail:  summary,
			Status:  timelineFinalStatus(snapshot.FinalReview.Status),
			Artifacts: []ArtifactRef{
				{Kind: "diff", Title: fmt.Sprintf("%d files", snapshot.FinalReview.ChangedFiles)},
				{Kind: "tests", Title: validationSummary(snapshot)},
			},
			Collapsed: true,
		})
	}

	sort.SliceStable(items, func(i, j int) bool {
		left := items[i].StartedAt
		right := items[j].StartedAt
		if left.IsZero() || right.IsZero() {
			return i < j
		}
		return left.Before(right)
	})
	return items
}

func timelineStepSummary(step StepCard) string {
	label := strings.TrimSpace(step.Command)
	if label == "" {
		label = strings.TrimSpace(step.Tool)
	}
	if label == "" {
		label = strings.TrimSpace(step.Phase)
	}
	parts := []string{label}
	if outcome := strings.TrimSpace(step.Outcome); outcome != "" {
		parts = append(parts, outcome)
	}
	if step.Duration > 0 {
		parts = append(parts, step.Duration.String())
	}
	return strings.Join(parts, "  ")
}

func timelineStatus(outcome string) ItemStatus {
	outcome = strings.ToLower(strings.TrimSpace(outcome))
	switch {
	case outcome == "" || outcome == "running":
		return itemRunning
	case outcome == "ok":
		return itemOK
	case strings.HasPrefix(outcome, "exit ") || outcome == "timeout":
		return itemFailed
	case outcome == "denied":
		return itemSkipped
	default:
		return itemOK
	}
}

func timelineFinalStatus(status string) ItemStatus {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "error", "failed", "failure":
		return itemFailed
	case "running":
		return itemRunning
	default:
		return itemOK
	}
}

func timelineRisk(step StepCard) RiskLevel {
	switch step.Phase {
	case "edit":
		return riskMedium
	case "shell":
		return riskLow
	default:
		return riskNone
	}
}

func timelineArtifacts(step StepCard) []ArtifactRef {
	var artifacts []ArtifactRef
	if step.Output != "" {
		artifacts = append(artifacts, ArtifactRef{Kind: "output", Title: "output"})
	}
	if len(step.EventIDs) > 0 {
		artifacts = append(artifacts, ArtifactRef{Kind: "events", Title: formatEventIDs(step.EventIDs)})
	}
	return artifacts
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

func phaseForStep(tool string, command string) string {
	tool = strings.TrimSpace(tool)
	command = strings.TrimSpace(command)
	if tool == "run_tests" || looksLikeValidation(command) {
		return "validate"
	}
	if tool == "shell" && looksLikeEdit(command) {
		return "edit"
	}
	return phaseForTool(tool)
}

func looksLikeValidation(command string) bool {
	command = strings.ToLower(strings.TrimSpace(command))
	if command == "" {
		return false
	}
	patterns := []string{
		"go test",
		"npm test",
		"pnpm test",
		"yarn test",
		"pytest",
		"cargo test",
		"mvn test",
		"gradle test",
		"make test",
		"go vet",
		"golangci-lint",
		"ruff",
		"eslint",
	}
	for _, pattern := range patterns {
		if strings.Contains(command, pattern) {
			return true
		}
	}
	return false
}

func looksLikeEdit(command string) bool {
	command = strings.ToLower(strings.TrimSpace(command))
	if command == "" {
		return false
	}
	patterns := []string{"apply_patch", "sed -i", "perl -pi", "go fmt", "gofmt", "npm run format"}
	for _, pattern := range patterns {
		if strings.Contains(command, pattern) {
			return true
		}
	}
	return false
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
