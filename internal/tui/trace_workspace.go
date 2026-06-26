package tui

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/local/swe-agent/internal/core"
	"github.com/local/swe-agent/internal/problemtrace"
)

func traceWorkspaceViewWidth(record taskRecord, state traceWorkspaceState, width int, trajectoryPath string) string {
	return traceWorkspaceView(record, state, width, 0, trajectoryPath)
}

func traceWorkspaceView(record taskRecord, state traceWorkspaceState, width int, height int, trajectoryPath string) string {
	state.ensureDefaults()
	vm := buildTraceWorkspaceVM(record, state, trajectoryPath)
	var b strings.Builder
	b.WriteString("Problem Trace Workspace\n")
	b.WriteString(traceTabs(state.Tab))
	b.WriteString("\n\n")
	switch state.Tab {
	case traceTabFrontier, traceTabMemory:
		renderTraceCollectionWorkspace(&b, buildTraceCollectionVM(state.Tab, vm.Trace, state.Debug), state, width, height)
	case traceTabEvents:
		renderTraceEventsWorkspace(&b, record.Events, state, width, height)
	case traceTabPrompt, traceTabCards:
		renderTraceCollectionWorkspace(&b, buildTraceCollectionVM(state.Tab, vm.Trace, state.Debug), state, width, height)
	default:
		renderTraceTreeTab(&b, vm, state, width, height, record)
	}
	return b.String()
}

func traceTabs(active traceTab) string {
	tabs := []traceTab{traceTabTrace, traceTabFrontier, traceTabMemory, traceTabEvents, traceTabPrompt, traceTabCards}
	parts := make([]string, 0, len(tabs))
	for i, tab := range tabs {
		label := fmt.Sprintf("%d %s", i+1, traceTabLabel(tab))
		if tab == active {
			label = "[" + label + "]"
		}
		parts = append(parts, label)
	}
	return strings.Join(parts, "  ")
}

func traceTabLabel(tab traceTab) string {
	switch tab {
	case traceTabFrontier:
		return "Next"
	case traceTabMemory:
		return "Memory"
	case traceTabEvents:
		return "Events"
	case traceTabPrompt:
		return "Prompt"
	case traceTabCards:
		return "Learn"
	default:
		return "Trace"
	}
}

func traceDetailTabLabel(tab traceDetailTab) string {
	switch tab {
	case traceDetailOutput:
		return "Output"
	case traceDetailEvents:
		return "Events"
	case traceDetailDebug:
		return "Debug"
	default:
		return "Overview"
	}
}

// renderTraceSpanGraph is reserved for a future Spans tab.
func renderTraceSpanGraph(b *strings.Builder, trace problemtrace.ProblemTrace, width int) {
	writeSection(b, "Span Graph")
	if len(trace.Spans) == 0 {
		b.WriteString("No spans recorded yet.\n")
		return
	}
	spans := append([]problemtrace.TraceSpan(nil), trace.Spans...)
	sort.SliceStable(spans, func(i, j int) bool {
		return spans[i].StartTime.Before(spans[j].StartTime)
	})
	for _, span := range spans {
		status := string(span.Status)
		if status == "" {
			status = "unset"
		}
		line := fmt.Sprintf("%s %s parent=%s status=%s", span.SpanID, span.Name, valueOrDefault(span.ParentSpanID, "root"), status)
		b.WriteString(wrapText(line, width))
		b.WriteByte('\n')
	}
	if len(trace.Links) > 0 {
		writeSection(b, "Links")
		for _, link := range trace.Links {
			b.WriteString(wrapText(fmt.Sprintf("%s --%s--> %s", link.FromID, link.Kind, link.ToID), width))
			b.WriteByte('\n')
		}
	}
}

type TraceCollectionVM struct {
	Title       string
	DetailTitle string
	Empty       string
	Rows        []TraceCollectionRowVM
}

type TraceCollectionRowVM struct {
	Index   int
	Kind    string
	Icon    string
	Status  string
	Title   string
	Summary string
	Data    any
	Raw     any
}

func isTraceCollectionTab(tab traceTab) bool {
	switch tab {
	case traceTabFrontier, traceTabMemory, traceTabPrompt, traceTabCards:
		return true
	default:
		return false
	}
}

func buildTraceCollectionVM(tab traceTab, trace problemtrace.ProblemTrace, debug bool) TraceCollectionVM {
	switch tab {
	case traceTabFrontier:
		return buildTraceNextCollection(trace, debug)
	case traceTabMemory:
		return buildTraceMemoryCollection(trace, debug)
	case traceTabPrompt:
		return buildTracePromptCollection(trace, debug)
	case traceTabCards:
		return buildTraceLearnCollection(trace, debug)
	default:
		return TraceCollectionVM{
			Title:       "Items",
			DetailTitle: "Selected Item",
			Empty:       "No items recorded.",
		}
	}
}

func buildTraceNextCollection(trace problemtrace.ProblemTrace, debug bool) TraceCollectionVM {
	rows := []TraceCollectionRowVM{}
	activeID := strings.TrimSpace(trace.Frontier.ActiveDirectionID)
	activeSummary := activeDirectionSummary(trace)
	activeRaw := traceDirectionByID(trace, activeID)
	rows = append(rows, TraceCollectionRowVM{
		Index:   len(rows),
		Kind:    "direction",
		Icon:    "D",
		Status:  valueOrDefault(activeID, "none"),
		Title:   "Active",
		Summary: activeSummary,
		Data: map[string]any{
			"active_direction_id": activeID,
			"summary":             activeSummary,
		},
		Raw: activeRaw,
	})

	actions := trace.Frontier.RecommendedActions
	if !debug {
		actions = topActions(actions, 3)
	}
	for _, action := range actions {
		rows = append(rows, TraceCollectionRowVM{
			Index:   len(rows),
			Kind:    "action",
			Icon:    ">",
			Status:  "next",
			Title:   "Action",
			Summary: action.Action,
			Data: map[string]any{
				"action":            action.Action,
				"tool":              action.Tool,
				"command":           action.Command,
				"rationale":         action.Rationale,
				"expected_evidence": action.ExpectedEvidence,
				"direction_id":      action.DirectionID,
				"priority":          action.Priority,
			},
			Raw: action,
		})
	}

	questions := trace.Frontier.OpenQuestions
	if !debug {
		questions = topStrings(questions, 3)
	}
	for _, question := range questions {
		rows = append(rows, TraceCollectionRowVM{
			Index:   len(rows),
			Kind:    "question",
			Icon:    "?",
			Status:  "open",
			Title:   "Open",
			Summary: question,
			Data:    map[string]any{"question": question},
			Raw:     question,
		})
	}

	if debug {
		for _, direction := range trace.Directions {
			rows = append(rows, TraceCollectionRowVM{
				Index:   len(rows),
				Kind:    "direction",
				Icon:    "D",
				Status:  string(direction.Status),
				Title:   "Directions",
				Summary: displayDirectionTitle(direction.ID, direction.Hypothesis),
				Data: map[string]any{
					"id":         direction.ID,
					"hypothesis": direction.Hypothesis,
					"rationale":  direction.Rationale,
					"status":     direction.Status,
					"priority":   direction.Priority,
				},
				Raw: direction,
			})
		}
		for _, stop := range trace.Frontier.StopConditions {
			rows = append(rows, TraceCollectionRowVM{
				Index:   len(rows),
				Kind:    "stop",
				Icon:    "!",
				Status:  "stop",
				Title:   "Stop Conditions",
				Summary: stop,
				Data:    map[string]any{"stop_condition": stop},
				Raw:     stop,
			})
		}
		for _, risk := range trace.Frontier.Risks {
			rows = append(rows, TraceCollectionRowVM{
				Index:   len(rows),
				Kind:    "risk",
				Icon:    "!",
				Status:  "risk",
				Title:   "Risks",
				Summary: risk,
				Data:    map[string]any{"risk": risk},
				Raw:     risk,
			})
		}
	}

	return TraceCollectionVM{
		Title:       "Next Work",
		DetailTitle: "Selected Next",
		Empty:       "No recommended action yet.",
		Rows:        rows,
	}
}

func buildTraceMemoryCollection(trace problemtrace.ProblemTrace, debug bool) TraceCollectionVM {
	rows := []TraceCollectionRowVM{}
	for _, memory := range trace.Memories {
		status := valueOrDefault(memory.Status, "used")
		rows = append(rows, TraceCollectionRowVM{
			Index:   len(rows),
			Kind:    "memory",
			Icon:    "M",
			Status:  status,
			Title:   "Memory",
			Summary: memory.Summary,
			Data: map[string]any{
				"id":         memory.ID,
				"status":     status,
				"similarity": memory.Similarity,
				"summary":    memory.Summary,
				"reason":     memory.Reason,
				"source_run": memory.SourceRunID,
			},
			Raw: memory,
		})
	}
	if debug {
		for _, policy := range traceMemoryPolicies() {
			rows = append(rows, TraceCollectionRowVM{
				Index:   len(rows),
				Kind:    "policy",
				Icon:    "P",
				Status:  "policy",
				Title:   "Policy",
				Summary: policy,
				Data:    map[string]any{"policy": policy},
				Raw:     policy,
			})
		}
	}
	return TraceCollectionVM{
		Title:       "Memory Sources",
		DetailTitle: "Selected Memory",
		Empty:       traceMemoryEmptyMessage(debug),
		Rows:        rows,
	}
}

func traceMemoryEmptyMessage(debug bool) string {
	if debug {
		return "No memory used in this run."
	}
	return "No memory surfaced for this run. Press D for debug policy context."
}

func buildTracePromptCollection(trace problemtrace.ProblemTrace, debug bool) TraceCollectionVM {
	rows := []TraceCollectionRowVM{}
	if len(trace.Prompts) == 0 {
		return TraceCollectionVM{
			Title:       "Prompt Context",
			DetailTitle: "Selected Prompt",
			Empty:       tracePromptEmptyMessage(debug),
		}
	}

	prompts := trace.Prompts
	if !debug {
		prompts = trace.Prompts[len(trace.Prompts)-1:]
	}
	for _, prompt := range prompts {
		status := "latest"
		title := "Latest Prompt"
		if debug {
			status = "snapshot"
			title = "Prompt"
		}
		summary := tracePromptSummary(prompt)
		if debug && strings.TrimSpace(prompt.Model) != "" {
			summary += " Model: " + strings.TrimSpace(prompt.Model)
		}
		rows = append(rows, TraceCollectionRowVM{
			Index:   len(rows),
			Kind:    "prompt",
			Icon:    "P",
			Status:  status,
			Title:   title,
			Summary: summary,
			Data: map[string]any{
				"id":             prompt.ID,
				"step":           prompt.Step,
				"model":          prompt.Model,
				"messages":       prompt.MessageCount,
				"tools":          prompt.ToolCount,
				"token_estimate": prompt.TokenEstimate,
			},
			Raw: prompt,
		})
		for _, block := range prompt.Blocks {
			if !debug && !isCompactPromptBlock(block.Kind) {
				continue
			}
			statusText := "no"
			if block.Included {
				statusText = "yes"
			}
			summary := fmt.Sprintf("%s: %s", valueOrDefault(block.Title, block.Kind), statusText)
			if debug {
				summary = fmt.Sprintf("%s included=%s", valueOrDefault(block.Title, block.Kind), statusText)
			}
			if block.Count > 0 {
				summary += fmt.Sprintf(" count=%d", block.Count)
			}
			rows = append(rows, TraceCollectionRowVM{
				Index:   len(rows),
				Kind:    "prompt_block",
				Icon:    "C",
				Status:  statusText,
				Title:   "Context",
				Summary: summary,
				Data: map[string]any{
					"kind":     block.Kind,
					"title":    block.Title,
					"included": block.Included,
					"count":    block.Count,
					"summary":  block.Summary,
				},
				Raw: block,
			})
		}
	}

	return TraceCollectionVM{
		Title:       "Prompt Context",
		DetailTitle: "Selected Prompt",
		Empty:       tracePromptEmptyMessage(debug),
		Rows:        rows,
	}
}

func tracePromptEmptyMessage(debug bool) string {
	if debug {
		return "No prompt snapshots recorded."
	}
	return "No prompt snapshot surfaced for this run. Press D for raw prompt/debug context."
}

func buildTraceLearnCollection(trace problemtrace.ProblemTrace, debug bool) TraceCollectionVM {
	rows := []TraceCollectionRowVM{}
	for _, card := range trace.Cards {
		status := valueOrDefault(card.Status, "draft")
		summary := fmt.Sprintf("%s %s", valueOrDefault(card.Kind, "card"), status)
		if card.Summary != "" {
			summary += "  " + card.Summary
		}
		rows = append(rows, TraceCollectionRowVM{
			Index:   len(rows),
			Kind:    "card",
			Icon:    "L",
			Status:  status,
			Title:   "Card",
			Summary: summary,
			Data: map[string]any{
				"id":           card.ID,
				"kind":         card.Kind,
				"status":       status,
				"summary":      card.Summary,
				"fix":          card.FixPattern,
				"verification": card.Verification,
			},
			Raw: card,
		})
	}
	return TraceCollectionVM{
		Title:       "Draft Memory Cards",
		DetailTitle: "Selected Card",
		Empty:       "No draft memory cards yet. Cards are generated when the run finishes.",
		Rows:        rows,
	}
}

func renderTraceCollectionWorkspace(b *strings.Builder, collection TraceCollectionVM, state traceWorkspaceState, width int, height int) {
	if width < 120 {
		renderTraceCollectionListOnly(b, collection, state.CollectionCursor, width, height)
		return
	}

	const gap = 3
	leftWidth, rightWidth := traceEventSplitWidths(width, gap)
	cursor := traceCollectionCursorForRows(state, collection.Rows)
	left := renderTraceCollectionList(collection, cursor, leftWidth, state.CollectionPane == tracePaneTree)

	var right string
	if len(collection.Rows) == 0 {
		right = collection.DetailTitle + "\n\n" + collection.Empty
		if !strings.HasSuffix(right, "\n") {
			right += "\n"
		}
	} else {
		right = renderTraceCollectionDetail(collection, collection.Rows[cursor], state.CollectionTab, rightWidth, state.CollectionPane == tracePaneDetail)
	}

	panelHeight := traceEventSplitPanelHeight(left, right, height)
	left = fitEventListAroundCursor(left, panelHeight, cursor)
	right = fitHeightOffset(right, panelHeight, state.CollectionOffset)

	b.WriteString(renderSplitPanels(left, right, leftWidth, rightWidth))
}

func renderTraceCollectionListOnly(b *strings.Builder, collection TraceCollectionVM, cursor int, width int, height int) {
	cursor = traceCollectionCursorForRows(traceWorkspaceState{CollectionCursor: cursor}, collection.Rows)
	content := renderTraceCollectionList(collection, cursor, width, true)
	if height > 0 {
		content = fitEventListAroundCursor(content, max(8, height-4), cursor)
	}
	b.WriteString(content)
}

func renderTraceCollectionList(collection TraceCollectionVM, cursor int, width int, active bool) string {
	var b strings.Builder
	title := collection.Title
	b.WriteString(renderTracePaneTitle(title, active))
	b.WriteByte('\n')
	b.WriteString("j/k move  pg scroll  l detail  o open  [/] tabs  D debug\n\n")

	if len(collection.Rows) == 0 {
		b.WriteString(collection.Empty)
		if !strings.HasSuffix(collection.Empty, "\n") {
			b.WriteByte('\n')
		}
		return b.String()
	}

	cursor = clamp(cursor, 0, len(collection.Rows)-1)
	for i, row := range collection.Rows {
		prefix := "  "
		if i == cursor {
			prefix = "> "
		}

		line := fmt.Sprintf(
			"%s#%-3d %-2s %-16s %s",
			prefix,
			row.Index+1,
			row.Icon,
			truncateDisplay(row.Title, 16),
			row.Summary,
		)
		line = truncateDisplay(line, width)

		if i == cursor {
			line = traceSelectedStyle.Width(width).Render(line)
		} else {
			line = collectionRowStyle(row).Render(line)
		}

		b.WriteString(line)
		b.WriteByte('\n')
	}

	return b.String()
}

func renderTraceCollectionDetail(collection TraceCollectionVM, row TraceCollectionRowVM, tab traceCollectionTab, width int, active bool) string {
	var b strings.Builder
	title := collection.DetailTitle
	b.WriteString(renderTracePaneTitle(title, active))
	b.WriteByte('\n')
	b.WriteString("j/k scroll  pg page  h list  [/] tabs\n")
	b.WriteString(traceCollectionTabs(tab))
	b.WriteString("\n\n")

	switch tab {
	case traceCollectionData:
		renderTraceCollectionData(&b, row, width)
	case traceCollectionRaw:
		renderTraceCollectionRaw(&b, row, width)
	default:
		renderTraceCollectionOverview(&b, row, width)
	}

	return b.String()
}

func renderTraceCollectionOverview(b *strings.Builder, row TraceCollectionRowVM, width int) {
	writeField(b, "Kind", row.Kind, width)
	writeField(b, "Status", row.Status, width)
	writeField(b, "Title", row.Title, width)
	writeField(b, "Summary", row.Summary, width)

	switch typed := row.Raw.(type) {
	case problemtrace.NextAction:
		writeField(b, "Action", typed.Action, width)
		writeField(b, "Tool", typed.Tool, width)
		writeField(b, "Command", typed.Command, width)
		writeField(b, "Rationale", typed.Rationale, width)
		for _, expected := range topStrings(typed.ExpectedEvidence, 3) {
			writeField(b, "Expected", expected, width)
		}
	case problemtrace.InvestigationDirection:
		writeField(b, "Direction", displayDirectionTitle(typed.ID, typed.Hypothesis), width)
		writeField(b, "Rationale", typed.Rationale, width)
		writeField(b, "Priority", typed.Priority, width)
	case problemtrace.MemoryUsage:
		writeField(b, "Memory", typed.Summary, width)
		writeField(b, "Similarity", typed.Similarity, width)
		writeField(b, "Reason", typed.Reason, width)
	case problemtrace.PromptSnapshot:
		writeField(b, "Latest Prompt", tracePromptSummary(typed), width)
		if row.Status == "snapshot" {
			writeField(b, "Model", typed.Model, width)
		}
		writeField(b, "Tokens", typed.TokenEstimate, width)
		writeField(b, "Messages", typed.MessageCount, width)
		writeField(b, "Tools", typed.ToolCount, width)
		writeSection(b, "Context")
		for _, block := range typed.Blocks {
			status := "no"
			if block.Included {
				status = "yes"
			}
			line := fmt.Sprintf("- %s: %s", valueOrDefault(block.Title, block.Kind), status)
			if row.Status == "snapshot" {
				line = fmt.Sprintf("- %s included=%s", valueOrDefault(block.Title, block.Kind), status)
			}
			if block.Count > 0 {
				line += fmt.Sprintf(" count=%d", block.Count)
			}
			b.WriteString(wrapText(line, width))
			b.WriteByte('\n')
		}
	case problemtrace.PromptBlock:
		writeField(b, "Block", valueOrDefault(typed.Title, typed.Kind), width)
		writeField(b, "Included", typed.Included, width)
		writeField(b, "Count", typed.Count, width)
		writeField(b, "Summary", typed.Summary, width)
		writeField(b, "Content", typed.Content, width)
	case problemtrace.MemoryCard:
		writeField(b, "Card", valueOrDefault(typed.Kind, "card"), width)
		writeField(b, "Status", valueOrDefault(typed.Status, "draft"), width)
		writeField(b, "Summary", typed.Summary, width)
		writeField(b, "Fix", typed.FixPattern, width)
		writeField(b, "Verification", typed.Verification, width)
		for _, evidence := range typed.Evidence {
			writeField(b, "Evidence", evidence, width)
		}
	case string:
		switch row.Kind {
		case "question":
			writeField(b, "Question", typed, width)
		case "risk":
			writeField(b, "Risk", typed, width)
		case "stop":
			writeField(b, "Stop Condition", typed, width)
		case "policy":
			writeField(b, "Policy", typed, width)
		}
	}
}

func renderTraceCollectionData(b *strings.Builder, row TraceCollectionRowVM, width int) {
	if row.Data == nil {
		b.WriteString("No structured data for this item.\n")
		return
	}
	writeValueTree(b, "", row.Data, 0, width)
}

func renderTraceCollectionRaw(b *strings.Builder, row TraceCollectionRowVM, width int) {
	if row.Raw == nil {
		b.WriteString("No raw data for this item.\n")
		return
	}
	writeValueTree(b, "", row.Raw, 0, width)
}

func traceCollectionTabs(active traceCollectionTab) string {
	items := []traceCollectionTab{
		traceCollectionOverview,
		traceCollectionData,
		traceCollectionRaw,
	}

	parts := make([]string, 0, len(items))
	for _, item := range items {
		label := traceCollectionTabLabel(item)
		if item == active {
			label = "[" + label + "]"
		}
		parts = append(parts, label)
	}
	return strings.Join(parts, "  ")
}

func traceCollectionTabLabel(tab traceCollectionTab) string {
	switch tab {
	case traceCollectionData:
		return "Data"
	case traceCollectionRaw:
		return "Raw"
	default:
		return "Overview"
	}
}

func traceCollectionDetailMaxOffset(collection TraceCollectionVM, state traceWorkspaceState, width int, height int) int {
	if width < 120 || len(collection.Rows) == 0 {
		return 0
	}
	const gap = 3
	leftWidth, rightWidth := traceEventSplitWidths(width, gap)
	cursor := traceCollectionCursorForRows(state, collection.Rows)
	left := renderTraceCollectionList(collection, cursor, leftWidth, state.CollectionPane == tracePaneTree)
	right := renderTraceCollectionDetail(collection, collection.Rows[cursor], state.CollectionTab, rightWidth, state.CollectionPane == tracePaneDetail)
	panelHeight := traceEventSplitPanelHeight(left, right, height)
	return max(0, lipgloss.Height(right)-panelHeight)
}

func traceCollectionCursorForRows(state traceWorkspaceState, rows []TraceCollectionRowVM) int {
	if len(rows) == 0 {
		return 0
	}
	return clamp(state.CollectionCursor, 0, len(rows)-1)
}

func collectionRowStyle(row TraceCollectionRowVM) lipgloss.Style {
	switch row.Kind {
	case "direction":
		return traceDirectionStyle
	case "action":
		return traceActionStyle
	case "question", "prompt", "prompt_block":
		return traceThoughtStyle
	case "memory":
		return traceMemoryStyle
	case "card":
		return traceFixStyle
	case "risk", "stop":
		return traceErrorStyle
	case "policy":
		return traceDefaultStyle
	default:
		return traceDefaultStyle
	}
}

func traceDirectionByID(trace problemtrace.ProblemTrace, id string) any {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil
	}
	for _, direction := range trace.Directions {
		if strings.TrimSpace(direction.ID) == id {
			return direction
		}
	}
	return nil
}

func traceMemoryPolicies() []string {
	return []string{
		"Memory is hypothesis input, not fact.",
		"Cards remain draft until review.",
		"Secrets and hidden reasoning are not exported.",
	}
}

func tracePromptSummary(prompt problemtrace.PromptSnapshot) string {
	return fmt.Sprintf("%s step=%d", prompt.ID, prompt.Step)
}

func isCompactPromptBlock(kind string) bool {
	switch kind {
	case "frontier", "memory_context", "recent_observations", "conversation_state":
		return true
	default:
		return false
	}
}

type indexedEvent struct {
	Index int
	Event core.Event
}

type TraceEventRowVM struct {
	Index   int
	Step    int
	Type    string
	Kind    string
	Icon    string
	Status  string
	Title   string
	Summary string
	Tool    string
	Code    int
	Event   core.Event
}

func buildTraceEventRows(events []core.Event, debug bool) []TraceEventRowVM {
	items := indexedEvents(events)
	if !debug {
		items = keyTraceEvents(events)
	}

	rows := make([]TraceEventRowVM, 0, len(items))
	for _, item := range items {
		rows = append(rows, projectTraceEvent(item))
	}
	inferTraceEventSteps(rows)
	return rows
}

func inferTraceEventSteps(rows []TraceEventRowVM) {
	currentStep := 0
	openActionStep := 0
	stepOpen := false
	for i := range rows {
		explicitStep := traceEventExplicitStep(rows[i].Event)
		if explicitStep > 0 {
			rows[i].Step = explicitStep
			currentStep = max(currentStep, explicitStep)
			switch rows[i].Type {
			case "tool_call", "tool_proposed":
				openActionStep = explicitStep
				stepOpen = true
			case "tool_result", "tool_denied":
				openActionStep = 0
				stepOpen = false
			}
			continue
		}

		switch rows[i].Type {
		case "model_request":
			currentStep++
			rows[i].Step = currentStep
			stepOpen = true
		case "model_response":
			if currentStep == 0 || !stepOpen {
				currentStep++
			}
			rows[i].Step = currentStep
			stepOpen = true
		case "tool_proposed", "tool_call":
			if currentStep == 0 || !stepOpen {
				currentStep++
			}
			rows[i].Step = currentStep
			openActionStep = currentStep
			stepOpen = true
		case "tool_result", "tool_denied":
			if openActionStep > 0 {
				rows[i].Step = openActionStep
			} else {
				if currentStep == 0 {
					currentStep++
				}
				rows[i].Step = currentStep
			}
			openActionStep = 0
			stepOpen = false
		case "symptom_detected", "direction_created", "direction_updated", "evidence_added", "prompt_snapshot", "frontier_updated", "memory_card_generated":
			if currentStep > 0 {
				rows[i].Step = currentStep
			}
		}
	}
}

func traceEventExplicitStep(event core.Event) int {
	if step := intValue(event.Data["step"]); step > 0 {
		return step
	}
	if snapshot, ok := normalizeValue(event.Data["snapshot"]).(map[string]any); ok {
		return intValue(snapshot["step"])
	}
	return 0
}

func projectTraceEvent(item indexedEvent) TraceEventRowVM {
	event := item.Event
	row := TraceEventRowVM{
		Index:  item.Index,
		Type:   event.Type,
		Kind:   "debug",
		Icon:   ".",
		Status: "debug",
		Title:  event.Type,
		Event:  event,
	}

	switch event.Type {
	case "user_task":
		row.Kind = "task"
		row.Icon = "T"
		row.Status = "start"
		row.Title = "Task"
		row.Summary = traceEventSummary(event.Data["task"], 100)
	case "model_request":
		row.Kind = "ai"
		row.Icon = "A"
		row.Status = "request"
		row.Title = "AI request"
		row.Summary = traceEventModelRequestSummary(event)
	case "model_response":
		row.Kind = "ai"
		row.Icon = "A"
		row.Status = "plan"
		row.Title = "AI plan"
		row.Summary = traceEventSummary(stripFencedBlocks(traceEventText(event.Data["content"])), 120)
	case "tool_proposed":
		row.Kind = "approval"
		row.Icon = "?"
		row.Status = "proposed"
		row.Tool = traceEventText(event.Data["tool"])
		row.Title = "Tool proposed"
		row.Summary = valueOrDefault(row.Tool, "tool")
	case "tool_call":
		row.Kind = "action"
		row.Icon = ">"
		row.Status = "run"
		row.Tool = traceEventText(event.Data["tool"])
		row.Title = "Action"
		row.Summary = traceEventActionSummary(row.Tool, commandFromArgs(event.Data["args"]))
	case "tool_result":
		row.Kind = "result"
		row.Tool = traceEventText(event.Data["tool"])
		row.Code = intValue(event.Data["code"])
		if row.Code == 0 {
			row.Icon = "+"
			row.Status = "ok"
			row.Title = "Result"
		} else {
			row.Icon = "x"
			row.Status = "failed"
			row.Title = "Result"
		}
		if timedOut, ok := normalizeValue(event.Data["timed_out"]).(bool); ok && timedOut {
			row.Icon = "x"
			row.Status = "timeout"
			row.Title = "Result"
		}
		row.Summary = traceEventToolResultSummary(row)
	case "tool_denied":
		row.Kind = "approval"
		row.Icon = "x"
		row.Status = "denied"
		row.Tool = traceEventText(event.Data["tool"])
		row.Title = "Tool denied"
		row.Summary = valueOrDefault(traceEventSummary(event.Data["reason"], 100), valueOrDefault(row.Tool, "tool"))
	case "symptom_detected":
		row.Kind = "symptom"
		row.Icon = "!"
		row.Status = "observed"
		row.Title = "Symptom"
		row.Summary = traceEventSummary(nestedSummary(event.Data["symptom"], "summary"), 120)
	case "direction_created", "direction_updated":
		row.Kind = "direction"
		row.Icon = "D"
		row.Status = "updated"
		row.Title = "Direction"
		row.Summary = traceEventSummary(displayDirectionTitle(
			nestedSummary(event.Data["direction"], "id"),
			nestedSummary(event.Data["direction"], "hypothesis"),
		), 120)
	case "evidence_added":
		row.Kind = "evidence"
		row.Icon = "E"
		row.Status = "captured"
		row.Title = "Evidence"
		row.Summary = traceEventSummary(nestedSummary(event.Data["evidence"], "summary"), 120)
	case "error":
		row.Kind = "error"
		row.Icon = "x"
		row.Status = "error"
		row.Title = "Error"
		row.Summary = traceEventSummary(event.Data["error"], 120)
	case "final":
		row.Kind = "final"
		row.Icon = "+"
		row.Status = valueOrDefault(traceEventText(event.Data["status"]), "finished")
		row.Title = "Final"
		row.Summary = row.Status
	case "problem_trace_initialized":
		row.Kind = "task"
		row.Icon = "T"
		row.Status = "trace"
		row.Title = "Trace started"
		row.Summary = traceEventSummary(event.Data["trace_id"], 100)
	case "trace_span_ended":
		row.Kind = "debug"
		row.Icon = "S"
		row.Status = "span"
		row.Title = "Trace span"
		row.Summary = traceEventSpanSummary(event)
	case "prompt_snapshot":
		row.Kind = "ai"
		row.Icon = "P"
		row.Status = "prompt"
		row.Title = "Prompt"
		row.Summary = traceEventSnapshotSummary(event)
	case "frontier_updated":
		row.Kind = "direction"
		row.Icon = "D"
		row.Status = "frontier"
		row.Title = "Frontier"
		row.Summary = traceEventFrontierSummary(event)
	case "memory_card_generated":
		row.Kind = "evidence"
		row.Icon = "M"
		row.Status = "memory"
		row.Title = "Memory card"
		row.Summary = traceEventMemoryCardSummary(event)
	default:
		row.Summary = traceEventDefaultSummary(event)
	}

	if row.Summary == "" {
		row.Summary = traceEventTraceSummary(event)
	}
	return row
}

func renderTraceEventsWorkspace(b *strings.Builder, events []core.Event, state traceWorkspaceState, width int, height int) {
	rows := buildTraceEventRows(events, state.Debug)
	if width < 120 {
		renderTraceEventListOnly(b, rows, state.EventCursor, width, height)
		return
	}

	const gap = 3
	leftWidth, rightWidth := traceEventSplitWidths(width, gap)
	cursor := traceEventCursorForRows(state, rows)
	left := renderTraceEventList(rows, cursor, leftWidth, state.EventPane == tracePaneTree)

	var right string
	if len(rows) == 0 {
		right = "Selected Event\n\nNo event selected.\n"
	} else {
		right = renderTraceEventDetail(rows[cursor], state.EventTab, rightWidth, state.EventPane == tracePaneDetail)
	}

	panelHeight := traceEventSplitPanelHeight(left, right, height)
	left = fitEventListAroundCursor(left, panelHeight, traceEventDisplayCursor(rows, cursor))
	right = fitHeightOffset(right, panelHeight, state.EventOffset)

	b.WriteString(renderSplitPanels(left, right, leftWidth, rightWidth))
}

func renderTraceEventListOnly(b *strings.Builder, rows []TraceEventRowVM, cursor int, width int, height int) {
	cursor = traceEventCursorForRows(traceWorkspaceState{EventCursor: cursor}, rows)
	content := renderTraceEventList(rows, cursor, width, true)
	if height > 0 {
		content = fitEventListAroundCursor(content, max(8, height-4), traceEventDisplayCursor(rows, cursor))
	}
	b.WriteString(content)
}

func renderTraceEventList(rows []TraceEventRowVM, cursor int, width int, active bool) string {
	var b strings.Builder
	title := "Event Stream"
	b.WriteString(renderTracePaneTitle(title, active))
	b.WriteByte('\n')
	b.WriteString("j/k move  pg scroll  l detail  o open  [/] tabs  D debug\n\n")

	if len(rows) == 0 {
		b.WriteString("No key events recorded.\n")
		return b.String()
	}

	cursor = clamp(cursor, 0, len(rows)-1)
	lastGroup := ""
	for i, row := range rows {
		if group := traceEventGroupLabel(row); group != "" && group != lastGroup {
			b.WriteString(mutedStyle.Render(group))
			b.WriteByte('\n')
			lastGroup = group
		}

		prefix := "  "
		if i == cursor {
			prefix = "> "
		}

		line := fmt.Sprintf(
			"%s#%-3d %-2s %-11s %s",
			prefix,
			row.Index+1,
			row.Icon,
			truncateDisplay(row.Title, 11),
			row.Summary,
		)
		line = truncateDisplay(line, width)

		if i == cursor {
			line = traceSelectedStyle.Width(width).Render(line)
		} else {
			line = eventRowStyle(row).Render(line)
		}

		b.WriteString(line)
		b.WriteByte('\n')
	}

	return b.String()
}

func traceEventGroupLabel(row TraceEventRowVM) string {
	if row.Step > 0 {
		return fmt.Sprintf("Step %d", row.Step)
	}
	if row.Kind == "final" {
		return "Final"
	}
	return ""
}

func traceEventDisplayCursor(rows []TraceEventRowVM, cursor int) int {
	if len(rows) == 0 {
		return 0
	}
	cursor = clamp(cursor, 0, len(rows)-1)
	line := 0
	lastGroup := ""
	for i, row := range rows {
		if group := traceEventGroupLabel(row); group != "" && group != lastGroup {
			line++
			lastGroup = group
		}
		if i == cursor {
			return line
		}
		line++
	}
	return line
}

func renderTraceEventDetail(row TraceEventRowVM, tab traceEventTab, width int, active bool) string {
	var b strings.Builder
	title := "Selected Event"
	b.WriteString(renderTracePaneTitle(title, active))
	b.WriteByte('\n')
	b.WriteString("j/k scroll  pg page  h stream  [/] tabs\n")
	b.WriteString(traceEventTabs(tab))
	b.WriteString("\n\n")

	switch tab {
	case traceEventData:
		renderTraceEventData(&b, row, width)
	case traceEventTrace:
		renderTraceEventTrace(&b, row, width)
	case traceEventRaw:
		renderTraceEventRaw(&b, row, width)
	default:
		renderTraceEventOverview(&b, row, width)
	}

	return b.String()
}

func renderTraceEventOverview(b *strings.Builder, row TraceEventRowVM, width int) {
	writeField(b, "Event", fmt.Sprintf("#%d %s", row.Index+1, row.Type), width)
	if !row.Event.Time.IsZero() {
		writeField(b, "Time", row.Event.Time.Format("2006-01-02 15:04:05"), width)
	}
	if row.Step > 0 {
		writeField(b, "Step", row.Step, width)
	}
	writeField(b, "Kind", row.Kind, width)
	writeField(b, "Status", row.Status, width)
	writeField(b, "Title", row.Title, width)
	writeField(b, "Summary", row.Summary, width)

	switch row.Type {
	case "user_task":
		writeField(b, "Task", row.Event.Data["task"], width)
		writeField(b, "Repository", row.Event.Data["repo"], width)
	case "model_request":
		writeField(b, "Step", row.Event.Data["step"], width)
		writeField(b, "Prompt", row.Event.Data["prompt_snapshot_id"], width)
	case "model_response":
		writeField(b, "AI said", stripFencedBlocks(traceEventText(row.Event.Data["content"])), width)
	case "tool_proposed":
		writeField(b, "Tool", row.Event.Data["tool"], width)
		writeField(b, "Risk", row.Event.Data["risk"], width)
	case "tool_call":
		writeField(b, "Tool", row.Event.Data["tool"], width)
		writeField(b, "Command", commandFromArgs(row.Event.Data["args"]), width)
	case "tool_result":
		writeField(b, "Tool", row.Event.Data["tool"], width)
		writeField(b, "Code", row.Event.Data["code"], width)
		writeField(b, "Timed Out", row.Event.Data["timed_out"], width)
		writeField(b, "Output", eventOutput(row.Event.Data), width)
	case "tool_denied":
		writeField(b, "Tool", row.Event.Data["tool"], width)
		writeField(b, "Reason", row.Event.Data["reason"], width)
	case "symptom_detected":
		writeField(b, "Symptom", nestedSummary(row.Event.Data["symptom"], "summary"), width)
	case "direction_created", "direction_updated":
		writeField(b, "Direction", displayDirectionTitle(
			nestedSummary(row.Event.Data["direction"], "id"),
			nestedSummary(row.Event.Data["direction"], "hypothesis"),
		), width)
	case "evidence_added":
		writeField(b, "Evidence", nestedSummary(row.Event.Data["evidence"], "summary"), width)
	case "error":
		writeField(b, "Error", row.Event.Data["error"], width)
	case "final":
		writeField(b, "Steps", row.Event.Data["steps"], width)
		writeField(b, "Submission", row.Event.Data["submission"], width)
	}
	renderTraceEventRelated(b, row, width)
}

func renderTraceEventRelated(b *strings.Builder, row TraceEventRowVM, width int) {
	fields := []struct {
		key   string
		value any
	}{}
	add := func(key string, value any) {
		if strings.TrimSpace(fmt.Sprint(value)) == "" {
			return
		}
		fields = append(fields, struct {
			key   string
			value any
		}{key, value})
	}
	if tc, ok := traceEventContext(row.Event); ok {
		add("Trace ID", tc.TraceID)
		add("Span", tc.SpanID)
		add("Parent Span", tc.ParentSpanID)
		add("Direction", tc.DirectionID)
		add("Prompt", tc.PromptSnapshotID)
		if len(tc.MemoryIDs) > 0 {
			add("Memories", strings.Join(tc.MemoryIDs, ", "))
		}
	} else if traceID := traceEventText(row.Event.Data["trace_id"]); traceID != "" {
		add("Trace ID", traceID)
	}
	if directionID := traceEventText(row.Event.Data["direction_id"]); directionID != "" {
		add("Direction", directionID)
	}
	if promptID := traceEventText(row.Event.Data["prompt_snapshot_id"]); promptID != "" {
		add("Prompt", promptID)
	}

	if len(fields) == 0 {
		return
	}
	writeSection(b, "Related")
	for _, field := range fields {
		writeField(b, field.key, field.value, width)
	}
}

func traceEventContext(event core.Event) (problemtrace.TraceContext, bool) {
	var tc problemtrace.TraceContext
	if problemtraceDecode(event.Data["trace_context"], &tc) {
		return tc, true
	}
	return problemtrace.TraceContext{}, false
}

func renderTraceEventData(b *strings.Builder, row TraceEventRowVM, width int) {
	if len(row.Event.Data) == 0 {
		b.WriteString("No structured data for this event.\n")
		return
	}
	writeValueTree(b, "", row.Event.Data, 0, width)
}

func renderTraceEventTrace(b *strings.Builder, row TraceEventRowVM, width int) {
	if tc, ok := traceEventContext(row.Event); ok {
		writeSection(b, "Trace Context")
		writeField(b, "Trace ID", tc.TraceID, width)
		writeField(b, "Span ID", tc.SpanID, width)
		writeField(b, "Parent Span", tc.ParentSpanID, width)
		writeField(b, "Direction", tc.DirectionID, width)
		writeField(b, "Prompt", tc.PromptSnapshotID, width)
		if len(tc.MemoryIDs) > 0 {
			writeField(b, "Memories", strings.Join(tc.MemoryIDs, ", "), width)
		}
		if tc.Flags.Recording || tc.Flags.Sampled {
			writeField(b, "Recording", tc.Flags.Recording, width)
			writeField(b, "Sampled", tc.Flags.Sampled, width)
		}
		return
	}
	if traceID := traceEventText(row.Event.Data["trace_id"]); traceID != "" {
		writeSection(b, "Trace Context")
		writeField(b, "Trace ID", traceID, width)
		return
	}
	b.WriteString("No trace context linked to this event.\n")
}

func renderTraceEventRaw(b *strings.Builder, row TraceEventRowVM, width int) {
	raw := map[string]any{
		"index": row.Index + 1,
		"type":  row.Type,
	}
	if !row.Event.Time.IsZero() {
		raw["time"] = row.Event.Time.Format("2006-01-02T15:04:05Z07:00")
	}
	if len(row.Event.Data) > 0 {
		raw["data"] = row.Event.Data
	}
	writeValueTree(b, "", raw, 0, width)
}

func traceEventTabs(active traceEventTab) string {
	items := []traceEventTab{
		traceEventOverview,
		traceEventData,
		traceEventTrace,
		traceEventRaw,
	}

	parts := make([]string, 0, len(items))
	for _, item := range items {
		label := traceEventTabLabel(item)
		if item == active {
			label = "[" + label + "]"
		}
		parts = append(parts, label)
	}
	return strings.Join(parts, "  ")
}

func traceEventTabLabel(tab traceEventTab) string {
	switch tab {
	case traceEventData:
		return "Data"
	case traceEventTrace:
		return "Trace"
	case traceEventRaw:
		return "Raw"
	default:
		return "Overview"
	}
}

func traceEventSplitWidths(width int, gap int) (int, int) {
	return splitPanelWidths(width, gap, 55, 52, 40)
}

func traceEventSplitPanelHeight(left string, right string, height int) int {
	return splitPanelHeight(left, right, height, 4)
}

func traceEventDetailMaxOffset(events []core.Event, state traceWorkspaceState, width int, height int) int {
	if width < 120 {
		return 0
	}
	rows := buildTraceEventRows(events, state.Debug)
	if len(rows) == 0 {
		return 0
	}
	const gap = 3
	leftWidth, rightWidth := traceEventSplitWidths(width, gap)
	cursor := traceEventCursorForRows(state, rows)
	left := renderTraceEventList(rows, cursor, leftWidth, state.EventPane == tracePaneTree)
	right := renderTraceEventDetail(rows[cursor], state.EventTab, rightWidth, state.EventPane == tracePaneDetail)
	panelHeight := traceEventSplitPanelHeight(left, right, height)
	return max(0, lipgloss.Height(right)-panelHeight)
}

func traceEventCursorForRows(state traceWorkspaceState, rows []TraceEventRowVM) int {
	if len(rows) == 0 {
		return 0
	}
	return clamp(state.EventCursor, 0, len(rows)-1)
}

func fitEventListAroundCursor(content string, height int, cursor int) string {
	return fitListAroundCursor(content, height, cursor)
}

func eventRowStyle(row TraceEventRowVM) lipgloss.Style {
	switch row.Kind {
	case "task":
		return traceProblemStyle
	case "ai":
		return traceThoughtStyle
	case "action":
		return traceActionStyle
	case "result":
		if row.Status == "failed" || row.Status == "error" || row.Status == "timeout" {
			return traceErrorStyle
		}
		return traceObserveStyle
	case "symptom":
		return traceErrorStyle
	case "direction":
		return traceDirectionStyle
	case "evidence":
		return traceEvidenceStyle
	case "final":
		return traceFixStyle
	default:
		return traceDefaultStyle
	}
}

func traceEventSummary(value any, limit int) string {
	text := shortString(value, limit)
	if text == "<nil>" {
		return ""
	}
	return text
}

func traceEventText(value any) string {
	text := strings.TrimSpace(formatScalar(normalizeValue(value)))
	if text == "<nil>" {
		return ""
	}
	return text
}

func traceEventActionSummary(tool string, command string) string {
	if command != "" {
		return traceEventSummary(command, 120)
	}
	return valueOrDefault(tool, "tool")
}

func traceEventToolResultSummary(row TraceEventRowVM) string {
	tool := valueOrDefault(row.Tool, "tool")
	if row.Event.Data["code"] == nil {
		if row.Status == "timeout" {
			return tool + " timeout"
		}
		return tool
	}
	summary := fmt.Sprintf("%s code=%d", tool, row.Code)
	if row.Status == "timeout" {
		summary += " timeout"
	}
	return summary
}

func traceEventModelRequestSummary(event core.Event) string {
	parts := []string{}
	if step := traceEventText(event.Data["step"]); step != "" {
		parts = append(parts, "step="+step)
	}
	if promptID := traceEventText(event.Data["prompt_snapshot_id"]); promptID != "" {
		parts = append(parts, "prompt="+promptID)
	}
	if len(parts) == 0 {
		return traceEventSummary(event.Data["model"], 100)
	}
	return strings.Join(parts, " ")
}

func traceEventSpanSummary(event core.Event) string {
	span, ok := normalizeValue(event.Data["span"]).(map[string]any)
	if !ok {
		return traceEventTraceSummary(event)
	}
	parts := []string{}
	for _, key := range []string{"name", "kind", "status", "span_id"} {
		if value := traceEventText(span[key]); value != "" {
			parts = append(parts, value)
		}
	}
	return traceEventSummary(strings.Join(parts, " "), 120)
}

func traceEventSnapshotSummary(event core.Event) string {
	snapshot, ok := normalizeValue(event.Data["snapshot"]).(map[string]any)
	if !ok {
		return traceEventTraceSummary(event)
	}
	parts := []string{}
	if id := traceEventText(snapshot["id"]); id != "" {
		parts = append(parts, id)
	}
	if step := traceEventText(snapshot["step"]); step != "" {
		parts = append(parts, "step="+step)
	}
	if tokens := traceEventText(snapshot["token_estimate"]); tokens != "" {
		parts = append(parts, "tokens="+tokens)
	}
	return traceEventSummary(strings.Join(parts, " "), 120)
}

func traceEventFrontierSummary(event core.Event) string {
	frontier, ok := normalizeValue(event.Data["frontier"]).(map[string]any)
	if !ok {
		return traceEventTraceSummary(event)
	}
	if active := traceEventText(frontier["active_direction_id"]); active != "" {
		return traceEventSummary("active "+active, 120)
	}
	return traceEventTraceSummary(event)
}

func traceEventMemoryCardSummary(event core.Event) string {
	card, ok := normalizeValue(event.Data["card"]).(map[string]any)
	if !ok {
		return traceEventTraceSummary(event)
	}
	return traceEventSummary(card["summary"], 120)
}

func traceEventTraceSummary(event core.Event) string {
	if summary := traceContextSummary(event.Data["trace_context"]); summary != "" {
		return traceEventSummary(summary, 120)
	}
	if traceID := traceEventText(event.Data["trace_id"]); traceID != "" {
		return traceEventSummary("trace="+traceID, 120)
	}
	return ""
}

func traceEventDefaultSummary(event core.Event) string {
	if summary := traceEventTraceSummary(event); summary != "" {
		return summary
	}
	if len(event.Data) == 0 {
		return ""
	}
	return traceEventSummary(formatArgsMap(event.Data), 120)
}

func truncateDisplay(s string, width int) string {
	s = formatDisplayText(s)
	if width <= 0 {
		return ""
	}
	if displayWidth(s) <= width {
		return s
	}
	if width <= 3 {
		var b strings.Builder
		for _, r := range s {
			if displayWidth(b.String()+string(r)) > width {
				break
			}
			b.WriteRune(r)
		}
		return b.String()
	}

	target := width - 3
	var b strings.Builder
	for _, r := range s {
		next := b.String() + string(r)
		if displayWidth(next) > target {
			break
		}
		b.WriteRune(r)
	}
	return strings.TrimRight(b.String(), " ") + "..."
}

func indexedEvents(events []core.Event) []indexedEvent {
	out := make([]indexedEvent, 0, len(events))
	for i, event := range events {
		out = append(out, indexedEvent{Index: i, Event: event})
	}
	return out
}

func keyTraceEvents(events []core.Event) []indexedEvent {
	keep := map[string]bool{
		"user_task":         true,
		"model_response":    true,
		"tool_proposed":     true,
		"tool_call":         true,
		"tool_result":       true,
		"tool_denied":       true,
		"symptom_detected":  true,
		"direction_created": true,
		"direction_updated": true,
		"evidence_added":    true,
		"error":             true,
		"final":             true,
	}
	var out []indexedEvent
	for i, event := range events {
		if keep[event.Type] {
			out = append(out, indexedEvent{Index: i, Event: event})
		}
	}
	return out
}

func activeDirectionSummary(trace problemtrace.ProblemTrace) string {
	activeID := strings.TrimSpace(trace.Frontier.ActiveDirectionID)
	if activeID == "" {
		return "none"
	}
	for _, direction := range trace.Directions {
		if strings.TrimSpace(direction.ID) == activeID {
			return displayDirectionTitle(direction.ID, direction.Hypothesis)
		}
	}
	return displayDirectionTitle(activeID, activeID)
}

func topActions(actions []problemtrace.NextAction, limit int) []problemtrace.NextAction {
	if limit <= 0 || len(actions) <= limit {
		return actions
	}
	return actions[:limit]
}

func topStrings(values []string, limit int) []string {
	if limit <= 0 || len(values) <= limit {
		return values
	}
	return values[:limit]
}

func displayDirectionTitle(id, title string) string {
	title = strings.TrimSpace(title)
	if isGenericObservationDirection(id, title) {
		return "Observation captured"
	}
	return title
}

func isGenericObservationDirection(id, title string) bool {
	id = strings.TrimSpace(strings.ToLower(id))
	title = strings.TrimSpace(strings.ToLower(title))
	return id == "node-dir-collect-current-repository-evidence" ||
		id == "dir-collect-current-repository-evidence" ||
		title == "collect current repository evidence"
}

func traceContextSummary(value any) string {
	var tc problemtrace.TraceContext
	if !problemtraceDecode(value, &tc) {
		return ""
	}
	parts := []string{}
	if tc.TraceID != "" {
		parts = append(parts, "trace="+tc.TraceID)
	}
	if tc.SpanID != "" {
		parts = append(parts, "span="+tc.SpanID)
	}
	if tc.DirectionID != "" {
		parts = append(parts, "dir="+tc.DirectionID)
	}
	if tc.PromptSnapshotID != "" {
		parts = append(parts, "prompt="+tc.PromptSnapshotID)
	}
	return strings.Join(parts, " ")
}

func nestedSummary(value any, key string) string {
	normalized := normalizeValue(value)
	if m, ok := normalized.(map[string]any); ok {
		return strings.TrimSpace(fmt.Sprint(m[key]))
	}
	return ""
}

func problemtraceDecode(value any, out any) bool {
	data, err := json.Marshal(value)
	if err != nil {
		return false
	}
	return json.Unmarshal(data, out) == nil
}
