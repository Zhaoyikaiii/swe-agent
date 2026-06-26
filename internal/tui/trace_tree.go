package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/local/swe-agent/internal/core"
	"github.com/local/swe-agent/internal/problemtrace"
)

type TraceWorkspaceVM struct {
	Trace          problemtrace.ProblemTrace
	Tree           TraceTreeVM
	Rows           []TraceTreeRow
	Selected       *TraceTreeNodeVM
	Events         []core.Event
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
	traceDefaultStyle   = lipgloss.NewStyle().Foreground(colorMuted)
	traceProblemStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Bold(true)
	traceDirectionStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("45"))
	traceEvidenceStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	tracePromptStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("177"))
	traceThoughtStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("111"))
	traceActionStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("208"))
	traceObserveStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	traceMemoryStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	traceEventStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	traceFixStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("82")).Bold(true)
	traceErrorStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Bold(true)
	tracePaneTitleStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Background(lipgloss.Color("24")).Bold(true)
	traceSelectedStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Background(lipgloss.Color("24")).Bold(true)
)

func renderTracePaneTitle(title string, active bool) string {
	if active {
		return tracePaneTitleStyle.Render(title)
	}
	return title
}

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
	nodes := trace.History
	if !state.Debug {
		nodes = buildTraceNarrativeNodes(record, trace)
	}
	tree := buildTraceTreeVM(nodes)
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
		Events:         append([]core.Event(nil), record.Events...),
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

func renderTraceTreeTab(b *strings.Builder, vm TraceWorkspaceVM, state traceWorkspaceState, width int, height int, record taskRecord) {
	trace := vm.Trace
	snapshot := BuildRunSnapshot(record, vm.TrajectoryPath)
	status := narrativeRunStatus(record, trace)
	writeField(b, "Task", shortString(trace.Problem.UserTask, 100), width)
	writeField(b, "Status", status, width)
	if outcome, confidence, reason, ok := traceOutcomeSignal(status, record, snapshot); ok {
		writeField(b, "Outcome", outcome, width)
		writeField(b, "Confidence", confidence, width)
		writeField(b, "Reason", reason, width)
	}
	writeField(b, "Validation", validationSummary(snapshot), width)
	if snapshot.FinalReview.ChangedFiles > 0 {
		writeField(b, "Diff", fmt.Sprintf("%d files changed", snapshot.FinalReview.ChangedFiles), width)
	}
	if active := activeDirectionSummary(trace); active != "none" {
		writeField(b, "Active", active, width)
	}
	if trace.Problem.ErrorSummary != "" {
		writeField(b, "Symptom", shortString(trace.Problem.ErrorSummary, 100), width)
	}
	if state.Debug {
		writeSection(b, "Debug")
		writeField(b, "Trace ID", trace.TraceID, width)
		writeField(b, "Trajectory", vm.TrajectoryPath, width)
		writeField(b, "Repository", trace.Problem.Repo, width)
	}

	b.WriteByte('\n')
	if width >= 120 {
		b.WriteString(renderTraceSplit(vm, state, width, height))
		return
	}

	b.WriteString(renderTraceTreeASCII(vm.Rows, state, width))

	if vm.Selected != nil && len(vm.Rows) > 0 {
		cursor := traceCursorForRows(state, vm.Rows)
		b.WriteByte('\n')
		b.WriteString(renderTraceNodeInspector(vm.Rows[cursor], *vm.Selected, width, state.Debug))
	}

	// Keep the Trace tab focused on the problem tree and selected node details.
	// Span-level data remains available to move into a dedicated tab later.
}

func traceOutcomeSignal(status string, record taskRecord, snapshot RunSnapshot) (string, string, string, bool) {
	if !strings.EqualFold(strings.TrimSpace(status), "submitted") {
		return "", "", "", false
	}
	if snapshot.FinalReview.ChangedFiles > 0 || strings.TrimSpace(record.Result.Diff) != "" || snapshot.FinalReview.TestsRun > 0 {
		return "", "", "", false
	}
	return "submitted, but no diff or validation recorded", "low", "no diff and no validation recorded", true
}

func renderTraceTreeASCII(rows []TraceTreeRow, state traceWorkspaceState, width int) string {
	return renderTraceTreeASCIIOptions(rows, state, width, true)
}

func renderTraceTreeASCIIOptions(rows []TraceTreeRow, state traceWorkspaceState, width int, inlineSummary bool) string {
	var b strings.Builder
	title := "Trace Tree"
	b.WriteString(renderTracePaneTitle(title, !inlineSummary && state.Pane == tracePaneTree))
	b.WriteByte('\n')
	if inlineSummary {
		b.WriteString("j/k move  space fold  enter/o detail  O output  tab switch tab\n\n")
	} else {
		b.WriteString("j/k move  space fold  h/l focus  enter/o detail  O output\n\n")
	}

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

		line := formatTraceTreeLine(row, selection, twisty, width)
		line = truncate(line, width)
		if i == cursor {
			line = traceSelectedStyle.Width(width).Render(line)
		} else {
			line = traceNodeLineStyle(row.Kind, row.Status, false).Render(line)
		}
		b.WriteString(line)
		b.WriteByte('\n')

		if inlineSummary && i == cursor && strings.TrimSpace(row.Summary) != "" {
			b.WriteString(indentText(wrapText(row.Summary, remainingWidth(width, 4)), 4))
			b.WriteByte('\n')
		}
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

func formatTraceTreeLine(row TraceTreeRow, selection string, twisty string, width int) string {
	prefix := fmt.Sprintf("%s%s%s%s %s ", selection, row.Prefix, row.Connector, twisty, traceStatusASCII(row.Status))
	title := displayTraceRowTitle(row)
	suffix := ""
	if kind := displayTraceKindLabel(row.Kind, row.NodeID, row.Title); kind != "" {
		suffix += "  " + kind
	}
	if row.Status != "" {
		suffix += "  " + row.Status
	}
	if width > 0 && suffix != "" {
		titleWidth := width - displayWidth(prefix) - displayWidth(suffix)
		if titleWidth >= 4 {
			title = truncate(title, titleWidth)
		}
	}
	return prefix + title + suffix
}

func renderTraceSplit(vm TraceWorkspaceVM, state traceWorkspaceState, width int, height int) string {
	gap := 3
	leftWidth, rightWidth := traceSplitWidths(width, gap)

	left := renderTraceTreePanel(vm, state, leftWidth)
	right := renderTraceDetailPanel(vm, state, rightWidth)

	panelHeight := traceSplitPanelHeight(left, right, height)

	left = fitTraceTreeAroundCursor(left, panelHeight, traceCursorForRows(state, vm.Rows))
	right = fitHeightOffset(right, panelHeight, state.DetailOffset)

	return renderSplitPanels(left, right, leftWidth, rightWidth)
}

func traceSplitWidths(width int, gap int) (int, int) {
	return splitPanelWidths(width, gap, 58, 48, 36)
}

func traceSplitPanelHeight(left string, right string, height int) int {
	return splitPanelHeight(left, right, height, 8)
}

func traceDetailMaxOffset(vm TraceWorkspaceVM, state traceWorkspaceState, width int, height int) int {
	if width < 120 {
		return 0
	}
	const gap = 3
	leftWidth, rightWidth := traceSplitWidths(width, gap)
	left := renderTraceTreePanel(vm, state, leftWidth)
	right := renderTraceDetailPanel(vm, state, rightWidth)
	panelHeight := traceSplitPanelHeight(left, right, height)
	return max(0, lipgloss.Height(right)-panelHeight)
}

func fitHeightOffset(content string, height int, offset int) string {
	if height <= 0 {
		return strings.TrimRight(content, "\n")
	}
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	maxOffset := max(0, len(lines)-height)
	offset = clamp(offset, 0, maxOffset)
	end := min(len(lines), offset+height)
	lines = append([]string(nil), lines[offset:end]...)
	if offset > 0 && len(lines) > 0 {
		lines[0] = "..."
	}
	if offset < maxOffset && len(lines) > 0 {
		lines[len(lines)-1] = "..."
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

func fitTraceTreeAroundCursor(content string, height int, cursor int) string {
	return fitListAroundCursor(content, height, cursor)
}

func renderTraceTreePanel(vm TraceWorkspaceVM, state traceWorkspaceState, width int) string {
	return renderTraceTreeASCIIOptions(vm.Rows, state, width, false)
}

func renderTraceDetailPanel(vm TraceWorkspaceVM, state traceWorkspaceState, width int) string {
	var b strings.Builder
	title := "Selected Detail"
	b.WriteString(renderTracePaneTitle(title, state.Pane == tracePaneDetail))
	b.WriteByte('\n')

	row, node, ok := selectedTraceNode(vm, state)
	if !ok {
		b.WriteString(traceDetailTabs(state.DetailTab))
		b.WriteString("\n\n")
		b.WriteString("No node selected.\n")
		return b.String()
	}

	activeTab := effectiveTraceDetailTab(state, node)
	b.WriteString(traceDetailTabs(activeTab))
	b.WriteString("\n\n")

	switch activeTab {
	case traceDetailOutput:
		renderTraceDetailOutput(&b, row, node, vm, width)
	case traceDetailEvents:
		renderTraceDetailEvents(&b, row, node, width)
	case traceDetailDebug:
		renderTraceDetailDebug(&b, row, node, vm, width)
	default:
		renderTraceDetailOverview(&b, row, node, vm, width)
	}

	return b.String()
}

func effectiveTraceDetailTab(state traceWorkspaceState, node TraceTreeNodeVM) traceDetailTab {
	if state.DetailTab != traceDetailOverview {
		return state.DetailTab
	}
	if strings.EqualFold(strings.TrimSpace(node.Kind), "action") && traceStatusIsFailure(node.Status) {
		return traceDetailOutput
	}
	return state.DetailTab
}

func traceDetailTabs(active traceDetailTab) string {
	items := []traceDetailTab{
		traceDetailOverview,
		traceDetailOutput,
		traceDetailEvents,
		traceDetailDebug,
	}

	parts := make([]string, 0, len(items))
	for _, item := range items {
		label := traceDetailTabLabel(item)
		if item == active {
			label = "[" + label + "]"
		}
		parts = append(parts, label)
	}
	return strings.Join(parts, "  ")
}

func selectedTraceNode(vm TraceWorkspaceVM, state traceWorkspaceState) (TraceTreeRow, TraceTreeNodeVM, bool) {
	if len(vm.Rows) == 0 {
		return TraceTreeRow{}, TraceTreeNodeVM{}, false
	}
	cursor := traceCursorForRows(state, vm.Rows)
	row := vm.Rows[cursor]
	node, ok := vm.Tree.Nodes[row.NodeID]
	return row, node, ok
}

func renderTraceDetailOverview(b *strings.Builder, row TraceTreeRow, node TraceTreeNodeVM, vm TraceWorkspaceVM, width int) {
	switch strings.ToLower(strings.TrimSpace(node.Kind)) {
	case "thought":
		writeField(b, "AI said", node.Summary, width)
	case "action":
		writeField(b, "Action", displayTraceNodeTitle(node), width)
		writeField(b, "Status", node.Status, width)
		if node.Summary != "" {
			writeField(b, "Why", node.Summary, width)
		}
		if child := firstChildOfKind(vm, node.ID, "observation"); child != nil {
			writeField(b, "Latest result", displayTraceNodeTitle(*child), width)
		}
	case "observation":
		writeField(b, "Observation", displayTraceNodeTitle(node), width)
		writeField(b, "Status", node.Status, width)
		writeField(b, "Summary", summarizeObservationForOverview(node.Summary), width)
	case "direction":
		writeField(b, "Direction", displayTraceNodeTitle(node), width)
		writeField(b, "Status", node.Status, width)
		writeField(b, "Why", node.Summary, width)
	case "evidence":
		writeField(b, "Evidence", displayTraceNodeTitle(node), width)
		writeField(b, "Status", node.Status, width)
		writeField(b, "Detail", node.Summary, width)
	case "symptom":
		writeField(b, "Symptom", displayTraceNodeTitle(node), width)
		writeField(b, "Status", node.Status, width)
		writeField(b, "Evidence", node.Summary, width)
	case "prompt":
		writeField(b, "Prompt", displayTraceNodeTitle(node), width)
	case "task", "problem":
		writeField(b, "What", displayTraceNodeTitle(node), width)
		writeField(b, "Status", node.Status, width)
		writeField(b, "Why", node.Summary, width)
	case "fix", "verification":
		writeField(b, "Item", displayTraceNodeTitle(node), width)
		writeField(b, "Status", node.Status, width)
		if node.Summary != "" {
			writeField(b, "Why", node.Summary, width)
		}
	default:
		writeField(b, "Item", displayTraceNodeTitle(node), width)
		writeField(b, "Status", node.Status, width)
		if node.Summary != "" {
			writeField(b, "Why", node.Summary, width)
		}
	}
	renderTraceNodeRelated(b, row, node, width)
}

func renderTraceNodeRelated(b *strings.Builder, row TraceTreeRow, node TraceTreeNodeVM, width int) {
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
	add("Direction", node.DirectionID)
	add("Prompt", node.PromptID)
	eventIDs := node.EventIDs
	if len(eventIDs) == 0 {
		eventIDs = row.EventIDs
	}
	if len(eventIDs) > 0 {
		add("Events", formatEventIDs(eventIDs))
	}
	if len(node.Children) > 0 {
		add("Children", len(node.Children))
	}

	if len(fields) == 0 {
		return
	}
	writeSection(b, "Related")
	for _, field := range fields {
		writeField(b, field.key, field.value, width)
	}
}

func renderTraceDetailOutput(b *strings.Builder, _ TraceTreeRow, node TraceTreeNodeVM, vm TraceWorkspaceVM, width int) {
	output := ""
	switch strings.ToLower(strings.TrimSpace(node.Kind)) {
	case "action":
		if child := firstChildOfKind(vm, node.ID, "observation"); child != nil {
			output = child.Summary
		}
	case "observation":
		output = node.Summary
	default:
		output = node.Summary
	}

	output = strings.TrimSpace(output)
	if output == "" {
		b.WriteString("No output captured for this node.\n")
		return
	}

	writeField(b, "Result", output, width)
}

func renderTraceDetailEvents(b *strings.Builder, row TraceTreeRow, node TraceTreeNodeVM, width int) {
	eventIDs := node.EventIDs
	if len(eventIDs) == 0 {
		eventIDs = row.EventIDs
	}
	if len(eventIDs) == 0 {
		b.WriteString("No linked events.\n")
		return
	}

	writeField(b, "Events", formatEventIDs(eventIDs), width)
	b.WriteString("\nPress 4 to open the Events tab for raw event details.\n")
}

func renderTraceDetailDebug(b *strings.Builder, row TraceTreeRow, node TraceTreeNodeVM, vm TraceWorkspaceVM, width int) {
	eventIDs := node.EventIDs
	if len(eventIDs) == 0 {
		eventIDs = row.EventIDs
	}

	writeField(b, "ID", node.ID, width)
	writeField(b, "Kind", node.Kind, width)
	writeField(b, "Status", node.Status, width)
	writeField(b, "Title", node.Title, width)
	if command := rawCommandForTraceNode(vm, eventIDs); command != "" {
		writeField(b, "Raw command", command, width)
	}
	writeField(b, "Summary", node.Summary, width)
	writeField(b, "Direction", node.DirectionID, width)
	writeField(b, "Prompt", node.PromptID, width)

	if len(eventIDs) > 0 {
		writeField(b, "Events", formatEventIDs(eventIDs), width)
	}
	if len(node.Children) > 0 {
		writeField(b, "Children", len(node.Children), width)
	}
}

func rawCommandForTraceNode(vm TraceWorkspaceVM, eventIDs []int) string {
	for _, eventID := range eventIDs {
		if eventID < 0 || eventID >= len(vm.Events) {
			continue
		}
		event := vm.Events[eventID]
		if event.Type != "tool_call" {
			continue
		}
		if command := commandFromArgs(event.Data["args"]); command != "" {
			return command
		}
	}
	return ""
}

func firstChildOfKind(vm TraceWorkspaceVM, parentID string, kind string) *TraceTreeNodeVM {
	parent, ok := vm.Tree.Nodes[parentID]
	if !ok {
		return nil
	}
	for _, childID := range parent.Children {
		child, ok := vm.Tree.Nodes[childID]
		if !ok {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(child.Kind), kind) {
			item := child
			return &item
		}
	}
	return nil
}

func summarizeObservationForOverview(summary string) string {
	return shortString(summary, 220)
}

func renderTraceNodeInspector(row TraceTreeRow, node TraceTreeNodeVM, width int, debug bool) string {
	var b strings.Builder
	b.WriteString("Selected\n")
	switch strings.ToLower(strings.TrimSpace(node.Kind)) {
	case "thought":
		writeField(&b, "AI said", node.Summary, width)
	case "action":
		writeField(&b, "Action", displayTraceNodeTitle(node), width)
		writeField(&b, "Why", node.Summary, width)
	case "observation":
		writeField(&b, "Observation", displayTraceNodeTitle(node), width)
		writeField(&b, "Result", node.Summary, width)
	case "direction":
		writeField(&b, "Direction", displayTraceNodeTitle(node), width)
		writeField(&b, "Why", node.Summary, width)
	case "evidence":
		writeField(&b, "Evidence", displayTraceNodeTitle(node), width)
		writeField(&b, "Detail", node.Summary, width)
	case "symptom":
		writeField(&b, "Symptom", displayTraceNodeTitle(node), width)
		writeField(&b, "Evidence", node.Summary, width)
	case "prompt":
		writeField(&b, "Prompt", displayTraceNodeTitle(node), width)
	case "task", "problem":
		writeField(&b, "What", displayTraceNodeTitle(node), width)
		writeField(&b, "Why", node.Summary, width)
	default:
		writeField(&b, "Item", displayTraceNodeTitle(node), width)
		if node.Summary != "" {
			writeField(&b, "Why", node.Summary, width)
		}
	}
	if !debug {
		return b.String()
	}

	writeSection(&b, "Debug")
	writeField(&b, "ID", node.ID, width)
	writeField(&b, "Kind", node.Kind, width)
	writeField(&b, "Status", node.Status, width)
	if displayTraceNodeTitle(node) != node.Title {
		writeField(&b, "Title", node.Title, width)
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

func displayTraceRowTitle(row TraceTreeRow) string {
	return displayDirectionTitle(row.NodeID, row.Title)
}

func displayTraceNodeTitle(node TraceTreeNodeVM) string {
	return displayDirectionTitle(node.ID, node.Title)
}

func displayTraceKindLabel(kind, id, title string) string {
	if isGenericObservationDirection(id, title) {
		return "observation"
	}
	return traceKindLabel(kind)
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

func traceStatusIsFailure(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "error", "failed", "failure", "refuted", "timeout", "blocked":
		return true
	default:
		return false
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
	case "task", "problem":
		return traceProblemStyle
	case "direction":
		return traceDirectionStyle
	case "evidence", "symptom":
		return traceEvidenceStyle
	case "prompt":
		return tracePromptStyle
	case "thought":
		return traceThoughtStyle
	case "action":
		return traceActionStyle
	case "observation":
		return traceObserveStyle
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
	case "task":
		return "task"
	case "problem":
		return "problem"
	case "prompt":
		return "prompt"
	case "thought":
		return "reason"
	case "action":
		return "action"
	case "observation":
		return "observation"
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
