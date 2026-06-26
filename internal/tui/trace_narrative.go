package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/local/swe-agent/internal/core"
	"github.com/local/swe-agent/internal/problemtrace"
)

type TraceNarrativeNode struct {
	ID          string
	ParentID    string
	Kind        string
	Title       string
	Summary     string
	Status      string
	Step        int
	EventIDs    []int
	DirectionID string
	PromptID    string
}

type pendingTraceAction struct {
	NodeIndex int
	NodeID    string
	Tool      string
	Command   string
	CallEvent int
}

func buildTraceNarrativeNodes(record taskRecord, trace problemtrace.ProblemTrace) []problemtrace.TraceNode {
	root := traceNarrativeRoot(record, trace)
	nodes := []problemtrace.TraceNode{root}

	lastNoteID := ""
	lastNoteSummary := ""
	var pending *pendingTraceAction
	actionCount := 0

	for i, event := range record.Events {
		switch event.Type {
		case "model_response":
			note := visibleAgentNote(event)
			if note == "" {
				continue
			}
			step := narrativeStep(event, i)
			id := fmt.Sprintf("node-step-%d-note", step)
			if hasNarrativeNode(nodes, id) {
				id = fmt.Sprintf("%s-%d", id, i)
			}
			nodes = append(nodes, problemtrace.TraceNode{
				ID:          id,
				ParentID:    root.ID,
				Kind:        "thought",
				Title:       fmt.Sprintf("Step %d: AI plan", step),
				Summary:     note,
				Status:      "observed",
				EventIDs:    []int{i},
				DirectionID: traceContextDirection(event),
				PromptID:    traceContextPrompt(event),
			})
			lastNoteID = id
			lastNoteSummary = note
		case "tool_call":
			actionCount++
			tool := strings.TrimSpace(fmt.Sprint(event.Data["tool"]))
			command := commandFromArgs(event.Data["args"])
			id := fmt.Sprintf("node-action-%d", i)
			parentID := root.ID
			if lastNoteID != "" {
				parentID = lastNoteID
			}
			nodes = append(nodes, problemtrace.TraceNode{
				ID:          id,
				ParentID:    parentID,
				Kind:        "action",
				Title:       actionTitle(tool, command),
				Summary:     actionSummary(tool, command, lastNoteSummary),
				Status:      "running",
				EventIDs:    []int{i},
				DirectionID: traceContextDirection(event),
				PromptID:    traceContextPrompt(event),
			})
			pending = &pendingTraceAction{
				NodeIndex: len(nodes) - 1,
				NodeID:    id,
				Tool:      tool,
				Command:   command,
				CallEvent: i,
			}
		case "tool_result":
			status := toolResultStatus(event)
			if pending == nil {
				tool := strings.TrimSpace(fmt.Sprint(event.Data["tool"]))
				id := fmt.Sprintf("node-action-%d-orphan", i)
				nodes = append(nodes, problemtrace.TraceNode{
					ID:          id,
					ParentID:    root.ID,
					Kind:        "action",
					Title:       actionTitle(tool, ""),
					Summary:     "A tool result was recorded without a visible matching call.",
					Status:      status,
					EventIDs:    []int{i},
					DirectionID: traceContextDirection(event),
					PromptID:    traceContextPrompt(event),
				})
				pending = &pendingTraceAction{NodeIndex: len(nodes) - 1, NodeID: id, Tool: tool, CallEvent: i}
			}
			if pending.NodeIndex >= 0 && pending.NodeIndex < len(nodes) {
				nodes[pending.NodeIndex].Status = status
			}
			nodes = append(nodes, problemtrace.TraceNode{
				ID:          fmt.Sprintf("node-observation-%d", i),
				ParentID:    pending.NodeID,
				Kind:        "observation",
				Title:       observationTitle(pending.Tool, event),
				Summary:     observationSummary(event),
				Status:      status,
				EventIDs:    compactEventIDs([]int{pending.CallEvent, i}),
				DirectionID: traceContextDirection(event),
				PromptID:    traceContextPrompt(event),
			})
			pending = nil
		case "tool_denied":
			tool := strings.TrimSpace(fmt.Sprint(event.Data["tool"]))
			id := fmt.Sprintf("node-action-denied-%d", i)
			nodes = append(nodes, problemtrace.TraceNode{
				ID:       id,
				ParentID: root.ID,
				Kind:     "action",
				Title:    actionTitle(tool, ""),
				Summary:  "The requested tool action was denied by policy.",
				Status:   "blocked",
				EventIDs: []int{i},
			})
			nodes = append(nodes, problemtrace.TraceNode{
				ID:       fmt.Sprintf("node-observation-denied-%d", i),
				ParentID: id,
				Kind:     "observation",
				Title:    "tool denied",
				Summary:  shortString(event.Data["reason"], 240),
				Status:   "blocked",
				EventIDs: []int{i},
			})
		}
	}

	nodes = appendTraceFacts(nodes, root.ID, trace, actionCount > 0)
	if len(nodes) == 1 {
		fallback := fallbackNarrativeFromHistory(root, trace.History)
		if len(fallback) > 1 {
			return fallback
		}
	}
	nodes = appendReviewNode(nodes, root.ID, record, trace)
	return nodes
}

func traceNarrativeRoot(record taskRecord, trace problemtrace.ProblemTrace) problemtrace.TraceNode {
	task := valueOrDefault(record.Task.Text, trace.Problem.UserTask)
	return problemtrace.TraceNode{
		ID:       "node-root",
		Kind:     "task",
		Title:    "Task",
		Summary:  shortString(task, 240),
		Status:   narrativeRunStatus(record, trace),
		EventIDs: taskEventIDs(record.Events),
	}
}

func narrativeRunStatus(record taskRecord, trace problemtrace.ProblemTrace) string {
	for _, value := range []string{
		record.Status,
		record.Result.Status,
		strings.TrimSpace(fmt.Sprint(lastEventData(record.Events, "final", "status"))),
		traceRootStatus(trace.History),
	} {
		value = strings.TrimSpace(value)
		if value != "" && value != "<nil>" {
			return value
		}
	}
	return "running"
}

func traceRootStatus(nodes []problemtrace.TraceNode) string {
	for _, node := range nodes {
		if strings.TrimSpace(node.ID) == "node-root" {
			return strings.TrimSpace(node.Status)
		}
	}
	return ""
}

func taskEventIDs(events []core.Event) []int {
	for i, event := range events {
		if event.Type == "user_task" || event.Type == "problem_trace_initialized" {
			return []int{i}
		}
	}
	return nil
}

func visibleAgentNote(event core.Event) string {
	if event.Type != "model_response" {
		return ""
	}
	content := strings.TrimSpace(fmt.Sprint(event.Data["content"]))
	content = stripFencedBlocks(content)
	content = strings.TrimSpace(content)
	if content == "" || content == "<nil>" {
		return ""
	}
	return shortString(content, 240)
}

func narrativeStep(event core.Event, eventIndex int) int {
	step := intValue(event.Data["step"])
	if step > 0 {
		return step
	}
	return eventIndex + 1
}

func actionTitle(tool string, command string) string {
	tool = valueOrDefault(tool, "tool")
	command = strings.TrimSpace(command)
	if title := classifyToolAction(tool, command); title != "" {
		return title
	}
	if command == "" {
		return tool
	}
	return fmt.Sprintf("%s: %s", tool, shortString(command, 120))
}

func classifyToolAction(tool string, command string) string {
	tool = strings.ToLower(strings.TrimSpace(tool))
	command = strings.TrimSpace(command)
	lowerCommand := strings.ToLower(command)

	if strings.Contains(tool, "apply_patch") {
		return "Apply patch"
	}
	if command == "" {
		return ""
	}
	if strings.Contains(lowerCommand, "gh api") && strings.Contains(lowerCommand, "reviewthreads") {
		return "Fetch unresolved PR review threads"
	}
	if strings.Contains(lowerCommand, "gh pr view") {
		return "Read PR metadata"
	}
	if strings.Contains(lowerCommand, "git status") {
		return "Check working tree status"
	}
	if strings.Contains(lowerCommand, "git diff") {
		return "Review local diff"
	}
	if strings.Contains(lowerCommand, "git apply") || strings.Contains(lowerCommand, "apply_patch") {
		return "Apply patch"
	}
	if commandLooksLikeVerification(lowerCommand) {
		return "Run verification"
	}
	if commandLooksLikeCodeInspection(lowerCommand) {
		return "Inspect referenced code"
	}
	if strings.HasPrefix(lowerCommand, "rg --files") {
		return "List repository files"
	}
	if strings.HasPrefix(lowerCommand, "rg ") || strings.Contains(lowerCommand, " rg ") {
		return "Search codebase"
	}
	return ""
}

func commandLooksLikeVerification(command string) bool {
	patterns := []string{
		"go test",
		"pytest",
		"cargo test",
		"npm test",
		"npm run test",
		"pnpm test",
		"yarn test",
		"make test",
	}
	for _, pattern := range patterns {
		if strings.Contains(command, pattern) {
			return true
		}
	}
	return false
}

func commandLooksLikeCodeInspection(command string) bool {
	return strings.Contains(command, "path(") && strings.Contains(command, "read_text") ||
		strings.Contains(command, "sed -n") ||
		strings.Contains(command, "nl -ba")
}

func actionSummary(tool string, command string, note string) string {
	if note != "" {
		return note
	}
	if strings.TrimSpace(command) != "" {
		return fmt.Sprintf("Agent selected %s to run `%s`.", valueOrDefault(tool, "tool"), shortString(command, 120))
	}
	return fmt.Sprintf("Agent selected the %s tool.", valueOrDefault(tool, "tool"))
}

func toolResultStatus(event core.Event) string {
	if timedOut, ok := normalizeValue(event.Data["timed_out"]).(bool); ok && timedOut {
		return "timeout"
	}
	if code := intValue(event.Data["code"]); code != 0 {
		return "failed"
	}
	return "ok"
}

func observationTitle(tool string, event core.Event) string {
	status := toolResultStatus(event)
	switch status {
	case "timeout":
		return "command timed out"
	case "failed":
		return "command failed"
	default:
		if strings.TrimSpace(tool) == "" {
			return "tool result"
		}
		return "command succeeded"
	}
}

func observationSummary(event core.Event) string {
	status := toolResultStatus(event)
	code := intValue(event.Data["code"])
	parts := []string{}
	switch status {
	case "timeout":
		parts = append(parts, "timed out")
	case "failed":
		parts = append(parts, fmt.Sprintf("exit code %d", code))
	default:
		parts = append(parts, "completed successfully")
	}
	if output := eventOutput(event.Data); output != "" {
		parts = append(parts, shortString(output, 240))
	} else if chars := intValue(event.Data["output_chars"]); chars > 0 {
		parts = append(parts, fmt.Sprintf("%d output chars captured", chars))
	} else {
		parts = append(parts, "no output captured")
	}
	return strings.Join(parts, "; ")
}

func appendTraceFacts(nodes []problemtrace.TraceNode, rootID string, trace problemtrace.ProblemTrace, hasActions bool) []problemtrace.TraceNode {
	for _, symptom := range trace.Symptoms {
		id := "node-" + symptom.ID
		if hasNarrativeNode(nodes, id) {
			continue
		}
		nodes = append(nodes, problemtrace.TraceNode{
			ID:       id,
			ParentID: rootID,
			Kind:     "symptom",
			Title:    symptom.Summary,
			Summary:  valueOrDefault(symptom.RawExcerpt, symptom.ErrorType),
			Status:   "observed",
			EventIDs: append([]int(nil), symptom.EventIDs...),
			Time:     symptom.CreatedAt,
		})
	}
	for _, direction := range trace.Directions {
		if hasActions && isGenericObservationDirection(direction.ID, direction.Hypothesis) {
			continue
		}
		id := "node-" + direction.ID
		if !hasNarrativeNode(nodes, id) {
			nodes = append(nodes, problemtrace.TraceNode{
				ID:          id,
				ParentID:    rootID,
				Kind:        "direction",
				Title:       direction.Hypothesis,
				Summary:     direction.Rationale,
				Status:      string(direction.Status),
				DirectionID: direction.ID,
			})
		}
		nodes = append(nodes, compactDirectionEvidence(direction)...)
	}
	return nodes
}

type evidenceGroup struct {
	DirectionID string
	Relation    string
	Title       string
	Detail      string
	EventIDs    []int
	Count       int
}

func compactDirectionEvidence(direction problemtrace.InvestigationDirection) []problemtrace.TraceNode {
	groups := map[string]*evidenceGroup{}
	var order []string
	add := func(evidence problemtrace.Evidence, fallback problemtrace.EvidenceRelation) {
		relation := string(evidence.Relation)
		if relation == "" {
			relation = string(fallback)
		}
		if relation == "" {
			relation = string(problemtrace.EvidenceSupports)
		}
		title := strings.TrimSpace(evidence.Summary)
		if title == "" {
			title = "Evidence captured"
		}
		key := direction.ID + "|" + relation + "|" + evidenceGroupKey(title)
		group, ok := groups[key]
		if !ok {
			group = &evidenceGroup{
				DirectionID: direction.ID,
				Relation:    relation,
				Title:       title,
				Detail:      strings.TrimSpace(evidence.Detail),
			}
			groups[key] = group
			order = append(order, key)
		}
		group.Count++
		group.EventIDs = compactEventIDs(append(group.EventIDs, evidence.EventIDs...))
		if detail := strings.TrimSpace(evidence.Detail); detail != "" {
			group.Detail = detail
		}
	}
	for _, evidence := range direction.SupportingEvidence {
		add(evidence, problemtrace.EvidenceSupports)
	}
	for _, evidence := range direction.RefutingEvidence {
		add(evidence, problemtrace.EvidenceRefutes)
	}

	out := make([]problemtrace.TraceNode, 0, len(order))
	for i, key := range order {
		group := groups[key]
		title := group.Title
		summary := shortString(group.Detail, 240)
		if group.Count > 1 {
			title = groupedEvidenceTitle(group.Title, group.Count)
			summary = fmt.Sprintf("%d similar observations captured", group.Count)
			if group.Detail != "" {
				summary += "; latest: " + shortString(group.Detail, 180)
			}
		}
		out = append(out, problemtrace.TraceNode{
			ID:          fmt.Sprintf("node-%s-evidence-%d", direction.ID, i+1),
			ParentID:    "node-" + direction.ID,
			Kind:        "evidence",
			Title:       title,
			Summary:     summary,
			Status:      group.Relation,
			EventIDs:    append([]int(nil), group.EventIDs...),
			DirectionID: direction.ID,
		})
	}
	return out
}

func evidenceGroupKey(title string) string {
	title = strings.ToLower(strings.TrimSpace(title))
	if strings.HasSuffix(title, " observation captured") {
		return title
	}
	return title
}

func groupedEvidenceTitle(title string, count int) string {
	lower := strings.ToLower(strings.TrimSpace(title))
	if strings.HasSuffix(lower, " observation captured") {
		tool := strings.TrimSpace(title[:len(title)-len(" observation captured")])
		if tool == "" {
			tool = "tool"
		}
		return fmt.Sprintf("%s observations captured x%d", tool, count)
	}
	return fmt.Sprintf("%s x%d", title, count)
}

func appendReviewNode(nodes []problemtrace.TraceNode, rootID string, record taskRecord, trace problemtrace.ProblemTrace) []problemtrace.TraceNode {
	status := narrativeRunStatus(record, trace)
	if status == "" || status == "pending" {
		return nodes
	}
	summary := cleanSubmission(record.Result.Submission)
	if summary == "" {
		summary = cleanSubmission(lastEventData(record.Events, "final", "submission"))
	}
	if summary == "" && trace.Frontier.ActiveDirectionID != "" {
		summary = "Current direction: " + activeDirectionSummary(trace)
	}
	if summary == "" && status == "running" {
		summary = "Run is still collecting evidence."
	}
	nodes = append(nodes, problemtrace.TraceNode{
		ID:       "node-review",
		ParentID: rootID,
		Kind:     "verification",
		Title:    "Review",
		Summary:  summary,
		Status:   status,
		EventIDs: finalEventIDs(record.Events),
	})
	return nodes
}

func finalEventIDs(events []core.Event) []int {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type == "final" {
			return []int{i}
		}
	}
	return nil
}

func fallbackNarrativeFromHistory(root problemtrace.TraceNode, history []problemtrace.TraceNode) []problemtrace.TraceNode {
	compact := compactTraceNodes(history)
	if len(compact) <= 1 {
		return []problemtrace.TraceNode{root}
	}
	compact[0] = root
	return compact
}

func compactTraceNodes(nodes []problemtrace.TraceNode) []problemtrace.TraceNode {
	out := make([]problemtrace.TraceNode, 0, len(nodes))
	for _, node := range nodes {
		switch strings.ToLower(strings.TrimSpace(node.Kind)) {
		case "prompt", "events":
			continue
		default:
			out = append(out, node)
		}
	}
	return out
}

func traceContextDirection(event core.Event) string {
	var tc problemtrace.TraceContext
	if problemtraceDecode(event.Data["trace_context"], &tc) {
		return strings.TrimSpace(tc.DirectionID)
	}
	return ""
}

func traceContextPrompt(event core.Event) string {
	var tc problemtrace.TraceContext
	if problemtraceDecode(event.Data["trace_context"], &tc) {
		return strings.TrimSpace(tc.PromptSnapshotID)
	}
	return ""
}

func hasNarrativeNode(nodes []problemtrace.TraceNode, id string) bool {
	for _, node := range nodes {
		if strings.TrimSpace(node.ID) == id {
			return true
		}
	}
	return false
}

func compactEventIDs(ids []int) []int {
	if len(ids) == 0 {
		return nil
	}
	seen := map[int]bool{}
	out := make([]int, 0, len(ids))
	for _, id := range ids {
		if id < 0 || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	sort.Ints(out)
	return out
}
