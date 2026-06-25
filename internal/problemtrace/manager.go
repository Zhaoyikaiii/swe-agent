package problemtrace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/local/swe-agent/internal/core"
)

const excerptLimit = 1600

type Manager struct {
	mu sync.Mutex

	trace ProblemTrace

	spanSeq      int
	symptomSeq   int
	directionSeq int
	evidenceSeq  int
	promptSeq    int
	cardSeq      int

	runSpanID       string
	lastModelSpanID string
	patchApplied    bool
	verificationOK  bool
}

type PromptInput struct {
	Step        int
	Model       string
	Provider    string
	Messages    []core.Message
	Tools       []core.ToolSpec
	Temperature float64
	MaxTokens   int
	WorkingDir  string
}

type PromptResult struct {
	Messages []core.Message
	Snapshot PromptSnapshot
	Context  TraceContext
	Events   []core.Event
}

func NewManager() *Manager {
	return &Manager{}
}

func (m *Manager) StartRun(ctx context.Context, task core.Task, resource TraceResource) []core.Event {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	traceID := newID("trace", now, task.Text+"|"+task.Repo)
	m.runSpanID = "span-1"
	m.spanSeq = 1
	m.trace = ProblemTrace{
		RunID:   traceID,
		TraceID: traceID,
		Problem: ProblemContext{
			UserTask: task.Text,
			Repo:     task.Repo,
			Constraints: []string{
				"Treat memory as hypotheses, not current facts.",
				"Verify evidence in the current repository before patching.",
				"Do not save hidden reasoning, secrets, or unbounded stdout.",
			},
		},
		Resource:  resource,
		CreatedAt: now,
		UpdatedAt: now,
	}
	runSpan := TraceSpan{
		TraceID:   traceID,
		SpanID:    m.runSpanID,
		Name:      SpanProblemRun,
		Kind:      "run",
		StartTime: now,
		Status:    SpanStatusUnset,
		Resource:  resource,
		Attributes: map[string]any{
			AttrRepoPath:      task.Repo,
			AttrRepoLanguage:  resource.RepoLanguage,
			AttrModelProvider: resource.ModelProvider,
			AttrModelName:     resource.Model,
		},
	}
	m.trace.Spans = append(m.trace.Spans, runSpan)
	m.trace.History = append(m.trace.History, TraceNode{
		ID:      "node-root",
		Kind:    "problem",
		Title:   "Problem",
		Summary: short(task.Text, 240),
		Status:  "running",
		Time:    now,
	})
	m.updateFrontierLocked()
	return []core.Event{
		m.eventLocked("problem_trace_initialized", map[string]any{
			"trace_id": traceID,
			"context":  m.contextLocked(m.runSpanID),
			"problem":  m.trace.Problem,
			"resource": resource,
		}),
		m.eventLocked("trace_span_started", map[string]any{
			"trace_context": m.contextLocked(m.runSpanID),
			"span":          runSpan,
		}),
		m.eventLocked("frontier_updated", map[string]any{
			"trace_context": m.contextLocked(m.runSpanID),
			"frontier":      m.trace.Frontier,
		}),
	}
}

func (m *Manager) BuildPrompt(ctx context.Context, input PromptInput) PromptResult {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.trace.TraceID == "" {
		m.startRunLocked(core.Task{Text: firstUserTask(input.Messages), Repo: input.WorkingDir}, TraceResource{
			RepoPath:      input.WorkingDir,
			ModelProvider: input.Provider,
			Model:         input.Model,
		})
	}

	span := m.startSpanLocked(SpanPromptBuild, "prompt", m.runSpanID, map[string]any{
		AttrAgentStepIndex: input.Step,
		AttrModelProvider:  input.Provider,
		AttrModelName:      input.Model,
	})
	promptID := m.nextPromptIDLocked()
	blocks := m.promptBlocksLocked(input)
	messages := append([]core.Message(nil), input.Messages...)
	contextBlock := m.renderPromptContextLocked()
	if strings.TrimSpace(contextBlock) != "" {
		messages = append(messages, core.Message{Role: core.RoleSystem, Content: contextBlock, Extra: map[string]string{
			"problem_trace": "frontier",
		}})
	}
	snapshot := PromptSnapshot{
		ID:            promptID,
		Step:          input.Step,
		Model:         input.Model,
		Blocks:        blocks,
		MessageCount:  len(messages),
		ToolCount:     len(input.Tools),
		TokenEstimate: estimateTokens(messages, input.Tools),
		DirectionIDs:  currentDirectionIDs(m.trace),
		MemoryIDs:     currentMemoryIDs(m.trace),
		CreatedAt:     time.Now(),
	}
	m.trace.Prompts = append(m.trace.Prompts, snapshot)
	m.trace.History = append(m.trace.History, TraceNode{
		ID:       "node-" + promptID,
		ParentID: "node-root",
		Kind:     "prompt",
		Title:    fmt.Sprintf("Prompt snapshot %d", input.Step),
		Summary:  fmt.Sprintf("%d messages, %d tools", snapshot.MessageCount, snapshot.ToolCount),
		Status:   "ok",
		PromptID: promptID,
		Time:     snapshot.CreatedAt,
	})
	span.Attributes[AttrPromptSnapshotID] = promptID
	span.Attributes["prompt.message_count"] = snapshot.MessageCount
	span.Attributes["prompt.tool_count"] = snapshot.ToolCount
	span.Attributes["prompt.token_estimate"] = snapshot.TokenEstimate
	span.Status = SpanStatusOK
	span.EndTime = time.Now()
	m.upsertSpanLocked(span)
	m.trace.UpdatedAt = time.Now()
	traceContext := m.contextLocked(span.SpanID)
	traceContext.PromptSnapshotID = promptID

	return PromptResult{
		Messages: messages,
		Snapshot: snapshot,
		Context:  traceContext,
		Events: []core.Event{
			m.eventLocked("trace_span_started", map[string]any{
				"trace_context": m.contextLocked(span.SpanID),
				"span":          span,
			}),
			m.eventLocked("prompt_snapshot", map[string]any{
				"trace_context": traceContext,
				"snapshot":      snapshot,
			}),
			m.eventLocked("trace_span_ended", map[string]any{
				"trace_context": m.contextLocked(span.SpanID),
				"span":          span,
			}),
		},
	}
}

func (m *Manager) StartModelCall(ctx context.Context, step int, promptID string, provider string, model string) (TraceContext, []core.Event) {
	m.mu.Lock()
	defer m.mu.Unlock()

	span := m.startSpanLocked(SpanModelCall, "model", m.runSpanID, map[string]any{
		AttrAgentStepIndex:   step,
		AttrModelProvider:    provider,
		AttrModelName:        model,
		AttrPromptSnapshotID: promptID,
		AttrDirectionID:      m.trace.Frontier.ActiveDirectionID,
	})
	span.Links = append(span.Links, TraceLink{
		FromID: span.SpanID,
		ToID:   promptID,
		Kind:   LinkCaused,
		Attributes: map[string]any{
			AttrPromptSnapshotID: promptID,
		},
	})
	m.upsertSpanLocked(span)
	m.lastModelSpanID = span.SpanID
	traceContext := m.contextLocked(span.SpanID)
	traceContext.PromptSnapshotID = promptID
	return traceContext, []core.Event{m.eventLocked("trace_span_started", map[string]any{
		"trace_context": traceContext,
		"span":          span,
	})}
}

func (m *Manager) EndModelCall(ctx context.Context, tc TraceContext, resp core.ModelResponse, err error) []core.Event {
	m.mu.Lock()
	defer m.mu.Unlock()

	span := m.spanByIDLocked(tc.SpanID)
	if span == nil {
		return nil
	}
	if err != nil {
		span.Status = SpanStatusError
		span.Events = append(span.Events, TraceEvent{Name: "model.error", Time: time.Now(), Body: short(err.Error(), 600)})
	} else {
		span.Status = SpanStatusOK
		span.Attributes[AttrModelInputTokens] = resp.Usage.InputTokens
		span.Attributes[AttrModelOutputTokens] = resp.Usage.OutputTokens
		span.Attributes["model.finish_reason"] = resp.FinishReason
		span.Events = append(span.Events, TraceEvent{Name: "model.response", Time: time.Now(), Body: short(stripFenced(resp.Message.Content), 800)})
	}
	span.EndTime = time.Now()
	m.trace.UpdatedAt = time.Now()
	return []core.Event{m.eventLocked("trace_span_ended", map[string]any{
		"trace_context": tc,
		"span":          *span,
	})}
}

func (m *Manager) StartToolCall(ctx context.Context, step int, call core.ToolCall) (TraceContext, []core.Event) {
	m.mu.Lock()
	defer m.mu.Unlock()

	parent := m.lastModelSpanID
	if parent == "" {
		parent = m.runSpanID
	}
	kind := toolSpanKind(call.Name)
	span := m.startSpanLocked(kind, "tool", parent, map[string]any{
		AttrAgentStepIndex: step,
		AttrToolName:       call.Name,
		AttrToolArgsHash:   hashAny(call.Args),
		AttrDirectionID:    m.trace.Frontier.ActiveDirectionID,
	})
	if cmd := commandFromToolCall(call); cmd != "" {
		span.Attributes[AttrTestCommand] = cmd
	}
	m.upsertSpanLocked(span)
	tc := m.contextLocked(span.SpanID)
	return tc, []core.Event{m.eventLocked("trace_span_started", map[string]any{
		"trace_context": tc,
		"span":          span,
	})}
}

func (m *Manager) ObserveToolResult(ctx context.Context, tc TraceContext, call core.ToolCall, result core.ToolResult) []core.Event {
	m.mu.Lock()
	defer m.mu.Unlock()

	var events []core.Event
	span := m.spanByIDLocked(tc.SpanID)
	if span != nil {
		span.EndTime = time.Now()
		span.Attributes[AttrToolExitCode] = result.Code
		span.Attributes[AttrToolTimedOut] = result.TimedOut
		span.Events = append(span.Events, TraceEvent{
			Name: "tool.result",
			Time: time.Now(),
			Attributes: map[string]any{
				AttrToolExitCode: result.Code,
				AttrToolTimedOut: result.TimedOut,
				"output.hash":    hashString(result.Output),
				"output.chars":   len([]rune(result.Output)),
			},
			Body: short(result.Output, 800),
		})
		switch {
		case result.TimedOut:
			span.Status = SpanStatusTimeout
		case result.Code != 0:
			span.Status = SpanStatusError
		default:
			span.Status = SpanStatusOK
		}
		events = append(events, m.eventLocked("trace_span_ended", map[string]any{
			"trace_context": tc,
			"span":          *span,
		}))
	}

	changes := m.applyToolObservationLocked(tc, call, result)
	for _, symptom := range changes.Symptoms {
		events = append(events, m.eventLocked("symptom_detected", map[string]any{
			"trace_context": tc,
			"symptom":       symptom,
		}))
	}
	for _, direction := range changes.Directions {
		events = append(events, m.eventLocked("direction_created", map[string]any{
			"trace_context": tc,
			"direction":     direction,
		}))
	}
	for _, item := range changes.Evidence {
		events = append(events, m.eventLocked("evidence_added", map[string]any{
			"trace_context": tc,
			"direction_id":  item.DirectionID,
			"evidence":      item.Evidence,
		}))
	}
	if changes.FrontierUpdated {
		events = append(events, m.eventLocked("frontier_updated", map[string]any{
			"trace_context": tc,
			"frontier":      m.trace.Frontier,
		}))
	}
	return events
}

func (m *Manager) FinishRun(ctx context.Context, status string, submission string) []core.Event {
	m.mu.Lock()
	defer m.mu.Unlock()

	var events []core.Event
	for _, card := range m.generateCardsLocked(status, submission) {
		events = append(events, m.eventLocked("memory_card_generated", map[string]any{
			"trace_context": m.contextLocked(m.runSpanID),
			"card":          card,
		}))
	}
	if span := m.spanByIDLocked(m.runSpanID); span != nil {
		span.EndTime = time.Now()
		if strings.EqualFold(status, "error") {
			span.Status = SpanStatusError
		} else {
			span.Status = SpanStatusOK
		}
		span.Attributes["problem.status"] = status
		events = append(events, m.eventLocked("trace_span_ended", map[string]any{
			"trace_context": m.contextLocked(m.runSpanID),
			"span":          *span,
		}))
	}
	m.trace.UpdatedAt = time.Now()
	return events
}

func (m *Manager) Snapshot() ProblemTrace {
	m.mu.Lock()
	defer m.mu.Unlock()
	return cloneTrace(m.trace)
}

func (m *Manager) startRunLocked(task core.Task, resource TraceResource) {
	if m.trace.TraceID != "" {
		return
	}
	now := time.Now()
	traceID := newID("trace", now, task.Text+"|"+task.Repo)
	m.runSpanID = "span-1"
	m.spanSeq = 1
	m.trace = ProblemTrace{
		RunID:   traceID,
		TraceID: traceID,
		Problem: ProblemContext{
			UserTask: task.Text,
			Repo:     task.Repo,
		},
		Resource:  resource,
		CreatedAt: now,
		UpdatedAt: now,
	}
	m.trace.Spans = append(m.trace.Spans, TraceSpan{
		TraceID:   traceID,
		SpanID:    m.runSpanID,
		Name:      SpanProblemRun,
		Kind:      "run",
		StartTime: now,
		Status:    SpanStatusUnset,
		Resource:  resource,
	})
	m.updateFrontierLocked()
}

func (m *Manager) applyToolObservationLocked(tc TraceContext, call core.ToolCall, result core.ToolResult) ChangeSet {
	var changes ChangeSet
	command := commandFromToolCall(call)
	output := result.Output

	if call.Name == "apply_patch" && result.Code == 0 {
		m.patchApplied = true
		directionID := m.ensureGenericDirectionLocked("Apply and review the candidate fix", "A patch was applied and now needs verification.", 60)
		ev := m.addEvidenceLocked(directionID, "Patch applied successfully", "apply_patch exited successfully", "tool_result", []int{})
		changes.Evidence = append(changes.Evidence, DirectionEvidence{DirectionID: directionID, Evidence: ev})
	}

	symptoms := detectSymptoms(call, result)
	for _, symptom := range symptoms {
		m.symptomSeq++
		symptom.ID = fmt.Sprintf("symptom-%d", m.symptomSeq)
		symptom.Command = valueOr(symptom.Command, command)
		symptom.RawExcerpt = short(symptom.RawExcerpt, excerptLimit)
		symptom.CreatedAt = time.Now()
		m.trace.Symptoms = append(m.trace.Symptoms, symptom)
		m.trace.Problem.ErrorSummary = valueOr(m.trace.Problem.ErrorSummary, symptom.Summary)
		if command != "" && !contains(m.trace.Problem.ReproCommands, command) && isValidationCommand(call.Name, command) {
			m.trace.Problem.ReproCommands = append(m.trace.Problem.ReproCommands, command)
		}
		changes.Symptoms = append(changes.Symptoms, symptom)

		direction := m.directionForSymptomLocked(symptom)
		if direction != nil {
			changes.Directions = append(changes.Directions, *direction)
			ev := m.addEvidenceLocked(direction.ID, symptom.Summary, symptom.RawExcerpt, "tool_result", symptom.EventIDs)
			changes.Evidence = append(changes.Evidence, DirectionEvidence{DirectionID: direction.ID, Evidence: ev})
			if spanID := tc.SpanID; spanID != "" {
				m.linkLocked(spanID, direction.ID, LinkSupports, map[string]any{
					AttrDirectionID:    direction.ID,
					AttrErrorSignature: symptom.ErrorType,
				})
			}
		}
	}

	if len(symptoms) == 0 && isValidationCommand(call.Name, command) && result.Code == 0 {
		m.verificationOK = true
		if m.trace.Frontier.ActiveDirectionID != "" {
			idx := m.directionIndexLocked(m.trace.Frontier.ActiveDirectionID)
			if idx >= 0 {
				m.trace.Directions[idx].Status = DirectionFixed
				ev := m.addEvidenceLocked(m.trace.Directions[idx].ID, "Verification passed", valueOr(command, call.Name)+" exited successfully", "tool_result", nil)
				changes.Evidence = append(changes.Evidence, DirectionEvidence{DirectionID: m.trace.Directions[idx].ID, Evidence: ev})
			}
		}
	}

	if len(symptoms) == 0 && call.Name != "submit" && strings.TrimSpace(output) != "" {
		directionID := m.trace.Frontier.ActiveDirectionID
		if directionID == "" {
			directionID = m.ensureGenericDirectionLocked("Collect current repository evidence", "A tool observation was captured and should be interpreted before patching.", 40)
		}
		ev := m.addEvidenceLocked(directionID, fmt.Sprintf("%s observation captured", call.Name), short(output, 360), "tool_result", nil)
		changes.Evidence = append(changes.Evidence, DirectionEvidence{DirectionID: directionID, Evidence: ev})
	}

	before := hashAny(m.trace.Frontier)
	m.updateFrontierLocked()
	changes.FrontierUpdated = before != hashAny(m.trace.Frontier)
	m.trace.UpdatedAt = time.Now()
	return changes
}

func (m *Manager) directionForSymptomLocked(symptom Symptom) *InvestigationDirection {
	switch symptom.ErrorType {
	case "go_import_cycle":
		return m.ensureDirectionLocked("dir-go-import-cycle", "Resolve the Go import cycle", "The command reported `import cycle not allowed`; the next useful evidence is the exact package cycle and the shared abstraction that closes it.", 100, []NextAction{
			{
				ID:        "next-go-import-cycle",
				Action:    "Inspect the reported import chain and grep the involved packages' imports.",
				Tool:      "grep",
				Rationale: "The fix should target the concrete dependency edge that closes the cycle.",
				ExpectedEvidence: []string{
					"Package A imports package B and package B imports package A, directly or through tests/build tags.",
					"A neutral package or dependency inversion point is visible after confirming the cycle.",
				},
				Priority: 100,
			},
		})
	case "panic":
		return m.ensureDirectionLocked("dir-runtime-panic", "Localize the runtime panic", "The output contains a panic; inspect the stack frame and reproduce with the narrowest command.", 90, []NextAction{
			{ID: "next-runtime-panic", Action: "Inspect the panic stack trace and open the first repository frame.", Tool: "read_file", Priority: 90},
		})
	case "go_undefined", "compile_error":
		return m.ensureDirectionLocked("dir-compile-error", "Fix the compile error at the referenced symbol or package", "Compilation failed; the next step is to inspect the referenced files and symbols rather than editing broadly.", 85, []NextAction{
			{ID: "next-compile-error", Action: "Open the files and symbols named in the compiler output.", Tool: "read_file", Priority: 85},
		})
	case "lint_error":
		return m.ensureDirectionLocked("dir-lint-error", "Resolve the lint finding without unrelated rewrites", "A lint command failed and should be handled at the exact reported location.", 75, []NextAction{
			{ID: "next-lint-error", Action: "Inspect the lint output locations and apply the smallest local fix.", Tool: "read_file", Priority: 75},
		})
	default:
		return m.ensureDirectionLocked("dir-test-failure", "Classify and reproduce the failing validation", "A validation command failed; classify the error signature before modifying code.", 70, []NextAction{
			{ID: "next-test-failure", Action: "Read the failure output, identify the failing package/test/file, then inspect only that area.", Tool: "read_file", Priority: 70},
		})
	}
}

func (m *Manager) ensureDirectionLocked(id, hypothesis, rationale string, priority int, actions []NextAction) *InvestigationDirection {
	if idx := m.directionIndexLocked(id); idx >= 0 {
		direction := &m.trace.Directions[idx]
		if direction.Status == DirectionOpen {
			direction.Status = DirectionActive
		}
		return nil
	}
	direction := InvestigationDirection{
		ID:               id,
		Hypothesis:       hypothesis,
		Rationale:        rationale,
		Status:           DirectionActive,
		Priority:         priority,
		NextActions:      actions,
		ExpectedEvidence: expectedEvidenceForActions(actions),
	}
	for i := range direction.NextActions {
		direction.NextActions[i].DirectionID = id
	}
	if m.trace.Frontier.ActiveDirectionID != "" {
		if idx := m.directionIndexLocked(m.trace.Frontier.ActiveDirectionID); idx >= 0 && m.trace.Directions[idx].Status == DirectionActive {
			m.trace.Directions[idx].Status = DirectionSupported
		}
	}
	m.trace.Directions = append(m.trace.Directions, direction)
	m.trace.Frontier.ActiveDirectionID = id
	m.trace.History = append(m.trace.History, TraceNode{
		ID:          "node-" + id,
		ParentID:    "node-root",
		Kind:        "direction",
		Title:       hypothesis,
		Summary:     rationale,
		Status:      string(direction.Status),
		DirectionID: id,
		Time:        time.Now(),
	})
	return &m.trace.Directions[len(m.trace.Directions)-1]
}

func (m *Manager) ensureGenericDirectionLocked(hypothesis, rationale string, priority int) string {
	id := "dir-" + slug(hypothesis)
	direction := m.ensureDirectionLocked(id, hypothesis, rationale, priority, nil)
	if direction != nil {
		return direction.ID
	}
	return id
}

func (m *Manager) addEvidenceLocked(directionID, summary, detail, source string, eventIDs []int) Evidence {
	m.evidenceSeq++
	ev := Evidence{
		ID:        fmt.Sprintf("evidence-%d", m.evidenceSeq),
		Summary:   summary,
		Detail:    short(detail, excerptLimit),
		Source:    source,
		EventIDs:  append([]int(nil), eventIDs...),
		CreatedAt: time.Now(),
	}
	if idx := m.directionIndexLocked(directionID); idx >= 0 {
		m.trace.Directions[idx].SupportingEvidence = append(m.trace.Directions[idx].SupportingEvidence, ev)
		if m.trace.Directions[idx].Status == DirectionOpen {
			m.trace.Directions[idx].Status = DirectionSupported
		}
	}
	m.trace.History = append(m.trace.History, TraceNode{
		ID:          "node-" + ev.ID,
		ParentID:    "node-" + directionID,
		Kind:        "evidence",
		Title:       summary,
		Summary:     short(detail, 240),
		Status:      "ok",
		EventIDs:    append([]int(nil), eventIDs...),
		DirectionID: directionID,
		Time:        ev.CreatedAt,
	})
	return ev
}

func (m *Manager) updateFrontierLocked() {
	var candidates []string
	var recommended []NextAction
	active := m.trace.Frontier.ActiveDirectionID
	for _, direction := range m.trace.Directions {
		switch direction.Status {
		case DirectionOpen, DirectionActive, DirectionSupported:
			candidates = append(candidates, direction.ID)
			for _, action := range direction.NextActions {
				if action.ID == "" {
					action.ID = "next-" + direction.ID
				}
				action.DirectionID = direction.ID
				recommended = append(recommended, action)
			}
			if active == "" {
				active = direction.ID
			}
		}
	}
	if active == "" && len(m.trace.Symptoms) == 0 {
		recommended = append(recommended, NextAction{
			ID:               "next-reproduce",
			Action:           "Run or inspect the narrowest command that exposes the reported problem.",
			Tool:             "run_tests",
			Rationale:        "The trace needs a current symptom before choosing a fix direction.",
			ExpectedEvidence: []string{"A concrete error signature, failing file/package, or confirmation that the issue is already fixed."},
			Priority:         50,
		})
	}
	m.trace.Frontier = InvestigationFrontier{
		ActiveDirectionID:   active,
		CandidateDirections: candidates,
		OpenQuestions:       openQuestions(m.trace),
		RecommendedActions:  recommended,
		StopConditions: []string{
			"The root cause has current-repository evidence.",
			"The smallest relevant fix has been applied.",
			"Focused verification passes or the remaining blocker is explicitly recorded.",
		},
		Risks: []string{
			"Do not treat historical memory as fact until verified in this checkout.",
			"Do not save secrets, hidden reasoning, or unbounded stdout into memory cards.",
		},
	}
}

func (m *Manager) generateCardsLocked(status string, submission string) []MemoryCard {
	if len(m.trace.Cards) > 0 {
		return nil
	}
	now := time.Now()
	add := func(kind, summary string) MemoryCard {
		m.cardSeq++
		card := MemoryCard{
			ID:          fmt.Sprintf("card-%d", m.cardSeq),
			Kind:        kind,
			Summary:     summary,
			ProblemSig:  m.problemSignatureLocked(),
			SourceRunID: m.trace.TraceID,
			Status:      "draft",
			CreatedAt:   now,
		}
		m.trace.Cards = append(m.trace.Cards, card)
		return card
	}
	var cards []MemoryCard
	if summary := strings.TrimSpace(m.trace.Problem.ErrorSummary); summary != "" {
		cards = append(cards, add("symptom", summary))
	}
	for _, direction := range m.trace.Directions {
		if len(direction.SupportingEvidence) == 0 {
			continue
		}
		card := add("direction", direction.Hypothesis)
		card.Evidence = evidenceSummaries(direction.SupportingEvidence)
		m.trace.Cards[len(m.trace.Cards)-1] = card
		cards = append(cards, card)
	}
	if m.patchApplied && strings.TrimSpace(submission) != "" {
		card := add("fix_pattern", strings.TrimSpace(submission))
		card.FixPattern = strings.TrimSpace(submission)
		m.trace.Cards[len(m.trace.Cards)-1] = card
		cards = append(cards, card)
	}
	if m.verificationOK || strings.EqualFold(status, "submitted") {
		card := add("verification", "Focused verification completed for this run.")
		card.Verification = "status=" + status
		m.trace.Cards[len(m.trace.Cards)-1] = card
		cards = append(cards, card)
	}
	cards = append(cards, add("run_summary", valueOr(strings.TrimSpace(submission), "Run finished with status "+status)))
	return cards
}

func (m *Manager) promptBlocksLocked(input PromptInput) []PromptBlock {
	blocks := []PromptBlock{
		{Kind: "system_rules", Title: "System Rules", Count: countMessages(input.Messages, core.RoleSystem), Included: countMessages(input.Messages, core.RoleSystem) > 0, Summary: "agent system prompt"},
		{Kind: "user_task", Title: "User Task", Count: countMessages(input.Messages, core.RoleUser), Included: countMessages(input.Messages, core.RoleUser) > 0, Summary: "current task and repository root"},
		{Kind: "recent_observations", Title: "Recent Observations", Count: countMessages(input.Messages, core.RoleTool), Included: countMessages(input.Messages, core.RoleTool) > 0, Summary: "tool observations from this run"},
		{Kind: "conversation_state", Title: "Conversation State", Count: countMessages(input.Messages, core.RoleAssistant), Included: countMessages(input.Messages, core.RoleAssistant) > 0, Summary: "visible assistant responses from this run"},
		{Kind: "tool_schema", Title: "Tool Schema", Count: len(input.Tools), Included: len(input.Tools) > 0, Summary: "available tool names and schemas"},
	}
	if strings.TrimSpace(m.trace.Problem.UserTask) != "" {
		blocks = append(blocks, PromptBlock{Kind: "problem_context", Title: "Problem Context", Included: true, Content: short(m.trace.Problem.UserTask, 600), Summary: m.trace.Problem.ErrorSummary})
	}
	if len(m.trace.Directions) > 0 || len(m.trace.Frontier.RecommendedActions) > 0 {
		blocks = append(blocks, PromptBlock{Kind: "frontier", Title: "Investigation Frontier", Included: true, Content: m.renderPromptContextLocked(), SourceIDs: append([]string(nil), m.trace.Frontier.CandidateDirections...)})
	} else {
		blocks = append(blocks, PromptBlock{Kind: "frontier", Title: "Investigation Frontier", Included: false, Summary: "not enough evidence yet"})
	}
	if len(m.trace.Memories) > 0 {
		blocks = append(blocks, PromptBlock{Kind: "memory_context", Title: "Memory Context", Included: true, Count: len(m.trace.Memories), Summary: "retrieved memory is hypothesis input only"})
	} else {
		blocks = append(blocks, PromptBlock{Kind: "memory_context", Title: "Memory Context", Included: false, Summary: "no memory retrieved"})
	}
	return blocks
}

func (m *Manager) renderPromptContextLocked() string {
	var b strings.Builder
	b.WriteString("Problem Trace Context\n")
	if m.trace.Problem.ErrorSummary != "" {
		fmt.Fprintf(&b, "- Current symptom: %s\n", m.trace.Problem.ErrorSummary)
	}
	if m.trace.Frontier.ActiveDirectionID != "" {
		if idx := m.directionIndexLocked(m.trace.Frontier.ActiveDirectionID); idx >= 0 {
			d := m.trace.Directions[idx]
			fmt.Fprintf(&b, "- Active direction: %s (%s)\n", d.Hypothesis, d.Status)
			if d.Rationale != "" {
				fmt.Fprintf(&b, "- Why this direction: %s\n", d.Rationale)
			}
		}
	}
	if len(m.trace.Directions) > 0 {
		b.WriteString("- Known evidence:\n")
		for _, direction := range m.trace.Directions {
			for _, ev := range direction.SupportingEvidence {
				fmt.Fprintf(&b, "  - %s: %s\n", direction.ID, ev.Summary)
			}
		}
	}
	if len(m.trace.Frontier.RecommendedActions) > 0 {
		b.WriteString("- Recommended next actions:\n")
		for _, action := range m.trace.Frontier.RecommendedActions {
			fmt.Fprintf(&b, "  - %s\n", action.Action)
			for _, expected := range action.ExpectedEvidence {
				fmt.Fprintf(&b, "    expected: %s\n", expected)
			}
		}
	}
	b.WriteString("- Instruction: treat memory as hypotheses, not facts; verify in the current repo before patching; avoid repeating refuted directions.\n")
	return b.String()
}

func (m *Manager) startSpanLocked(name, kind, parentID string, attrs map[string]any) TraceSpan {
	m.spanSeq++
	id := fmt.Sprintf("span-%d", m.spanSeq)
	if attrs == nil {
		attrs = map[string]any{}
	}
	attrs[AttrDirectionID] = valueOr(fmt.Sprint(attrs[AttrDirectionID]), m.trace.Frontier.ActiveDirectionID)
	return TraceSpan{
		TraceID:      m.trace.TraceID,
		SpanID:       id,
		ParentSpanID: parentID,
		Name:         name,
		Kind:         kind,
		StartTime:    time.Now(),
		Status:       SpanStatusUnset,
		Attributes:   attrs,
		Resource:     m.trace.Resource,
	}
}

func (m *Manager) upsertSpanLocked(span TraceSpan) {
	for i := range m.trace.Spans {
		if m.trace.Spans[i].SpanID == span.SpanID {
			m.trace.Spans[i] = span
			return
		}
	}
	m.trace.Spans = append(m.trace.Spans, span)
}

func (m *Manager) spanByIDLocked(spanID string) *TraceSpan {
	for i := range m.trace.Spans {
		if m.trace.Spans[i].SpanID == spanID {
			return &m.trace.Spans[i]
		}
	}
	return nil
}

func (m *Manager) linkLocked(fromID, toID, kind string, attrs map[string]any) {
	link := TraceLink{TraceID: m.trace.TraceID, FromID: fromID, ToID: toID, Kind: kind, Attributes: attrs}
	m.trace.Links = append(m.trace.Links, link)
	if span := m.spanByIDLocked(fromID); span != nil {
		span.Links = append(span.Links, link)
	}
}

func (m *Manager) contextLocked(spanID string) TraceContext {
	span := m.spanByIDLocked(spanID)
	tc := TraceContext{
		TraceID:     m.trace.TraceID,
		SpanID:      spanID,
		DirectionID: m.trace.Frontier.ActiveDirectionID,
		Flags:       TraceFlags{Recording: true, Sampled: true},
	}
	if span != nil {
		tc.ParentSpanID = span.ParentSpanID
		if value := strings.TrimSpace(fmt.Sprint(span.Attributes[AttrPromptSnapshotID])); value != "" && value != "<nil>" {
			tc.PromptSnapshotID = value
		}
	}
	return tc
}

func (m *Manager) eventLocked(eventType string, data map[string]any) core.Event {
	if data == nil {
		data = map[string]any{}
	}
	if _, ok := data["trace_id"]; !ok && m.trace.TraceID != "" {
		data["trace_id"] = m.trace.TraceID
	}
	return core.Event{Type: eventType, Time: time.Now(), Data: data}
}

func (m *Manager) nextPromptIDLocked() string {
	m.promptSeq++
	return fmt.Sprintf("prompt-%d", m.promptSeq)
}

func (m *Manager) directionIndexLocked(id string) int {
	for i := range m.trace.Directions {
		if m.trace.Directions[i].ID == id {
			return i
		}
	}
	return -1
}

func (m *Manager) problemSignatureLocked() string {
	parts := []string{m.trace.Problem.ErrorSummary}
	for _, symptom := range m.trace.Symptoms {
		parts = append(parts, symptom.ErrorType)
	}
	return hashString(strings.Join(parts, "|"))
}

func detectSymptoms(call core.ToolCall, result core.ToolResult) []Symptom {
	if result.Code == 0 && !result.TimedOut {
		return nil
	}
	output := result.Output
	lower := strings.ToLower(output)
	command := commandFromToolCall(call)
	kind := "tool_failure"
	errorType := "nonzero_exit"
	summary := fmt.Sprintf("%s failed", valueOr(call.Name, "tool"))

	switch {
	case strings.Contains(output, "import cycle not allowed"):
		kind = "compile_error"
		errorType = "go_import_cycle"
		summary = "Go compile failed with import cycle not allowed"
	case strings.Contains(lower, "undefined:"):
		kind = "compile_error"
		errorType = "go_undefined"
		summary = "Go compile failed with undefined symbol"
	case strings.Contains(lower, "panic:"):
		kind = "runtime_panic"
		errorType = "panic"
		summary = "Command failed with runtime panic"
	case strings.Contains(lower, "lint") || strings.Contains(command, "golangci-lint") || strings.Contains(command, "eslint") || strings.Contains(command, "ruff"):
		kind = "lint_error"
		errorType = "lint_error"
		summary = "Lint command failed"
	case call.Name == "run_tests" || looksLikeTestCommand(command):
		kind = "test_failure"
		errorType = "test_failure"
		summary = "Test command failed"
	case result.TimedOut:
		kind = "timeout"
		errorType = "timeout"
		summary = "Tool command timed out"
	}
	if command != "" {
		summary += ": " + command
	}
	return []Symptom{{
		Kind:       kind,
		Summary:    summary,
		RawExcerpt: excerptAroundSignal(output),
		ErrorType:  errorType,
		Command:    command,
		Files:      extractFileRefs(output),
		Packages:   extractPackages(output),
	}}
}

func openQuestions(trace ProblemTrace) []string {
	if len(trace.Symptoms) == 0 {
		return []string{"What exact command or observation reproduces the reported problem?"}
	}
	var questions []string
	active := trace.Frontier.ActiveDirectionID
	if active != "" {
		questions = append(questions, "What evidence would support or refute "+active+"?")
	}
	if !hasFixedDirection(trace) {
		questions = append(questions, "Which smallest change addresses the supported root cause?")
		questions = append(questions, "Which focused verification proves the fix?")
	}
	return questions
}

func toolSpanKind(tool string) string {
	switch tool {
	case "run_tests":
		return SpanTestRun
	case "apply_patch":
		return SpanPatchApply
	case "submit":
		return "submit"
	default:
		return SpanToolCall
	}
}

func commandFromToolCall(call core.ToolCall) string {
	if call.Args == nil {
		return ""
	}
	for _, key := range []string{"command", "path", "query", "patch"} {
		if value := strings.TrimSpace(fmt.Sprint(call.Args[key])); value != "" && value != "<nil>" {
			if key == "patch" {
				return "patch:" + hashString(value)
			}
			return value
		}
	}
	return ""
}

func isValidationCommand(tool, command string) bool {
	return tool == "run_tests" || looksLikeTestCommand(command)
}

func looksLikeTestCommand(command string) bool {
	command = strings.ToLower(strings.TrimSpace(command))
	for _, pattern := range []string{"go test", "pytest", "cargo test", "npm test", "pnpm test", "yarn test", "mvn test", "gradle test", "make test", "go vet", "golangci-lint", "eslint", "ruff"} {
		if strings.Contains(command, pattern) {
			return true
		}
	}
	return false
}

func countMessages(messages []core.Message, role core.Role) int {
	count := 0
	for _, msg := range messages {
		if msg.Role == role {
			count++
		}
	}
	return count
}

func estimateTokens(messages []core.Message, tools []core.ToolSpec) int {
	chars := 0
	for _, msg := range messages {
		chars += len([]rune(msg.Content))
	}
	for _, tool := range tools {
		chars += len([]rune(tool.Name)) + len([]rune(tool.Description))
	}
	if chars == 0 {
		return 0
	}
	return chars/4 + 1
}

func currentDirectionIDs(trace ProblemTrace) []string {
	var ids []string
	for _, direction := range trace.Directions {
		ids = append(ids, direction.ID)
	}
	return ids
}

func currentMemoryIDs(trace ProblemTrace) []string {
	var ids []string
	for _, memory := range trace.Memories {
		ids = append(ids, memory.ID)
	}
	return ids
}

func firstUserTask(messages []core.Message) string {
	for _, msg := range messages {
		if msg.Role == core.RoleUser {
			return msg.Content
		}
	}
	return ""
}

func extractFileRefs(output string) []string {
	re := regexp.MustCompile(`(?:^|\s)([A-Za-z0-9_./-]+\.(?:go|py|js|ts|tsx|jsx|rs|java|c|cc|cpp|h|hpp|yaml|yml|json))(?::\d+)?`)
	return uniqueMatches(re, output)
}

func extractPackages(output string) []string {
	re := regexp.MustCompile(`(?:package|FAIL)\s+([A-Za-z0-9_./-]+)`)
	return uniqueMatches(re, output)
}

func uniqueMatches(re *regexp.Regexp, text string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, match := range re.FindAllStringSubmatch(text, 20) {
		if len(match) < 2 {
			continue
		}
		value := strings.TrimSpace(match[1])
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func excerptAroundSignal(output string) string {
	output = strings.TrimSpace(output)
	if output == "" {
		return ""
	}
	lower := strings.ToLower(output)
	for _, needle := range []string{"import cycle not allowed", "panic:", "undefined:", "fail", "error"} {
		idx := strings.Index(lower, needle)
		if idx >= 0 {
			start := max(0, idx-500)
			end := min(len(output), idx+1000)
			return short(output[start:end], excerptLimit)
		}
	}
	return short(output, excerptLimit)
}

func expectedEvidenceForActions(actions []NextAction) []string {
	var out []string
	for _, action := range actions {
		out = append(out, action.ExpectedEvidence...)
	}
	return out
}

func evidenceSummaries(evidence []Evidence) []string {
	out := make([]string, 0, len(evidence))
	for _, ev := range evidence {
		out = append(out, ev.Summary)
	}
	return out
}

func hasFixedDirection(trace ProblemTrace) bool {
	for _, direction := range trace.Directions {
		if direction.Status == DirectionFixed {
			return true
		}
	}
	return false
}

func cloneTrace(trace ProblemTrace) ProblemTrace {
	data, err := json.Marshal(trace)
	if err != nil {
		return trace
	}
	var out ProblemTrace
	if err := json.Unmarshal(data, &out); err != nil {
		return trace
	}
	return out
}

func hashAny(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return hashString(fmt.Sprint(value))
	}
	return hashString(string(data))
}

func hashString(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:16]
}

func newID(prefix string, at time.Time, seed string) string {
	return fmt.Sprintf("%s-%s-%s", prefix, at.UTC().Format("20060102T150405"), hashString(seed))
}

func short(value string, limit int) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	if limit <= 3 {
		return string(runes[:limit])
	}
	return string(runes[:limit-3]) + "..."
}

func stripFenced(value string) string {
	re := regexp.MustCompile("(?s)```.*?```")
	return strings.TrimSpace(re.ReplaceAllString(value, ""))
}

func valueOr(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" || value == "<nil>" {
		return strings.TrimSpace(fallback)
	}
	return value
}

func contains(values []string, value string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}

func slug(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	lastDash := false
	for _, r := range value {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if ok {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash && b.Len() > 0 {
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
