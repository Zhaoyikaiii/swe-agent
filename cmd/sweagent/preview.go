package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/local/swe-agent/internal/core"
	"github.com/local/swe-agent/internal/problemtrace"
	"github.com/local/swe-agent/internal/trajectory"
	"github.com/local/swe-agent/internal/tui"
)

func previewCommand(args []string) error {
	fs := flag.NewFlagSet("preview", flag.ContinueOnError)
	tracePath := fs.String("trace", "", "path to trajectory JSONL")
	repo := fs.String("repo", "", "repository/workspace label for preview fallback")
	render := fs.Bool("render", false, "render a static preview to stdout instead of opening the TUI")
	width := fs.Int("width", 140, "static render width")
	height := fs.Int("height", 30, "static render height")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *tracePath == "" {
		return errors.New("--trace is required")
	}

	events, err := trajectory.LoadFile(context.Background(), *tracePath)
	if err != nil {
		return err
	}
	if *render {
		fmt.Print(tui.RenderTracePreview(events, *tracePath, *repo, *width, *height))
		return nil
	}
	return tui.NewSession().PreviewTrace(context.Background(), events, *tracePath, *repo)
}

func previewFixtureCommand(args []string) error {
	fs := flag.NewFlagSet("preview-fixture", flag.ContinueOnError)
	output := fs.String("output", "", "output JSONL path; stdout when empty or '-'")
	if err := fs.Parse(args); err != nil {
		return err
	}

	events := sampleTracePreviewEvents()
	if *output == "" || *output == "-" {
		return writeJSONLEvents(os.Stdout, events)
	}
	if err := os.MkdirAll(filepath.Dir(*output), 0o755); err != nil {
		return err
	}
	file, err := os.Create(*output)
	if err != nil {
		return err
	}
	defer file.Close()
	return writeJSONLEvents(file, events)
}

func writeJSONLEvents(w io.Writer, events []core.Event) error {
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	for _, event := range events {
		if err := encoder.Encode(event); err != nil {
			return err
		}
	}
	return nil
}

func sampleTracePreviewEvents() []core.Event {
	base := time.Date(2026, 6, 26, 1, 0, 0, 0, time.UTC)
	at := func(offset int) time.Time {
		return base.Add(time.Duration(offset) * time.Second)
	}
	traceID := "trace-preview"
	directionID := "dir-review-comments"
	promptID := "prompt-preview-1"
	traceContext := problemtrace.TraceContext{
		TraceID:          traceID,
		SpanID:           "span-tool-gh-api",
		DirectionID:      directionID,
		PromptSnapshotID: promptID,
		Flags:            problemtrace.TraceFlags{Recording: true, Sampled: true},
	}

	return []core.Event{
		{
			Type: "user_task",
			Time: at(0),
			Data: map[string]any{
				"task": "Fix unresolved PR review comments and verify the change",
				"repo": "/workspace/example",
			},
		},
		{
			Type: "problem_trace_initialized",
			Time: at(1),
			Data: map[string]any{
				"trace_id": traceID,
				"problem": problemtrace.ProblemContext{
					UserTask:     "Fix unresolved PR review comments and verify the change",
					Repo:         "/workspace/example",
					ErrorSummary: "PR has unresolved review threads and failing validation is unknown.",
					ReproCommands: []string{
						"gh pr view --json reviewDecision",
						"go test ./...",
					},
				},
			},
		},
		{
			Type: "symptom_detected",
			Time: at(2),
			Data: map[string]any{
				"symptom": problemtrace.Symptom{
					ID:         "symptom-unresolved-review",
					Kind:       "review",
					Summary:    "Unresolved PR review comments require code changes before submit.",
					RawExcerpt: "reviewDecision=CHANGES_REQUESTED",
					ErrorType:  "review_threads",
					Command:    "gh pr view --json reviewDecision",
					CreatedAt:  at(2),
				},
			},
		},
		{
			Type: "direction_created",
			Time: at(3),
			Data: map[string]any{
				"direction": problemtrace.InvestigationDirection{
					ID:         directionID,
					Hypothesis: "Resolve reviewer feedback before final validation",
					Rationale:  "The safest next step is to inspect unresolved threads, patch the exact issue, then rerun the targeted test.",
					Status:     problemtrace.DirectionSupported,
					Priority:   95,
					ExpectedEvidence: []string{
						"A list of unresolved review threads",
						"A patch that addresses the selected thread",
						"A successful targeted validation command",
					},
					NextActions: []problemtrace.NextAction{{
						ID:          "next-read-review-threads",
						Action:      "List unresolved review threads through GitHub GraphQL.",
						Tool:        "shell",
						Command:     "gh api graphql -f query=@reviewThreads.graphql",
						DirectionID: directionID,
						Priority:    100,
					}},
				},
			},
		},
		{
			Type: "prompt_snapshot",
			Time: at(4),
			Data: map[string]any{
				"snapshot": problemtrace.PromptSnapshot{
					ID:            promptID,
					Step:          1,
					Model:         "codex-cli",
					MessageCount:  6,
					ToolCount:     3,
					TokenEstimate: 1840,
					DirectionIDs:  []string{directionID},
					CreatedAt:     at(4),
					MemoryIDs:     []string{"mem-review-thread-workflow"},
					InputTokens:   1730,
					Blocks: []problemtrace.PromptBlock{{
						Kind:     "frontier",
						Title:    "Investigation Frontier",
						Included: true,
						Summary:  "Inspect unresolved threads before editing.",
					}},
				},
			},
		},
		{
			Type: "model_response",
			Time: at(5),
			Data: map[string]any{
				"content":       "I will inspect unresolved review threads first, then patch only the referenced code path.",
				"trace_context": traceContext,
			},
		},
		{
			Type: "tool_call",
			Time: at(6),
			Data: map[string]any{
				"tool":          "shell",
				"args":          map[string]any{"command": "gh api graphql -f query=@reviewThreads.graphql"},
				"trace_context": traceContext,
			},
		},
		{
			Type: "tool_result",
			Time: at(7),
			Data: map[string]any{
				"tool":          "shell",
				"code":          0,
				"output":        "thread #12 file=internal/tui/trace_tree.go line=411\ncomment=Selected detail should not dump long tool output in the overview panel.",
				"trace_context": traceContext,
			},
		},
		{
			Type: "evidence_added",
			Time: at(8),
			Data: map[string]any{
				"direction_id": directionID,
				"evidence": problemtrace.Evidence{
					ID:        "evidence-review-thread-12",
					Summary:   "Reviewer asked to move long output out of Overview.",
					Detail:    "thread #12 points at trace_tree.go and recommends a dedicated output panel.",
					Relation:  problemtrace.EvidenceSupports,
					Source:    "github-review-thread",
					EventIDs:  []int{7},
					CreatedAt: at(8),
				},
			},
		},
		{
			Type: "tool_call",
			Time: at(9),
			Data: map[string]any{
				"tool": "apply_patch",
				"args": map[string]any{"path": "internal/tui/trace_tree.go"},
				"trace_context": problemtrace.TraceContext{
					TraceID:          traceID,
					SpanID:           "span-apply-patch",
					ParentSpanID:     "span-tool-gh-api",
					DirectionID:      directionID,
					PromptSnapshotID: promptID,
					Flags:            problemtrace.TraceFlags{Recording: true, Sampled: true},
				},
			},
		},
		{
			Type: "tool_result",
			Time: at(10),
			Data: map[string]any{
				"tool":   "apply_patch",
				"code":   0,
				"output": "added renderTraceDetailOutput and kept Overview semantic-only",
			},
		},
		{
			Type: "frontier_updated",
			Time: at(11),
			Data: map[string]any{
				"frontier": problemtrace.InvestigationFrontier{
					ActiveDirectionID:   directionID,
					CandidateDirections: []string{directionID},
					OpenQuestions:       []string{"Does the targeted TUI test cover detail tab switching?"},
					RecommendedActions: []problemtrace.NextAction{{
						ID:          "next-run-tests",
						Action:      "Run the targeted Trace workspace tests, then run go test ./...",
						Tool:        "shell",
						Command:     "go test ./internal/tui -run TraceWorkspace",
						DirectionID: directionID,
						Priority:    90,
					}},
				},
			},
		},
		{
			Type: "memory_card_generated",
			Time: at(12),
			Data: map[string]any{
				"card": problemtrace.MemoryCard{
					ID:           "card-trace-detail-tabs",
					Kind:         "ui-pattern",
					Summary:      "Keep Trace overview semantic and move long output into a dedicated Output tab.",
					ProblemSig:   "Trace tree and selected details compete in one text column.",
					Evidence:     []string{"reviewer asked for OTel-like split layout", "targeted tests cover pane and tab keys"},
					FixPattern:   "Use split panes plus Overview/Output/Events/Debug detail tabs.",
					Verification: "go test ./...",
					Tags:         []string{"tui", "trace", "preview"},
					Status:       "draft",
					CreatedAt:    at(12),
				},
			},
		},
		{
			Type: "final",
			Time: at(13),
			Data: map[string]any{
				"status":     "submitted",
				"steps":      4,
				"submission": "Implemented Trace split layout with dedicated detail tabs and verified with go test ./...",
			},
		},
	}
}
