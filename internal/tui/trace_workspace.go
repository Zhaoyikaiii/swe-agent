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
		renderTraceFrontier(&b, vm.Trace, width)
	case traceTabMemory:
		renderTraceMemory(&b, vm.Trace, width)
	case traceTabEvents:
		renderTraceEvents(&b, record.Events, width)
	case traceTabPrompt:
		renderTracePrompts(&b, vm.Trace, width)
	case traceTabCards:
		renderTraceCards(&b, vm.Trace, width)
	default:
		renderTraceTreeTab(&b, vm, state, width)
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
		return "Frontier"
	case traceTabMemory:
		return "Memory"
	case traceTabEvents:
		return "Events"
	case traceTabPrompt:
		return "Prompt"
	case traceTabCards:
		return "Cards"
	default:
		return "Trace"
	}
}

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

func renderTraceFrontier(b *strings.Builder, trace problemtrace.ProblemTrace, width int) {
	writeField(b, "Active Direction", trace.Frontier.ActiveDirectionID, width)
	if len(trace.Directions) > 0 {
		writeSection(b, "Directions")
		for _, direction := range trace.Directions {
			line := fmt.Sprintf("%s  %s  priority=%d", direction.Status, direction.Hypothesis, direction.Priority)
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
	writeSection(b, "Recommended Actions")
	if len(trace.Frontier.RecommendedActions) == 0 {
		b.WriteString("No recommended actions yet.\n")
	} else {
		for _, action := range trace.Frontier.RecommendedActions {
			b.WriteString(wrapText(fmt.Sprintf("- %s", action.Action), width))
			b.WriteByte('\n')
			if action.Rationale != "" {
				b.WriteString(indentText(wrapText(action.Rationale, remainingWidth(width, 2)), 2))
				b.WriteByte('\n')
			}
			for _, expected := range action.ExpectedEvidence {
				b.WriteString(indentText(wrapText("expected: "+expected, remainingWidth(width, 2)), 2))
				b.WriteByte('\n')
			}
		}
	}
	renderStringList(b, "Open Questions", trace.Frontier.OpenQuestions, width)
	renderStringList(b, "Stop Conditions", trace.Frontier.StopConditions, width)
	renderStringList(b, "Risks", trace.Frontier.Risks, width)
}

func renderTraceMemory(b *strings.Builder, trace problemtrace.ProblemTrace, width int) {
	writeSection(b, "Memory Usage")
	if len(trace.Memories) == 0 {
		b.WriteString("No memory has been retrieved or injected for this run.\n")
	} else {
		for _, memory := range trace.Memories {
			line := fmt.Sprintf("%s  %s  %.2f", memory.Status, memory.Summary, memory.Similarity)
			b.WriteString(wrapText(line, width))
			b.WriteByte('\n')
			if memory.Reason != "" {
				b.WriteString(indentText(wrapText(memory.Reason, remainingWidth(width, 2)), 2))
				b.WriteByte('\n')
			}
		}
	}
	writeSection(b, "Memory Discipline")
	for _, line := range []string{
		"Retrieved memory is a source of hypotheses, not current-repository fact.",
		"Cards remain draft until the user reviews and saves them.",
		"Secrets, hidden reasoning, and unbounded stdout should not be exported.",
	} {
		b.WriteString(wrapText("- "+line, width))
		b.WriteByte('\n')
	}
}

func renderTraceEvents(b *strings.Builder, events []core.Event, width int) {
	if len(events) == 0 {
		b.WriteString("No events recorded.\n")
		return
	}
	for i, event := range events {
		line := fmt.Sprintf("#%d %s", i+1, event.Type)
		if tc, ok := event.Data["trace_context"]; ok {
			line += "  " + shortString(traceContextSummary(tc), 80)
		} else if traceID := strings.TrimSpace(fmt.Sprint(event.Data["trace_id"])); traceID != "" && traceID != "<nil>" {
			line += "  trace=" + traceID
		}
		b.WriteString(wrapText(line, width))
		b.WriteByte('\n')
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
			writeField(b, "Direction", nestedSummary(event.Data["direction"], "hypothesis"), width)
		}
	}
}

func renderTracePrompts(b *strings.Builder, trace problemtrace.ProblemTrace, width int) {
	if len(trace.Prompts) == 0 {
		b.WriteString("No prompt snapshots recorded.\n")
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

func renderTraceCards(b *strings.Builder, trace problemtrace.ProblemTrace, width int) {
	if len(trace.Cards) == 0 {
		b.WriteString("No draft memory cards yet. Cards are generated when the run finishes.\n")
		return
	}
	for _, card := range trace.Cards {
		writeSection(b, fmt.Sprintf("%s  %s  %s", card.ID, card.Kind, card.Status))
		b.WriteString(wrapText(card.Summary, width))
		b.WriteByte('\n')
		if card.FixPattern != "" {
			writeField(b, "Fix Pattern", card.FixPattern, width)
		}
		if card.Verification != "" {
			writeField(b, "Verification", card.Verification, width)
		}
		for _, evidence := range card.Evidence {
			b.WriteString(indentText(wrapText("evidence: "+evidence, remainingWidth(width, 2)), 2))
			b.WriteByte('\n')
		}
	}
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
