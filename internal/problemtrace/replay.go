package problemtrace

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/local/swe-agent/internal/core"
)

func FromEvents(events []core.Event) ProblemTrace {
	trace := ProblemTrace{}
	hasTraceNodeEvents := false
	for i, event := range events {
		switch event.Type {
		case "user_task":
			if trace.TraceID == "" {
				trace.TraceID = "trace-from-events"
				trace.RunID = trace.TraceID
				trace.CreatedAt = event.Time
			}
			if trace.Problem.UserTask == "" {
				trace.Problem.UserTask = stringData(event.Data, "task")
			}
			if trace.Problem.Repo == "" {
				trace.Problem.Repo = stringData(event.Data, "repo")
			}
		case "problem_trace_initialized":
			trace.TraceID = valueOr(stringData(event.Data, "trace_id"), trace.TraceID)
			trace.RunID = trace.TraceID
			decodeInto(event.Data["problem"], &trace.Problem)
			decodeInto(event.Data["resource"], &trace.Resource)
			if trace.CreatedAt.IsZero() {
				trace.CreatedAt = event.Time
			}
		case "trace_span_started", "trace_span_ended":
			var span TraceSpan
			if decodeInto(event.Data["span"], &span) && span.SpanID != "" {
				upsertSpan(&trace, span)
			}
		case "prompt_snapshot":
			var snapshot PromptSnapshot
			if decodeInto(event.Data["snapshot"], &snapshot) && snapshot.ID != "" && !hasPrompt(trace, snapshot.ID) {
				trace.Prompts = append(trace.Prompts, snapshot)
			}
		case "trace_node_added":
			var node TraceNode
			if decodeInto(event.Data["node"], &node) && strings.TrimSpace(node.ID) != "" && !hasTraceNode(trace, node.ID) {
				hasTraceNodeEvents = true
				trace.History = append(trace.History, cloneReplayTraceNode(node))
			}
		case "symptom_detected":
			var symptom Symptom
			if decodeInto(event.Data["symptom"], &symptom) && symptom.ID != "" && !hasSymptom(trace, symptom.ID) {
				symptom.EventIDs = appendIfMissing(symptom.EventIDs, i)
				trace.Symptoms = append(trace.Symptoms, symptom)
				if trace.Problem.ErrorSummary == "" {
					trace.Problem.ErrorSummary = symptom.Summary
				}
			}
		case "direction_created", "direction_updated":
			var direction InvestigationDirection
			if decodeInto(event.Data["direction"], &direction) && direction.ID != "" {
				upsertDirection(&trace, direction)
			}
		case "evidence_added":
			var evidence Evidence
			if decodeInto(event.Data["evidence"], &evidence) && evidence.ID != "" {
				directionID := stringData(event.Data, "direction_id")
				addEvidence(&trace, directionID, evidence, i)
			}
		case "frontier_updated":
			var frontier InvestigationFrontier
			if decodeInto(event.Data["frontier"], &frontier) {
				trace.Frontier = frontier
			}
		case "memory_card_generated":
			var card MemoryCard
			if decodeInto(event.Data["card"], &card) && card.ID != "" && !hasCard(trace, card.ID) {
				trace.Cards = append(trace.Cards, card)
			}
		}
		if !event.Time.IsZero() {
			trace.UpdatedAt = event.Time
		}
	}
	if trace.TraceID == "" {
		trace.TraceID = "trace-empty"
		trace.RunID = trace.TraceID
	}
	if !hasTraceNodeEvents {
		buildHistory(&trace, events)
	}
	if len(trace.Frontier.RecommendedActions) == 0 && len(trace.Directions) == 0 {
		trace.Frontier.RecommendedActions = []NextAction{{
			ID:       "next-reproduce",
			Action:   "Run or inspect the narrowest command that exposes the reported problem.",
			Tool:     "run_tests",
			Priority: 50,
		}}
		trace.Frontier.OpenQuestions = []string{"What exact observation reproduces the reported problem?"}
	}
	return trace
}

func Replay(events []core.Event) ProblemTrace {
	return FromEvents(events)
}

func buildHistory(trace *ProblemTrace, events []core.Event) {
	trace.History = nil
	trace.History = append(trace.History, TraceNode{
		ID:      "node-root",
		Kind:    "problem",
		Title:   "Problem",
		Summary: short(trace.Problem.UserTask, 240),
		Status:  valueOr(trace.Problem.ErrorSummary, "running"),
		Time:    trace.CreatedAt,
	})
	for _, symptom := range trace.Symptoms {
		trace.History = append(trace.History, TraceNode{
			ID:       "node-" + symptom.ID,
			ParentID: "node-root",
			Kind:     "symptom",
			Title:    symptom.Summary,
			Summary:  symptom.ErrorType,
			Status:   "observed",
			EventIDs: symptom.EventIDs,
			Time:     symptom.CreatedAt,
		})
	}
	for _, direction := range trace.Directions {
		trace.History = append(trace.History, TraceNode{
			ID:          "node-" + direction.ID,
			ParentID:    "node-root",
			Kind:        "direction",
			Title:       direction.Hypothesis,
			Summary:     direction.Rationale,
			Status:      string(direction.Status),
			DirectionID: direction.ID,
		})
		for _, evidence := range direction.SupportingEvidence {
			trace.History = append(trace.History, TraceNode{
				ID:          "node-" + evidence.ID,
				ParentID:    "node-" + direction.ID,
				Kind:        "evidence",
				Title:       evidence.Summary,
				Summary:     short(evidence.Detail, 240),
				Status:      "supports",
				EventIDs:    evidence.EventIDs,
				DirectionID: direction.ID,
				Time:        evidence.CreatedAt,
			})
		}
		for _, evidence := range direction.RefutingEvidence {
			trace.History = append(trace.History, TraceNode{
				ID:          "node-" + evidence.ID,
				ParentID:    "node-" + direction.ID,
				Kind:        "evidence",
				Title:       evidence.Summary,
				Summary:     short(evidence.Detail, 240),
				Status:      "refutes",
				EventIDs:    evidence.EventIDs,
				DirectionID: direction.ID,
				Time:        evidence.CreatedAt,
			})
		}
	}
	for _, prompt := range trace.Prompts {
		trace.History = append(trace.History, TraceNode{
			ID:       "node-" + prompt.ID,
			ParentID: "node-root",
			Kind:     "prompt",
			Title:    fmt.Sprintf("Prompt snapshot %d", prompt.Step),
			Summary:  fmt.Sprintf("%d messages, %d tools", prompt.MessageCount, prompt.ToolCount),
			Status:   "captured",
			PromptID: prompt.ID,
			Time:     prompt.CreatedAt,
		})
	}
	if len(events) > 0 {
		last := events[len(events)-1]
		trace.History = append(trace.History, TraceNode{
			ID:       "node-events",
			ParentID: "node-root",
			Kind:     "events",
			Title:    "Raw events",
			Summary:  fmt.Sprintf("%d events captured", len(events)),
			Status:   last.Type,
			EventIDs: []int{len(events) - 1},
			Time:     last.Time,
		})
	}
}

func upsertSpan(trace *ProblemTrace, span TraceSpan) {
	for _, link := range span.Links {
		upsertLink(trace, link)
	}
	for i := range trace.Spans {
		if trace.Spans[i].SpanID == span.SpanID {
			trace.Spans[i] = span
			return
		}
	}
	trace.Spans = append(trace.Spans, span)
}

func upsertLink(trace *ProblemTrace, link TraceLink) {
	if link.Kind == "" || (link.FromID == "" && link.SpanID == "") || link.ToID == "" {
		return
	}
	for _, existing := range trace.Links {
		if existing.TraceID == link.TraceID && existing.SpanID == link.SpanID && existing.FromID == link.FromID && existing.ToID == link.ToID && existing.Kind == link.Kind {
			return
		}
	}
	trace.Links = append(trace.Links, link)
}

func upsertDirection(trace *ProblemTrace, direction InvestigationDirection) {
	for i := range trace.Directions {
		if trace.Directions[i].ID == direction.ID {
			trace.Directions[i] = mergeDirection(trace.Directions[i], direction)
			return
		}
	}
	trace.Directions = append(trace.Directions, direction)
}

func mergeDirection(old, next InvestigationDirection) InvestigationDirection {
	if len(next.SupportingEvidence) == 0 {
		next.SupportingEvidence = old.SupportingEvidence
	}
	if len(next.RefutingEvidence) == 0 {
		next.RefutingEvidence = old.RefutingEvidence
	}
	return next
}

func addEvidence(trace *ProblemTrace, directionID string, evidence Evidence, eventID int) {
	if evidence.Relation == "" {
		evidence.Relation = EvidenceSupports
	}
	evidence.EventIDs = appendIfMissing(evidence.EventIDs, eventID)
	for i := range trace.Directions {
		if trace.Directions[i].ID != directionID {
			continue
		}
		if hasEvidence(trace.Directions[i], evidence.ID) {
			return
		}
		switch evidence.Relation {
		case EvidenceRefutes:
			trace.Directions[i].RefutingEvidence = append(trace.Directions[i].RefutingEvidence, evidence)
			if trace.Directions[i].Status != DirectionFixed {
				trace.Directions[i].Status = DirectionRefuted
			}
		default:
			trace.Directions[i].SupportingEvidence = append(trace.Directions[i].SupportingEvidence, evidence)
			if trace.Directions[i].Status == DirectionOpen || trace.Directions[i].Status == DirectionActive {
				trace.Directions[i].Status = DirectionSupported
			}
		}
		return
	}
	if directionID == "" {
		directionID = "dir-observation"
	}
	direction := InvestigationDirection{
		ID:         directionID,
		Hypothesis: "Interpret captured evidence",
		Status:     DirectionSupported,
	}
	if evidence.Relation == EvidenceRefutes {
		direction.Status = DirectionRefuted
		direction.RefutingEvidence = []Evidence{evidence}
	} else {
		direction.SupportingEvidence = []Evidence{evidence}
	}
	trace.Directions = append(trace.Directions, direction)
}

func hasEvidence(direction InvestigationDirection, evidenceID string) bool {
	for _, existing := range direction.SupportingEvidence {
		if existing.ID == evidenceID {
			return true
		}
	}
	for _, existing := range direction.RefutingEvidence {
		if existing.ID == evidenceID {
			return true
		}
	}
	return false
}

func hasPrompt(trace ProblemTrace, id string) bool {
	for _, item := range trace.Prompts {
		if item.ID == id {
			return true
		}
	}
	return false
}

func hasSymptom(trace ProblemTrace, id string) bool {
	for _, item := range trace.Symptoms {
		if item.ID == id {
			return true
		}
	}
	return false
}

func hasCard(trace ProblemTrace, id string) bool {
	for _, item := range trace.Cards {
		if item.ID == id {
			return true
		}
	}
	return false
}

func hasTraceNode(trace ProblemTrace, id string) bool {
	for _, item := range trace.History {
		if item.ID == id {
			return true
		}
	}
	return false
}

func cloneReplayTraceNode(node TraceNode) TraceNode {
	node.EventIDs = append([]int(nil), node.EventIDs...)
	return node
}

func appendIfMissing(values []int, value int) []int {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func decodeInto(value any, out any) bool {
	if value == nil {
		return false
	}
	data, err := json.Marshal(value)
	if err != nil {
		return false
	}
	if err := json.Unmarshal(data, out); err != nil {
		return false
	}
	return true
}

func stringData(data map[string]any, key string) string {
	if data == nil {
		return ""
	}
	value := strings.TrimSpace(fmt.Sprint(data[key]))
	if value == "<nil>" {
		return ""
	}
	return value
}

func FormatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}
