package tui

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/local/swe-agent/internal/core"
	"github.com/local/swe-agent/internal/problemtrace"
)

func traceWorkspaceViewWidth(record taskRecord, state traceWorkspaceState, width int, trajectoryPath string) string {
	state.ensureDefaults()
	vm := buildTraceWorkspaceVM(record, state, trajectoryPath)
	var b strings.Builder
	b.WriteString("Problem Trace Workspace\n")
	b.WriteString(traceTabs(state.Tab))
	b.WriteString("\n\n")
	switch state.Tab {
	case traceTabFrontier:
		renderTraceFrontier(&b, vm.Trace, width, state.Debug)
	case traceTabMemory:
		renderTraceMemory(&b, vm.Trace, width, state.Debug)
	case traceTabEvents:
		renderTraceEvents(&b, record.Events, width, state.Debug)
	case traceTabPrompt:
		renderTracePrompts(&b, vm.Trace, width, state.Debug)
	case traceTabCards:
		renderTraceCards(&b, vm.Trace, width, state.Debug)
	default:
		renderTraceTreeTab(&b, vm, state, width, record)
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

func renderTraceFrontier(b *strings.Builder, trace problemtrace.ProblemTrace, width int, debug bool) {
	writeField(b, "Active", activeDirectionSummary(trace), width)

	writeSection(b, "Next")
	if len(trace.Frontier.RecommendedActions) == 0 {
		b.WriteString("No recommended action yet.\n")
	} else {
		for _, action := range topActions(trace.Frontier.RecommendedActions, 3) {
			b.WriteString(wrapText(fmt.Sprintf("- %s", action.Action), width))
			b.WriteByte('\n')
			if debug && action.Rationale != "" {
				b.WriteString(indentText(wrapText(action.Rationale, remainingWidth(width, 2)), 2))
				b.WriteByte('\n')
			}
			if debug {
				for _, expected := range topStrings(action.ExpectedEvidence, 3) {
					b.WriteString(indentText(wrapText("expected: "+expected, remainingWidth(width, 2)), 2))
					b.WriteByte('\n')
				}
			}
		}
	}
	renderStringList(b, "Open", topStrings(trace.Frontier.OpenQuestions, 3), width)
	if !debug {
		return
	}

	renderDirectionsDebug(b, trace, width)
	renderStringList(b, "Stop Conditions", trace.Frontier.StopConditions, width)
	renderStringList(b, "Risks", trace.Frontier.Risks, width)
}

func renderDirectionsDebug(b *strings.Builder, trace problemtrace.ProblemTrace, width int) {
	if len(trace.Directions) == 0 {
		return
	}
	writeSection(b, "Directions")
	for _, direction := range trace.Directions {
		line := fmt.Sprintf("%s  %s  priority=%d", direction.Status, displayDirectionTitle(direction.ID, direction.Hypothesis), direction.Priority)
		b.WriteString(wrapText(line, width))
		b.WriteByte('\n')
		if direction.Rationale != "" {
			b.WriteString(indentText(wrapText(direction.Rationale, remainingWidth(width, 2)), 2))
			b.WriteByte('\n')
		}
		for _, evidence := range direction.SupportingEvidence {
			b.WriteString(indentText(wrapText("supports: "+evidence.Summary, remainingWidth(width, 2)), 2))
			b.WriteByte('\n')
		}
		for _, evidence := range direction.RefutingEvidence {
			b.WriteString(indentText(wrapText("refutes: "+evidence.Summary, remainingWidth(width, 2)), 2))
			b.WriteByte('\n')
		}
	}
}

func renderTraceMemory(b *strings.Builder, trace problemtrace.ProblemTrace, width int, debug bool) {
	writeSection(b, "Memory")
	if len(trace.Memories) == 0 {
		b.WriteString("No memory used in this run.\n")
		if debug {
			writeSection(b, "Policy")
			for _, line := range []string{
				"Memory is hypothesis input, not fact.",
				"Cards remain draft until review.",
				"Secrets and hidden reasoning are not exported.",
			} {
				b.WriteString(wrapText("- "+line, width))
				b.WriteByte('\n')
			}
		}
		return
	}
	for _, memory := range trace.Memories {
		status := valueOrDefault(memory.Status, "used")
		line := fmt.Sprintf("%s  %.2f  %s", status, memory.Similarity, memory.Summary)
		b.WriteString(wrapText(line, width))
		b.WriteByte('\n')
		if debug && memory.Reason != "" {
			b.WriteString(indentText(wrapText(memory.Reason, remainingWidth(width, 2)), 2))
			b.WriteByte('\n')
		}
	}
	if debug {
		writeSection(b, "Policy")
		for _, line := range []string{
			"Memory is hypothesis input, not fact.",
			"Cards remain draft until review.",
			"Secrets and hidden reasoning are not exported.",
		} {
			b.WriteString(wrapText("- "+line, width))
			b.WriteByte('\n')
		}
	}
}

type indexedEvent struct {
	Index int
	Event core.Event
}

func renderTraceEvents(b *strings.Builder, events []core.Event, width int, debug bool) {
	if len(events) == 0 {
		b.WriteString("No events recorded.\n")
		return
	}
	items := indexedEvents(events)
	if !debug {
		items = keyTraceEvents(events)
	}
	if len(items) == 0 {
		b.WriteString("No key events recorded.\n")
		return
	}
	for _, item := range items {
		event := item.Event
		line := fmt.Sprintf("#%d %s", item.Index+1, event.Type)
		line = appendCompactEventSummary(line, event)
		if debug {
			if tc, ok := event.Data["trace_context"]; ok {
				line += "  " + shortString(traceContextSummary(tc), 80)
			} else if traceID := strings.TrimSpace(fmt.Sprint(event.Data["trace_id"])); traceID != "" && traceID != "<nil>" {
				line += "  trace=" + traceID
			}
		}
		b.WriteString(wrapText(line, width))
		b.WriteByte('\n')
		if !debug {
			continue
		}
		switch event.Type {
		case "tool_call":
			writeField(b, "Tool", event.Data["tool"], width)
		case "tool_result":
			writeField(b, "Tool", event.Data["tool"], width)
			writeField(b, "Code", event.Data["code"], width)
		case "model_request":
			writeField(b, "Step", event.Data["step"], width)
			writeField(b, "Prompt", event.Data["prompt_snapshot_id"], width)
		case "symptom_detected":
			writeField(b, "Symptom", nestedSummary(event.Data["symptom"], "summary"), width)
		case "direction_created":
			writeField(b, "Direction", displayDirectionTitle(nestedSummary(event.Data["direction"], "id"), nestedSummary(event.Data["direction"], "hypothesis")), width)
		}
	}
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

func appendCompactEventSummary(line string, event core.Event) string {
	switch event.Type {
	case "user_task":
		return line + compactSuffix(event.Data["task"])
	case "model_response":
		return line + compactSuffix(event.Data["content"])
	case "tool_call":
		return line + compactSuffix(event.Data["tool"])
	case "tool_result":
		out := line + compactSuffix(event.Data["tool"])
		if code := intValue(event.Data["code"]); code != 0 || event.Data["code"] != nil {
			out += fmt.Sprintf(" code=%d", code)
		}
		return out
	case "tool_denied":
		return line + compactSuffix(event.Data["tool"])
	case "symptom_detected":
		return line + compactSuffix(nestedSummary(event.Data["symptom"], "summary"))
	case "direction_created", "direction_updated":
		return line + compactSuffix(displayDirectionTitle(nestedSummary(event.Data["direction"], "id"), nestedSummary(event.Data["direction"], "hypothesis")))
	case "evidence_added":
		return line + compactSuffix(nestedSummary(event.Data["evidence"], "summary"))
	case "error":
		return line + compactSuffix(event.Data["error"])
	case "final":
		return line + compactSuffix(event.Data["status"])
	default:
		return line
	}
}

func compactSuffix(value any) string {
	text := shortString(value, 120)
	if text == "" || text == "<nil>" {
		return ""
	}
	return " " + text
}

func renderTracePrompts(b *strings.Builder, trace problemtrace.ProblemTrace, width int, debug bool) {
	if len(trace.Prompts) == 0 {
		b.WriteString("No prompt snapshots recorded.\n")
		return
	}
	if !debug {
		prompt := trace.Prompts[len(trace.Prompts)-1]
		writeSection(b, fmt.Sprintf("Latest Prompt: %s step=%d", prompt.ID, prompt.Step))
		writeField(b, "Tokens", prompt.TokenEstimate, width)
		writeField(b, "Messages", prompt.MessageCount, width)
		writeField(b, "Tools", prompt.ToolCount, width)

		writeSection(b, "Context")
		for _, block := range prompt.Blocks {
			switch block.Kind {
			case "frontier", "memory_context", "recent_observations", "conversation_state":
				status := "no"
				if block.Included {
					status = "yes"
				}
				line := fmt.Sprintf("- %s: %s", valueOrDefault(block.Title, block.Kind), status)
				if block.Count > 0 {
					line += fmt.Sprintf(" count=%d", block.Count)
				}
				b.WriteString(wrapText(line, width))
				b.WriteByte('\n')
			}
		}
		return
	}
	for _, prompt := range trace.Prompts {
		writeSection(b, fmt.Sprintf("%s step=%d", prompt.ID, prompt.Step))
		writeField(b, "Model", prompt.Model, width)
		writeField(b, "Messages", prompt.MessageCount, width)
		writeField(b, "Tools", prompt.ToolCount, width)
		writeField(b, "Token Estimate", prompt.TokenEstimate, width)
		for _, block := range prompt.Blocks {
			status := "no"
			if block.Included {
				status = "yes"
			}
			line := fmt.Sprintf("- %s included=%s count=%d", valueOrDefault(block.Title, block.Kind), status, block.Count)
			b.WriteString(wrapText(line, width))
			b.WriteByte('\n')
			if block.Summary != "" {
				b.WriteString(indentText(wrapText(block.Summary, remainingWidth(width, 2)), 2))
				b.WriteByte('\n')
			}
		}
	}
}

func renderTraceCards(b *strings.Builder, trace problemtrace.ProblemTrace, width int, debug bool) {
	if len(trace.Cards) == 0 {
		b.WriteString("No draft memory cards yet. Cards are generated when the run finishes.\n")
		return
	}
	writeSection(b, "Draft Memory Cards")
	for _, card := range trace.Cards {
		header := fmt.Sprintf("[%s %s]", valueOrDefault(card.Kind, "card"), valueOrDefault(card.Status, "draft"))
		if debug && card.ID != "" {
			header += " " + card.ID
		}
		writeSection(b, header)
		if card.Summary != "" {
			b.WriteString(wrapText(card.Summary, width))
			b.WriteByte('\n')
		}
		if card.FixPattern != "" {
			writeField(b, "fix", card.FixPattern, width)
		}
		if card.Verification != "" {
			writeField(b, "verification", card.Verification, width)
		}
		for _, evidence := range card.Evidence {
			b.WriteString(indentText(wrapText("evidence: "+evidence, remainingWidth(width, 2)), 2))
			b.WriteByte('\n')
		}
	}
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

func renderStringList(b *strings.Builder, title string, values []string, width int) {
	writeSection(b, title)
	if len(values) == 0 {
		b.WriteString("none\n")
		return
	}
	for _, value := range values {
		b.WriteString(wrapText("- "+value, width))
		b.WriteByte('\n')
	}
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
