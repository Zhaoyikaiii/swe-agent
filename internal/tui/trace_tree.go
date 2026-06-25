package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/local/swe-agent/internal/problemtrace"
)

type TraceWorkspaceVM struct {
	Trace          problemtrace.ProblemTrace
	Tree           TraceTreeVM
	Rows           []TraceTreeRow
	Selected       *TraceTreeNodeVM
	TrajectoryPath string
}

type TraceTreeVM struct {
	Roots []string
	Nodes map[string]TraceTreeNodeVM
	Order []string
}

type TraceTreeNodeVM struct {
	ID          string
	ParentID    string
	Kind        string
	Title       string
	Summary     string
	Status      string
	EventIDs    []int
	DirectionID string
	PromptID    string
	Children    []string
}

type TraceTreeRow struct {
	NodeID    string
	Depth     int
	Prefix    string
	Connector string
	Title     string
	Summary   string
	Status    string
	Kind      string
	Expanded  bool
	HasKids   bool
	EventIDs  []int
}

var (
	traceDefaultStyle   = lipgloss.NewStyle()
	traceProblemStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Bold(true)
	traceDirectionStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("45"))
	traceEvidenceStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	tracePromptStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("177"))
	traceMemoryStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	traceEventStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	traceFixStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("82")).Bold(true)
	traceErrorStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Bold(true)
	traceSelectedStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Background(lipgloss.Color("24")).Bold(true)
)

func (s *traceWorkspaceState) ensureDefaults() {
	if s.Expanded == nil {
		s.Expanded = map[string]bool{
			"node-root": true,
		}
	}
}

func buildTraceWorkspaceVM(record taskRecord, state traceWorkspaceState, trajectoryPath string) TraceWorkspaceVM {
	state.ensureDefaults()

	trace := problemtrace.Replay(record.Events)
	tree := buildTraceTreeVM(trace.History)
	rows := flattenTraceTree(tree, state.Expanded)

	var selected *TraceTreeNodeVM
	if len(rows) > 0 {
		cursor := traceCursorForRows(state, rows)
		if node, ok := tree.Nodes[rows[cursor].NodeID]; ok {
			item := node
			selected = &item
		}
	}

	return TraceWorkspaceVM{
		Trace:          trace,
		Tree:           tree,
		Rows:           rows,
		Selected:       selected,
		TrajectoryPath: trajectoryPath,
	}
}

func buildTraceTreeVM(nodes []problemtrace.TraceNode) TraceTreeVM {
	vm := TraceTreeVM{
		Nodes: make(map[string]TraceTreeNodeVM),
	}
	seen := map[string]bool{}
	for _, node := range nodes {
		id := strings.TrimSpace(node.ID)
		if id == "" {
			continue
		}
		item := TraceTreeNodeVM{
			ID:          id,
			ParentID:    strings.TrimSpace(node.ParentID),
			Kind:        strings.TrimSpace(node.Kind),
			Title:       valueOrDefault(node.Title, valueOrDefault(node.Kind, id)),
			Summary:     strings.TrimSpace(node.Summary),
			Status:      strings.TrimSpace(node.Status),
			EventIDs:    append([]int(nil), node.EventIDs...),
			DirectionID: strings.TrimSpace(node.DirectionID),
			PromptID:    strings.TrimSpace(node.PromptID),
		}
		vm.Nodes[id] = item
		if !seen[id] {
			vm.Order = append(vm.Order, id)
			seen[id] = true
		}
	}

	rootSeen := map[string]bool{}
	for _, id := range vm.Order {
		node := vm.Nodes[id]
		if node.ParentID == "" {
			if !rootSeen[id] {
				vm.Roots = append(vm.Roots, id)
				rootSeen[id] = true
			}
			continue
		}
		parent, ok := vm.Nodes[node.ParentID]
		if !ok {
			if !rootSeen[id] {
				vm.Roots = append(vm.Roots, id)
				rootSeen[id] = true
			}
			continue
		}
		parent.Children = append(parent.Children, id)
		vm.Nodes[node.ParentID] = parent
	}
	if len(vm.Roots) == 0 && len(vm.Order) > 0 {
		vm.Roots = append(vm.Roots, vm.Order[0])
	}
	return vm
}

func flattenTraceTree(vm TraceTreeVM, expanded map[string]bool) []TraceTreeRow {
	if expanded == nil {
		expanded = map[string]bool{}
	}

	var rows []TraceTreeRow
	var walk func(id string, depth int, prefix string, connector string)
	walk = func(id string, depth int, prefix string, connector string) {
		node, ok := vm.Nodes[id]
		if !ok {
			return
		}
		hasKids := len(node.Children) > 0
		isExpanded := expanded[id]
		rows = append(rows, TraceTreeRow{
			NodeID:    node.ID,
			Depth:     depth,
			Prefix:    prefix,
			Connector: connector,
			Title:     node.Title,
			Summary:   node.Summary,
			Status:    node.Status,
			Kind:      node.Kind,
			Expanded:  isExpanded,
			HasKids:   hasKids,
			EventIDs:  append([]int(nil), node.EventIDs...),
		})
		if !hasKids || !isExpanded {
			return
		}
		childPrefix := prefix
		if connector != "" {
			if strings.HasPrefix(connector, "`") {
				childPrefix += "    "
			} else {
				childPrefix += "|   "
			}
		}
		for i, childID := range node.Children {
			last := i == len(node.Children)-1
			childConnector := "+-- "
			if last {
				childConnector = "`-- "
			}
			walk(childID, depth+1, childPrefix, childConnector)
		}
	}

	for i, rootID := range vm.Roots {
		connector := ""
		if len(vm.Roots) > 1 {
			if i == len(vm.Roots)-1 {
				connector = "`-- "
			} else {
				connector = "+-- "
			}
		}
		walk(rootID, 0, "", connector)
	}
	return rows
}

func renderTraceTreeTab(b *strings.Builder, vm TraceWorkspaceVM, state traceWorkspaceState, width int) {
	trace := vm.Trace
	writeField(b, "Trace ID", trace.TraceID, width)
	writeField(b, "Trajectory", vm.TrajectoryPath, width)
	writeField(b, "Repository", trace.Problem.Repo, width)
	writeField(b, "Task", trace.Problem.UserTask, width)
	if trace.Problem.ErrorSummary != "" {
		writeField(b, "Current Symptom", trace.Problem.ErrorSummary, width)
	}

	b.WriteByte('\n')
	b.WriteString(renderTraceTreeASCII(vm.Rows, state, width))

	if vm.Selected != nil && len(vm.Rows) > 0 {
		cursor := traceCursorForRows(state, vm.Rows)
		b.WriteByte('\n')
		b.WriteString(renderTraceNodeInspector(vm.Rows[cursor], *vm.Selected, width))
	}

	// Keep the Trace tab focused on the problem tree and selected node details.
	// Span-level data remains available to move into a dedicated tab later.
}

func renderTraceTreeASCII(rows []TraceTreeRow, state traceWorkspaceState, width int) string {
	var b strings.Builder
	b.WriteString("Trace Tree\n")
	b.WriteString("j/k move  enter expand/collapse  o inspect  tab switch tab\n\n")

	if len(rows) == 0 {
		b.WriteString("No trace tree nodes yet.\n")
		return b.String()
	}

	cursor := traceCursorForRows(state, rows)
	for i, row := range rows {
		selection := "  "
		if i == cursor {
			selection = "> "
		}

		twisty := "[ ]"
		if row.HasKids {
			if row.Expanded {
				twisty = "[-]"
			} else {
				twisty = "[+]"
			}
		}

		line := fmt.Sprintf("%s%s%s%s %s %s", selection, row.Prefix, row.Connector, twisty, traceStatusASCII(row.Status), row.Title)
		if kind := traceKindLabel(row.Kind); kind != "" {
			line += "  " + kind
		}
		if row.Status != "" {
			line += "  " + row.Status
		}
		line = truncate(line, width)
		line = traceNodeLineStyle(row.Kind, row.Status, i == cursor).Render(line)
		b.WriteString(line)
		b.WriteByte('\n')

		if i == cursor && strings.TrimSpace(row.Summary) != "" {
			b.WriteString(indentText(wrapText(row.Summary, remainingWidth(width, 4)), 4))
			b.WriteByte('\n')
		}
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

func renderTraceNodeInspector(row TraceTreeRow, node TraceTreeNodeVM, width int) string {
	var b strings.Builder
	b.WriteString("Selected Node\n")
	writeField(&b, "ID", node.ID, width)
	writeField(&b, "Kind", node.Kind, width)
	writeField(&b, "Status", node.Status, width)
	writeField(&b, "Title", node.Title, width)
	if node.Summary != "" {
		writeSection(&b, "Summary")
		b.WriteString(wrapText(node.Summary, width))
		b.WriteByte('\n')
	}
	if node.DirectionID != "" {
		writeField(&b, "Direction", node.DirectionID, width)
	}
	if node.PromptID != "" {
		writeField(&b, "Prompt", node.PromptID, width)
	}
	eventIDs := node.EventIDs
	if len(eventIDs) == 0 {
		eventIDs = row.EventIDs
	}
	if len(eventIDs) > 0 {
		writeField(&b, "Events", formatEventIDs(eventIDs), width)
	}
	if len(node.Children) > 0 {
		writeField(&b, "Children", len(node.Children), width)
	}
	return b.String()
}

func normalizeTraceCursor(state *traceWorkspaceState, rows []TraceTreeRow) {
	if len(rows) == 0 {
		state.Cursor = 0
		state.SelectedID = ""
		return
	}
	if state.SelectedID != "" {
		for i, row := range rows {
			if row.NodeID == state.SelectedID {
				state.Cursor = i
				return
			}
		}
	}
	state.Cursor = clamp(state.Cursor, 0, len(rows)-1)
	state.SelectedID = rows[state.Cursor].NodeID
}

func traceCursorForRows(state traceWorkspaceState, rows []TraceTreeRow) int {
	if len(rows) == 0 {
		return 0
	}
	if state.SelectedID != "" {
		for i, row := range rows {
			if row.NodeID == state.SelectedID {
				return i
			}
		}
	}
	return clamp(state.Cursor, 0, len(rows)-1)
}

func traceStatusASCII(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "running", "active":
		return "*"
	case "ok", "supported", "fixed", "submitted", "captured", "supports", "observed":
		return "+"
	case "refuted", "failed", "failure", "error", "refutes":
		return "x"
	case "blocked", "timeout":
		return "!"
	case "open", "":
		return "o"
	default:
		return "."
	}
}

func traceNodeLineStyle(kind string, status string, selected bool) lipgloss.Style {
	if selected {
		return traceSelectedStyle
	}

	switch strings.ToLower(strings.TrimSpace(status)) {
	case "error", "failed", "failure", "refuted", "timeout", "blocked":
		return traceErrorStyle
	}

	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "problem":
		return traceProblemStyle
	case "direction":
		return traceDirectionStyle
	case "evidence", "symptom":
		return traceEvidenceStyle
	case "prompt":
		return tracePromptStyle
	case "memory", "card":
		return traceMemoryStyle
	case "events", "event":
		return traceEventStyle
	case "fix", "verification":
		return traceFixStyle
	default:
		return traceDefaultStyle
	}
}

func traceKindLabel(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "problem":
		return "problem"
	case "prompt":
		return "prompt"
	case "direction":
		return "direction"
	case "evidence":
		return "evidence"
	case "fix":
		return "fix"
	case "verification":
		return "verify"
	case "memory":
		return "memory"
	case "card":
		return "card"
	default:
		return strings.TrimSpace(kind)
	}
}
