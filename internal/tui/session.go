package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	agentpkg "github.com/local/swe-agent/internal/agent"
	"github.com/local/swe-agent/internal/core"
	"github.com/local/swe-agent/internal/policy"
)

const (
	eventBuffer    = 256
	approvalBuffer = 16
)

type Session struct {
	events    chan eventMsg
	approvals chan approvalMsg
}

func NewSession() *Session {
	return &Session{
		events:    make(chan eventMsg, eventBuffer),
		approvals: make(chan approvalMsg, approvalBuffer),
	}
}

func (s *Session) EmitEvent(ctx context.Context, event core.Event) error {
	select {
	case s.events <- eventMsg{event: event}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Session) RequestApproval(ctx context.Context, req policy.ApprovalRequest) (policy.ApprovalDecision, error) {
	response := make(chan policy.ApprovalDecision, 1)
	msg := approvalMsg{request: req, response: response}
	select {
	case s.approvals <- msg:
	case <-ctx.Done():
		return policy.ApprovalDecision{}, ctx.Err()
	}
	select {
	case decision := <-response:
		return decision, nil
	case <-ctx.Done():
		return policy.ApprovalDecision{}, ctx.Err()
	}
}

func (s *Session) Run(ctx context.Context, ag *agentpkg.Agent, task core.Task) (agentpkg.Result, error) {
	m := newRunModel(s, ag, task, ctx)
	program := tea.NewProgram(m, tea.WithAltScreen())
	finalModel, err := program.Run()
	if m.cancel != nil {
		m.cancel()
	}
	if err != nil {
		return agentpkg.Result{}, err
	}
	if final, ok := finalModel.(*model); ok {
		if !final.done && final.canceled {
			return final.result, context.Canceled
		}
		return final.result, final.runErr
	}
	return agentpkg.Result{}, nil
}

func (s *Session) Loop(ctx context.Context, ag *agentpkg.Agent, repo string) (agentpkg.Result, error) {
	m := newLoopModel(s, ag, repo, ctx)
	program := tea.NewProgram(m, tea.WithAltScreen())
	finalModel, err := program.Run()
	if m.cancel != nil {
		m.cancel()
	}
	if err != nil {
		return agentpkg.Result{}, err
	}
	if final, ok := finalModel.(*model); ok {
		if final.canceled {
			return final.result, context.Canceled
		}
		return final.result, final.runErr
	}
	return agentpkg.Result{}, nil
}

type eventMsg struct {
	event core.Event
}

type approvalMsg struct {
	request  policy.ApprovalRequest
	response chan policy.ApprovalDecision
}

type runDoneMsg struct {
	result agentpkg.Result
	err    error
}

type editorDoneMsg struct {
	err error
}

type uiMode int

const (
	modeNormal uiMode = iota
	modeApproval
	modeCommand
	modeTask
	modeSearch
	modeHelp
	modeQuitConfirm
)

type detailView int

const (
	viewEvent detailView = iota
	viewDiff
	viewTrace
)

type approvalState struct {
	msg      approvalMsg
	expanded bool
}

type model struct {
	session *Session
	agent   *agentpkg.Agent
	task    core.Task
	parent  context.Context
	runCtx  context.Context
	cancel  context.CancelFunc
	loop    bool
	start   bool

	width  int
	height int

	mode       uiMode
	beforeHelp uiMode
	view       detailView
	focus      string

	events         []core.Event
	selected       int
	timelineOffset int
	detail         viewport.Model
	help           help.Model
	spinner        spinner.Model
	command        textinput.Model

	approval *approvalState
	result   agentpkg.Result
	runErr   error
	running  bool
	done     bool
	canceled bool

	query      string
	status     string
	pendingKey string
	startedAt  time.Time
}

func newRunModel(session *Session, ag *agentpkg.Agent, task core.Task, parent context.Context) *model {
	m := newModel(session, ag, task, parent)
	m.start = true
	m.status = "starting"
	return m
}

func newLoopModel(session *Session, ag *agentpkg.Agent, repo string, parent context.Context) *model {
	m := newModel(session, ag, core.Task{Repo: repo}, parent)
	m.loop = true
	m.mode = modeTask
	m.status = "ready"
	m.prepareTaskInput(true)
	return m
}

func newModel(session *Session, ag *agentpkg.Agent, task core.Task, parent context.Context) *model {
	if parent == nil {
		parent = context.Background()
	}
	command := textinput.New()
	command.Prompt = ":"
	command.CharLimit = 4096

	spin := spinner.New()
	spin.Spinner = spinner.MiniDot
	spin.Style = lipgloss.NewStyle().Foreground(colorAccent)

	detail := viewport.New(1, 1)
	detail.SetContent("Waiting for events...")

	h := help.New()

	return &model{
		session:   session,
		agent:     ag,
		task:      task,
		parent:    parent,
		mode:      modeNormal,
		view:      viewEvent,
		focus:     "timeline",
		selected:  -1,
		detail:    detail,
		help:      h,
		spinner:   spin,
		command:   command,
		status:    "starting",
		startedAt: time.Now(),
	}
}

func (m *model) Init() tea.Cmd {
	cmds := []tea.Cmd{
		m.spinner.Tick,
		waitForEvent(m.session.events),
		waitForApproval(m.session.approvals),
	}
	if m.mode == modeTask {
		cmds = append(cmds, m.command.Focus())
	}
	if m.start {
		cmds = append(cmds, m.startRun(m.task))
	}
	return tea.Batch(cmds...)
}

func waitForEvent(ch <-chan eventMsg) tea.Cmd {
	return func() tea.Msg {
		return <-ch
	}
}

func waitForApproval(ch <-chan approvalMsg) tea.Cmd {
	return func() tea.Msg {
		return <-ch
	}
}

func runAgent(ctx context.Context, ag *agentpkg.Agent, task core.Task) tea.Cmd {
	return func() tea.Msg {
		result, err := ag.Run(ctx, task)
		return runDoneMsg{result: result, err: err}
	}
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.resize()
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		if m.running {
			cmds = append(cmds, cmd)
		}
	case eventMsg:
		m.addEvent(msg.event)
		cmds = append(cmds, waitForEvent(m.session.events))
	case approvalMsg:
		m.approval = &approvalState{msg: msg}
		m.mode = modeApproval
		m.status = fmt.Sprintf("approval required: %s (%s)", msg.request.Call.Name, msg.request.Risk)
		m.view = viewEvent
		m.updateDetail()
		cmds = append(cmds, waitForApproval(m.session.approvals))
	case runDoneMsg:
		m.running = false
		m.done = true
		m.cancel = nil
		m.result = msg.result
		m.runErr = msg.err
		if msg.err != nil {
			m.status = "error: " + msg.err.Error()
		} else {
			m.status = "finished: " + msg.result.Status
		}
		m.updateDetail()
		if m.loop && m.approval == nil {
			m.prepareTaskInput(true)
			cmds = append(cmds, m.command.Focus())
		}
	case editorDoneMsg:
		if msg.err != nil {
			m.status = "editor error: " + msg.err.Error()
		} else {
			m.status = "editor closed"
		}
	case tea.KeyMsg:
		cmd := m.handleKey(msg)
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
	}

	return m, tea.Batch(cmds...)
}

func (m *model) handleKey(msg tea.KeyMsg) tea.Cmd {
	switch m.mode {
	case modeCommand:
		return m.handleCommandKey(msg)
	case modeTask:
		return m.handleTaskKey(msg)
	case modeSearch:
		return m.handleSearchKey(msg)
	case modeHelp:
		return m.handleHelpKey(msg)
	case modeApproval:
		return m.handleApprovalKey(msg)
	case modeQuitConfirm:
		return m.handleQuitConfirmKey(msg)
	default:
		return m.handleNormalKey(msg)
	}
}

func (m *model) handleNormalKey(msg tea.KeyMsg) tea.Cmd {
	keyString := msg.String()
	if m.handlePendingKey(keyString) {
		return nil
	}

	switch keyString {
	case "ctrl+c":
		return m.confirmQuit()
	case "q":
		if m.done {
			return tea.Quit
		}
		return m.confirmQuit()
	case "?":
		m.openHelp()
	case ":":
		return m.openCommandMode(":")
	case "i":
		if m.loop && !m.running {
			return m.openTaskMode()
		}
	case "/":
		return m.openSearchMode()
	case "tab", "shift+tab":
		m.toggleFocus()
	case "j", "down":
		m.moveSelection(1)
	case "k", "up":
		m.moveSelection(-1)
	case "h", "left":
		m.focus = "timeline"
	case "l", "right", "enter":
		m.focus = "detail"
	case "G", "end":
		m.selectIndex(len(m.events) - 1)
	case "ctrl+d":
		m.scrollDetailHalfPage(1)
	case "ctrl+u":
		m.scrollDetailHalfPage(-1)
	case "ctrl+f", "pagedown":
		m.scrollDetailPage(1)
	case "ctrl+b", "pageup":
		m.scrollDetailPage(-1)
	case "n":
		m.findMatch(1)
	case "N":
		m.findMatch(-1)
	case "d":
		m.view = viewDiff
		m.focus = "detail"
		m.updateDetail()
	case "t":
		m.view = viewEvent
		m.focus = "timeline"
		m.updateDetail()
	case "o":
		m.focus = "detail"
	case "g", "z":
		m.pendingKey = keyString
	default:
		m.pendingKey = ""
	}
	return nil
}

func (m *model) handlePendingKey(keyString string) bool {
	if m.pendingKey == "" {
		return false
	}
	pending := m.pendingKey
	m.pendingKey = ""
	switch pending + keyString {
	case "gg":
		m.selectIndex(0)
		return true
	case "zz":
		m.centerSelection()
		return true
	default:
		return false
	}
}

func (m *model) handleApprovalKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "ctrl+c":
		return m.confirmQuit()
	case "?":
		m.openHelp()
	case "e", "enter":
		if m.approval != nil {
			m.approval.expanded = !m.approval.expanded
			m.updateDetail()
		}
	case "y":
		m.answerApproval(policy.ApprovalDecision{Allowed: true, Reason: "approved by user"})
	case "n":
		m.answerApproval(policy.ApprovalDecision{Allowed: false, Reason: "denied by user"})
	case "a":
		m.answerApproval(policy.ApprovalDecision{Allowed: true, Reason: "approved by user for this risk", RememberRisk: true})
	}
	return nil
}

func (m *model) handleCommandKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc", "ctrl+c":
		m.closeInput()
		return nil
	case "enter":
		command := strings.TrimSpace(m.command.Value())
		m.closeInput()
		return m.executeCommand(command)
	default:
		var cmd tea.Cmd
		m.command, cmd = m.command.Update(msg)
		return cmd
	}
}

func (m *model) handleTaskKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "ctrl+c":
		return m.confirmQuit()
	case "esc":
		m.command.Blur()
		m.mode = modeNormal
		m.status = "task input closed; press i to start a task"
		return nil
	case "enter":
		input := strings.TrimSpace(m.command.Value())
		m.command.Reset()
		if input == "" {
			m.status = "enter a task or slash command"
			return nil
		}
		if strings.HasPrefix(input, "/") {
			return m.executeSlashCommand(input)
		}
		return m.startRun(core.Task{Text: input, Repo: m.task.Repo})
	default:
		var cmd tea.Cmd
		m.command, cmd = m.command.Update(msg)
		return cmd
	}
}

func (m *model) handleSearchKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc", "ctrl+c":
		m.closeInput()
		return nil
	case "enter":
		m.query = strings.TrimSpace(m.command.Value())
		m.closeInput()
		if m.query == "" {
			m.status = "search cleared"
			return nil
		}
		m.findMatch(1)
		return nil
	default:
		var cmd tea.Cmd
		m.command, cmd = m.command.Update(msg)
		return cmd
	}
}

func (m *model) handleHelpKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "q", "esc", "?":
		m.mode = m.beforeHelp
		if m.mode == modeHelp {
			m.mode = modeNormal
		}
	case "/", "n", "N":
		m.status = "help search is not implemented yet"
	}
	return nil
}

func (m *model) handleQuitConfirmKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "y", "q", "enter":
		m.canceled = true
		if m.cancel != nil {
			m.cancel()
		}
		return tea.Quit
	case "n", "esc":
		m.mode = modeNormal
		m.status = "cancel aborted"
	}
	return nil
}

func (m *model) confirmQuit() tea.Cmd {
	if !m.running {
		return tea.Quit
	}
	m.mode = modeQuitConfirm
	m.status = "run is active; press q/y/enter to cancel, esc/n to continue"
	return nil
}

func (m *model) openHelp() {
	m.beforeHelp = m.mode
	m.mode = modeHelp
}

func (m *model) openCommandMode(prompt string) tea.Cmd {
	m.mode = modeCommand
	m.command.Reset()
	m.command.Prompt = prompt
	m.command.Width = max(10, m.width-4)
	return m.command.Focus()
}

func (m *model) openSearchMode() tea.Cmd {
	m.mode = modeSearch
	m.command.Reset()
	m.command.Prompt = "/"
	m.command.Width = max(10, m.width-4)
	return m.command.Focus()
}

func (m *model) closeInput() {
	m.command.Blur()
	if m.approval != nil {
		m.mode = modeApproval
	} else if m.loop && !m.running && !m.done {
		m.prepareTaskInput(false)
	} else {
		m.mode = modeNormal
	}
}

func (m *model) executeCommand(command string) tea.Cmd {
	switch command {
	case "", "w":
		return nil
	case "q", "quit":
		return m.confirmQuit()
	case "q!", "quit!", "qa", "qa!":
		m.canceled = true
		if m.cancel != nil {
			m.cancel()
		}
		return tea.Quit
	case "h", "help":
		m.openHelp()
	case "d", "diff":
		m.view = viewDiff
		m.focus = "detail"
		m.updateDetail()
	case "t", "timeline", "events":
		m.view = viewEvent
		m.focus = "timeline"
		m.updateDetail()
	case "clear":
		m.clearSession()
	case "trace":
		m.view = viewTrace
		m.focus = "detail"
		m.status = "trajectory: " + m.trajectoryPath()
		m.updateDetail()
	case "open-trace":
		return m.openTrace()
	default:
		m.status = "unknown command: " + command
	}
	return nil
}

func (m *model) executeSlashCommand(command string) tea.Cmd {
	command = strings.TrimSpace(strings.TrimPrefix(command, "/"))
	switch command {
	case "":
		m.status = "empty slash command"
	case "clear":
		m.clearSession()
	case "q", "quit", "exit":
		return m.confirmQuit()
	case "h", "help", "?":
		m.openHelp()
	case "d", "diff":
		m.view = viewDiff
		m.focus = "detail"
		m.updateDetail()
	case "t", "timeline", "events":
		m.view = viewEvent
		m.focus = "timeline"
		m.updateDetail()
	case "trace":
		m.view = viewTrace
		m.focus = "detail"
		m.status = "trajectory: " + m.trajectoryPath()
		m.updateDetail()
	case "open-trace":
		return m.openTrace()
	default:
		m.status = "unknown slash command: /" + command
	}
	if m.loop && !m.running && m.mode != modeHelp {
		m.prepareTaskInput(true)
		return m.command.Focus()
	}
	return nil
}

func (m *model) openTaskMode() tea.Cmd {
	m.prepareTaskInput(false)
	return m.command.Focus()
}

func (m *model) prepareTaskInput(reset bool) {
	m.mode = modeTask
	m.command.Prompt = "task> "
	m.command.Placeholder = "type a task or /clear"
	m.command.Width = max(10, m.width-4)
	if reset {
		m.command.Reset()
	}
}

func (m *model) startRun(task core.Task) tea.Cmd {
	task.Text = strings.TrimSpace(task.Text)
	if task.Text == "" {
		m.status = "task is empty"
		return nil
	}
	if m.running {
		m.status = "run is already active"
		return nil
	}
	if task.Repo == "" {
		task.Repo = m.task.Repo
	}
	if task.Repo == "" && m.agent != nil && m.agent.Workspace != nil {
		task.Repo = m.agent.Workspace.Root()
	}
	m.task = task
	m.runCtx, m.cancel = context.WithCancel(m.parent)
	m.running = true
	m.done = false
	m.canceled = false
	m.result = agentpkg.Result{}
	m.runErr = nil
	m.startedAt = time.Now()
	m.mode = modeNormal
	m.command.Blur()
	m.view = viewEvent
	m.focus = "timeline"
	m.status = "running task: " + shortString(task.Text, 48)
	m.updateDetail()
	return runAgent(m.runCtx, m.agent, m.task)
}

func (m *model) clearSession() {
	m.events = nil
	m.selected = -1
	m.timelineOffset = 0
	m.result = agentpkg.Result{}
	m.runErr = nil
	m.done = false
	m.query = ""
	m.pendingKey = ""
	m.view = viewEvent
	m.focus = "timeline"
	m.detail.GotoTop()
	m.drainEvents()
	m.status = "cleared"
	m.updateDetail()
}

func (m *model) drainEvents() {
	for {
		select {
		case <-m.session.events:
		default:
			return
		}
	}
}

func (m *model) openTrace() tea.Cmd {
	path := m.trajectoryPath()
	if path == "" {
		m.status = "trajectory is not available yet"
		return nil
	}
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}
	return tea.ExecProcess(exec.Command(editor, path), func(err error) tea.Msg {
		return editorDoneMsg{err: err}
	})
}

func (m *model) answerApproval(decision policy.ApprovalDecision) {
	if m.approval == nil {
		return
	}
	m.approval.msg.response <- decision
	if decision.Allowed {
		m.status = "approved: " + m.approval.msg.request.Call.Name
	} else {
		m.status = "denied: " + m.approval.msg.request.Call.Name
	}
	m.approval = nil
	m.mode = modeNormal
	m.updateDetail()
}

func (m *model) addEvent(event core.Event) {
	follow := m.selected == len(m.events)-1 || m.selected < 0
	m.events = append(m.events, event)
	if follow {
		m.selectIndex(len(m.events) - 1)
	} else {
		m.updateDetail()
	}
	m.status = summarizeEvent(event)
}

func (m *model) moveSelection(delta int) {
	if len(m.events) == 0 {
		return
	}
	m.selectIndex(clamp(m.selected+delta, 0, len(m.events)-1))
}

func (m *model) selectIndex(index int) {
	if len(m.events) == 0 {
		m.selected = -1
		return
	}
	m.selected = clamp(index, 0, len(m.events)-1)
	m.ensureTimelineVisible()
	m.updateDetail()
}

func (m *model) centerSelection() {
	bodyHeight := m.bodyHeight()
	if bodyHeight <= 0 || m.selected < 0 {
		return
	}
	m.timelineOffset = max(0, m.selected-bodyHeight/2)
}

func (m *model) toggleFocus() {
	if m.focus == "timeline" {
		m.focus = "detail"
	} else {
		m.focus = "timeline"
	}
}

func (m *model) scrollDetailHalfPage(direction int) {
	if direction > 0 {
		m.detail.HalfPageDown()
	} else {
		m.detail.HalfPageUp()
	}
}

func (m *model) scrollDetailPage(direction int) {
	if direction > 0 {
		m.detail.PageDown()
	} else {
		m.detail.PageUp()
	}
}

func (m *model) findMatch(direction int) {
	if m.query == "" {
		m.status = "no search query"
		return
	}
	if len(m.events) == 0 {
		m.status = "no events to search"
		return
	}
	needle := strings.ToLower(m.query)
	start := m.selected
	for i := 1; i <= len(m.events); i++ {
		idx := (start + direction*i + len(m.events)*2) % len(m.events)
		haystack := strings.ToLower(summarizeEvent(m.events[idx]) + "\n" + eventDetail(m.events[idx]))
		if strings.Contains(haystack, needle) {
			m.selectIndex(idx)
			m.status = fmt.Sprintf("match %d: %s", idx+1, m.query)
			return
		}
	}
	m.status = "no match: " + m.query
}

func (m *model) resize() {
	bodyWidth := max(1, m.width-2)
	bodyHeight := max(1, m.bodyHeight())
	detailWidth := max(20, bodyWidth-m.timelineWidth()-1)
	m.detail.Width = detailWidth - 2
	m.detail.Height = bodyHeight - 2
	m.help.Width = m.width
	m.command.Width = max(10, m.width-4)
	m.ensureTimelineVisible()
	m.updateDetail()
}

func (m *model) bodyHeight() int {
	reserved := 4
	if m.approval != nil {
		reserved += lipgloss.Height(m.approvalView(max(1, m.width-2)))
	}
	if m.mode == modeCommand || m.mode == modeSearch || m.mode == modeTask {
		reserved += 1
	}
	return max(1, m.height-reserved)
}

func (m *model) timelineWidth() int {
	if m.width < 90 {
		return max(28, m.width/2)
	}
	return max(34, min(56, m.width*42/100))
}

func (m *model) ensureTimelineVisible() {
	bodyHeight := m.bodyHeight()
	if m.selected < 0 || bodyHeight <= 0 {
		m.timelineOffset = 0
		return
	}
	if m.selected < m.timelineOffset {
		m.timelineOffset = m.selected
	}
	if m.selected >= m.timelineOffset+bodyHeight {
		m.timelineOffset = m.selected - bodyHeight + 1
	}
	m.timelineOffset = max(0, m.timelineOffset)
}

func (m *model) updateDetail() {
	m.detail.SetContent(m.detailContent())
}

func (m *model) detailContent() string {
	switch m.view {
	case viewDiff:
		if m.result.Diff != "" {
			return m.result.Diff
		}
		if !m.done {
			return "Diff is available after the run finishes."
		}
		return "No diff."
	case viewTrace:
		return "Trajectory\n\n" + m.trajectoryPath()
	default:
		if m.approval != nil {
			return approvalDetail(m.approval)
		}
		if m.selected >= 0 && m.selected < len(m.events) {
			return eventDetail(m.events[m.selected])
		}
		if m.loop && !m.running {
			return "Type a task below to start.\n\nSlash commands: /clear, /quit, /help, /trace, /open-trace."
		}
		return "Waiting for events..."
	}
}

func (m *model) trajectoryPath() string {
	if m.result.TrajectoryPath != "" {
		return m.result.TrajectoryPath
	}
	if m.agent != nil && m.agent.Trajectory != nil {
		return m.agent.Trajectory.Path()
	}
	return ""
}

func (m *model) View() string {
	if m.width == 0 || m.height == 0 {
		return "starting..."
	}
	if m.mode == modeHelp {
		return m.helpView()
	}

	header := m.headerView()
	body := m.bodyView()
	parts := []string{header, body}
	if m.approval != nil {
		parts = append(parts, m.approvalView(m.width-2))
	}
	if m.mode == modeCommand || m.mode == modeSearch || m.mode == modeTask {
		parts = append(parts, inputStyle.Width(m.width-2).Render(m.command.View()))
	}
	parts = append(parts, m.footerView())
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

func (m *model) headerView() string {
	state := "idle"
	if m.running {
		state = "running"
	} else if m.done {
		state = "done"
	}
	if m.mode == modeApproval {
		state = "approval"
	}
	elapsed := time.Since(m.startedAt).Round(time.Second)
	modelName := ""
	if m.agent != nil {
		modelName = m.agent.Config.Model.Model
	}
	title := fmt.Sprintf(" swe-agent  %s %s  step %d  %s  %s ",
		m.spinner.View(), state, len(m.events), modelName, elapsed)
	if m.done {
		title = fmt.Sprintf(" swe-agent  %s  steps %d  %s ", m.result.Status, m.result.Steps, elapsed)
	}
	return headerStyle.Width(m.width).Render(truncate(title, m.width))
}

func (m *model) bodyView() string {
	bodyHeight := m.bodyHeight()
	timelineWidth := m.timelineWidth()
	detailWidth := max(20, m.width-timelineWidth-1)

	timeline := panelStyle.
		Width(timelineWidth).
		Height(bodyHeight).
		BorderForeground(focusColor(m.focus == "timeline")).
		Render(m.timelineView(timelineWidth-2, bodyHeight-2))
	detail := panelStyle.
		Width(detailWidth).
		Height(bodyHeight).
		BorderForeground(focusColor(m.focus == "detail")).
		Render(m.detail.View())
	return lipgloss.JoinHorizontal(lipgloss.Top, timeline, detail)
}

func (m *model) timelineView(width, height int) string {
	if len(m.events) == 0 {
		if m.loop && !m.running {
			return mutedStyle.Render("No events yet. Enter a task below.")
		}
		return mutedStyle.Render("Waiting for agent events...")
	}
	lines := make([]string, 0, height)
	end := min(len(m.events), m.timelineOffset+height)
	for i := m.timelineOffset; i < end; i++ {
		event := m.events[i]
		prefix := "  "
		style := timelineStyle
		if i == m.selected {
			prefix = "> "
			style = selectedStyle
		}
		line := fmt.Sprintf("%s %s", event.Time.Format("15:04:05"), summarizeEvent(event))
		lines = append(lines, style.Render(truncate(prefix+line, width)))
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

func (m *model) approvalView(width int) string {
	if m.approval == nil {
		return ""
	}
	req := m.approval.msg.request
	lines := []string{
		fmt.Sprintf("approval: %s  risk=%s", req.Call.Name, req.Risk),
		truncate(req.Spec.Description, width),
		"[y] allow once   [n] deny   [a] allow this risk for run   [e] expand   [?] help",
	}
	if m.approval.expanded {
		lines = append(lines, formatArgs(req.Call.Args))
	}
	return approvalStyle.Width(width).Render(strings.Join(lines, "\n"))
}

func (m *model) footerView() string {
	if m.mode == modeQuitConfirm {
		return footerStyle.Width(m.width).Render("confirm cancel: q/y/enter cancel | esc/n continue")
	}
	if m.status != "" {
		status := truncate(m.status, max(10, m.width/2))
		helpText := m.help.ShortHelpView(m.shortHelp())
		return footerStyle.Width(m.width).Render(lipgloss.JoinHorizontal(lipgloss.Top, statusStyle.Render(status), "  ", helpText))
	}
	return footerStyle.Width(m.width).Render(m.help.ShortHelpView(m.shortHelp()))
}

func (m *model) helpView() string {
	header := headerStyle.Width(m.width).Render(" swe-agent help ")
	contentHeight := max(1, m.height-2)
	content := panelStyle.
		Width(m.width - 2).
		Height(contentHeight).
		Render(m.help.FullHelpView(m.fullHelp()))
	footer := footerStyle.Width(m.width).Render("q/esc/? close help")
	return lipgloss.JoinVertical(lipgloss.Left, header, content, footer)
}

func (m *model) shortHelp() []key.Binding {
	switch m.mode {
	case modeApproval:
		return []key.Binding{keyAllow, keyDeny, keyRemember, keyHelp}
	case modeTask:
		return []key.Binding{keyEnter, keySlashClear, keyEsc}
	case modeCommand, modeSearch:
		return []key.Binding{keyEnter, keyEsc}
	default:
		return []key.Binding{keyMove, keyOpen, keyTaskInput, keyCommand, keySearch, keyHelp, keyQuit}
	}
}

func (m *model) fullHelp() [][]key.Binding {
	return [][]key.Binding{
		{keyMove, keyLeftRight, keyTop, keyBottom, keyCenter},
		{keyScrollHalf, keyScrollPage, keyTab, keyOpen},
		{keySearch, keyNext, keyPrev, keyCommand, keyHelp},
		{keyDiff, keyTimeline, keyTrace, keyOpenTrace},
		{keyTaskInput, keySlashClear, keySlashQuit},
		{keyAllow, keyDeny, keyRemember, keyExpandApproval},
		{keyQuit, keyCancel},
	}
}

func summarizeEvent(event core.Event) string {
	switch event.Type {
	case "user_task":
		return "task " + shortString(event.Data["task"], 48)
	case "model_request":
		return fmt.Sprintf("model_request step=%v messages=%v", event.Data["step"], event.Data["messages"])
	case "model_response":
		return "model_response " + shortString(event.Data["content"], 48)
	case "tool_call":
		return fmt.Sprintf("tool_call %v", event.Data["tool"])
	case "tool_result":
		return fmt.Sprintf("tool_result %v code=%v", event.Data["tool"], event.Data["code"])
	case "tool_denied":
		return fmt.Sprintf("tool_denied %v", event.Data["tool"])
	case "error":
		return "error " + shortString(event.Data["error"], 48)
	case "final":
		return fmt.Sprintf("final status=%v steps=%v", event.Data["status"], event.Data["steps"])
	default:
		if event.Type == "" {
			return "event"
		}
		return event.Type
	}
}

func eventDetail(event core.Event) string {
	var b bytes.Buffer
	fmt.Fprintf(&b, "type: %s\n", event.Type)
	if !event.Time.IsZero() {
		fmt.Fprintf(&b, "time: %s\n", event.Time.Format(time.RFC3339))
	}
	if len(event.Data) > 0 {
		b.WriteString("\ndata:\n")
		data, err := json.MarshalIndent(event.Data, "", "  ")
		if err != nil {
			fmt.Fprintf(&b, "%v\n", event.Data)
		} else {
			b.Write(data)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func approvalDetail(state *approvalState) string {
	req := state.msg.request
	var b strings.Builder
	fmt.Fprintf(&b, "Tool approval\n\n")
	fmt.Fprintf(&b, "tool: %s\n", req.Call.Name)
	fmt.Fprintf(&b, "risk: %s\n", req.Risk)
	fmt.Fprintf(&b, "reason: %s\n", req.Reason)
	fmt.Fprintf(&b, "description: %s\n\n", req.Spec.Description)
	fmt.Fprintf(&b, "args:\n%s\n", formatArgs(req.Call.Args))
	return b.String()
}

func formatArgs(args map[string]any) string {
	if len(args) == 0 {
		return "{}"
	}
	keys := make([]string, 0, len(args))
	for key := range args {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	ordered := map[string]any{}
	for _, key := range keys {
		ordered[key] = args[key]
	}
	data, err := json.MarshalIndent(ordered, "", "  ")
	if err != nil {
		return fmt.Sprint(args)
	}
	return string(data)
}

func shortString(value any, limit int) string {
	s := strings.TrimSpace(fmt.Sprint(value))
	s = strings.ReplaceAll(s, "\n", " ")
	return truncate(s, limit)
}

func truncate(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	if limit <= 1 {
		return string(runes[:limit])
	}
	if limit <= 3 {
		return string(runes[:limit])
	}
	return string(runes[:limit-3]) + "..."
}

func clamp(v, low, high int) int {
	if high < low {
		return low
	}
	if v < low {
		return low
	}
	if v > high {
		return high
	}
	return v
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

func focusColor(focused bool) lipgloss.Color {
	if focused {
		return colorAccent
	}
	return colorBorder
}

var (
	colorAccent = lipgloss.Color("39")
	colorBorder = lipgloss.Color("238")
	colorMuted  = lipgloss.Color("244")

	headerStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("15")).
			Background(lipgloss.Color("24")).
			Bold(true)
	panelStyle = lipgloss.NewStyle().
			Border(lipgloss.NormalBorder()).
			Padding(0, 1)
	footerStyle = lipgloss.NewStyle().
			Foreground(colorMuted)
	statusStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("250"))
	mutedStyle = lipgloss.NewStyle().
			Foreground(colorMuted)
	timelineStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("252"))
	selectedStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("15")).
			Background(lipgloss.Color("31"))
	approvalStyle = lipgloss.NewStyle().
			Border(lipgloss.NormalBorder()).
			BorderForeground(lipgloss.Color("214")).
			Padding(0, 1)
	inputStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("252"))
)

var (
	keyMove           = key.NewBinding(key.WithKeys("j/k", "up/down"), key.WithHelp("j/k", "move"))
	keyLeftRight      = key.NewBinding(key.WithKeys("h/l", "left/right"), key.WithHelp("h/l", "focus pane"))
	keyTop            = key.NewBinding(key.WithKeys("gg"), key.WithHelp("gg", "top"))
	keyBottom         = key.NewBinding(key.WithKeys("G"), key.WithHelp("G", "bottom"))
	keyCenter         = key.NewBinding(key.WithKeys("zz"), key.WithHelp("zz", "center"))
	keyScrollHalf     = key.NewBinding(key.WithKeys("ctrl+d", "ctrl+u"), key.WithHelp("ctrl+d/u", "half page"))
	keyScrollPage     = key.NewBinding(key.WithKeys("ctrl+f", "ctrl+b"), key.WithHelp("ctrl+f/b", "page"))
	keyTab            = key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "switch pane"))
	keyOpen           = key.NewBinding(key.WithKeys("enter", "o"), key.WithHelp("enter/o", "detail"))
	keySearch         = key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "search"))
	keyNext           = key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "next match"))
	keyPrev           = key.NewBinding(key.WithKeys("N"), key.WithHelp("N", "prev match"))
	keyTaskInput      = key.NewBinding(key.WithKeys("i"), key.WithHelp("i", "task input"))
	keyCommand        = key.NewBinding(key.WithKeys(":"), key.WithHelp(":", "command"))
	keyHelp           = key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help"))
	keyDiff           = key.NewBinding(key.WithKeys("d", ":diff"), key.WithHelp("d", "diff"))
	keyTimeline       = key.NewBinding(key.WithKeys("t", ":timeline"), key.WithHelp("t", "timeline"))
	keyTrace          = key.NewBinding(key.WithKeys(":trace"), key.WithHelp(":trace", "trace path"))
	keyOpenTrace      = key.NewBinding(key.WithKeys(":open-trace"), key.WithHelp(":open-trace", "$EDITOR trace"))
	keySlashClear     = key.NewBinding(key.WithKeys("/clear"), key.WithHelp("/clear", "clear"))
	keySlashQuit      = key.NewBinding(key.WithKeys("/quit"), key.WithHelp("/quit", "quit"))
	keyAllow          = key.NewBinding(key.WithKeys("y"), key.WithHelp("y", "allow"))
	keyDeny           = key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "deny"))
	keyRemember       = key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "allow risk"))
	keyExpandApproval = key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "expand approval"))
	keyQuit           = key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit"))
	keyCancel         = key.NewBinding(key.WithKeys("ctrl+c"), key.WithHelp("ctrl+c", "cancel"))
	keyEnter          = key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "submit"))
	keyEsc            = key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back"))
)
