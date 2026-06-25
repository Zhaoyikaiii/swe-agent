package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
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

type narrativeReadyMsg struct {
	taskID int
	body   string
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
	viewOverview detailView = iota
	viewSteps
	viewDiff
	viewTests
	viewTrace
)

type traceTab int

const (
	traceTabTrace traceTab = iota
	traceTabFrontier
	traceTabMemory
	traceTabEvents
	traceTabPrompt
	traceTabCards
)

type traceWorkspaceState struct {
	Tab traceTab
}

type sidebarMode int

const (
	sidebarRun sidebarMode = iota
	sidebarHistory
)

type runPhase int

const (
	phaseStarting runPhase = iota
	phaseReady
	phaseThinking
	phaseProcessing
	phaseTool
	phaseApproval
	phaseFinished
	phaseError
	phaseCanceled
)

type chatEntry struct {
	Role  string
	Title string
	Body  string
	Time  time.Time
}

type RunNarrative struct {
	Status  string
	Body    string
	Error   string
	Updated time.Time
}

type taskRecord struct {
	ID         int
	Task       core.Task
	Events     []core.Event
	Chat       []chatEntry
	Result     agentpkg.Result
	RunErr     error
	Status     string
	Summary    string
	Narrative  RunNarrative
	StartedAt  time.Time
	FinishedAt time.Time
	Selected   int
	ChatOffset int
}

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
	sidebar    sidebarMode
	focus      string

	tasks         []taskRecord
	nextTaskID    int
	activeTask    int
	selectedTask  int
	selected      int
	chatOffset    int
	historyOffset int
	detail        viewport.Model
	help          help.Model
	spinner       spinner.Model
	command       textinput.Model

	approval *approvalState
	result   agentpkg.Result
	runErr   error
	running  bool
	done     bool
	canceled bool

	query      string
	status     string
	phase      runPhase
	phaseHint  string
	pendingKey string
	startedAt  time.Time
	traceView  traceWorkspaceState
}

func newRunModel(session *Session, ag *agentpkg.Agent, task core.Task, parent context.Context) *model {
	m := newModel(session, ag, task, parent)
	m.start = true
	m.status = "starting"
	m.setPhase(phaseStarting, "starting agent")
	return m
}

func newLoopModel(session *Session, ag *agentpkg.Agent, repo string, parent context.Context) *model {
	m := newModel(session, ag, core.Task{Repo: repo}, parent)
	m.loop = true
	m.mode = modeTask
	m.status = "ready"
	m.setPhase(phaseReady, "enter a task")
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
		session:      session,
		agent:        ag,
		task:         task,
		parent:       parent,
		mode:         modeNormal,
		view:         viewOverview,
		sidebar:      sidebarRun,
		focus:        "sidebar",
		nextTaskID:   1,
		activeTask:   -1,
		selectedTask: -1,
		selected:     -1,
		detail:       detail,
		help:         h,
		spinner:      spin,
		command:      command,
		status:       "starting",
		phase:        phaseStarting,
		phaseHint:    "starting agent",
		startedAt:    time.Now(),
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
		m.setPhase(phaseApproval, fmt.Sprintf("%s requires %s approval", msg.request.Call.Name, msg.request.Risk))
		m.view = viewOverview
		m.updateDetail()
		cmds = append(cmds, waitForApproval(m.session.approvals))
	case runDoneMsg:
		m.running = false
		m.done = true
		m.cancel = nil
		m.result = msg.result
		m.runErr = msg.err
		m.finishActiveTask(msg.result, msg.err)
		if record := m.activeTaskRecord(); record != nil {
			record.Narrative.Status = "pending"
			record.Narrative.Updated = time.Now()
			snapshot := m.runSnapshot(*record)
			cmds = append(cmds, generateNarrativeCmd(m.parent, m.agent, record.ID, snapshot, record.Events))
		}
		if msg.err != nil {
			m.status = "error: " + msg.err.Error()
			m.setPhase(phaseError, msg.err.Error())
		} else {
			m.status = "finished: " + msg.result.Status
			m.setPhase(phaseFinished, valueOrDefault(msg.result.Status, "finished"))
		}
		m.updateDetail()
		if m.loop && m.approval == nil {
			m.prepareTaskInput(true)
			cmds = append(cmds, m.command.Focus())
		}
	case narrativeReadyMsg:
		record := m.findTaskByID(msg.taskID)
		if record != nil {
			record.Narrative.Updated = time.Now()
			if msg.err != nil {
				record.Narrative.Status = "failed"
				record.Narrative.Error = msg.err.Error()
			} else {
				record.Narrative.Status = "generated"
				record.Narrative.Body = msg.body
				record.Narrative.Error = ""
			}
			m.updateDetail()
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
	if m.view == viewTrace {
		switch keyString {
		case "q", "esc":
			m.view = viewOverview
			m.status = "run overview"
			m.updateDetail()
			return nil
		case "tab", "l", "right":
			m.cycleTraceTab(1)
			return nil
		case "shift+tab", "h", "left":
			m.cycleTraceTab(-1)
			return nil
		case "1", "2", "3", "4", "5", "6":
			m.setTraceTab(keyString)
			return nil
		}
	}
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
		m.focus = "sidebar"
	case "l", "right":
		m.focus = "detail"
	case "enter":
		if m.sidebar == sidebarHistory && m.focus == "sidebar" {
			m.showRun()
			return nil
		}
		m.focus = "detail"
	case "G", "end":
		m.selectLast()
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
	case "v":
		m.view = viewTests
		m.focus = "detail"
		m.updateDetail()
	case "s":
		m.view = viewSteps
		m.focus = "detail"
		m.updateDetail()
	case "x":
		m.view = viewTrace
		m.focus = "detail"
		m.status = "trace workspace"
		m.updateDetail()
	case "t":
		m.showRun()
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
		m.selectFirst()
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
		m.setPhase(phaseCanceled, "run canceled")
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
	m.command.Width = max(10, m.width-16)
	return m.command.Focus()
}

func (m *model) openSearchMode() tea.Cmd {
	m.mode = modeSearch
	m.command.Reset()
	m.command.Prompt = "/"
	m.command.Width = max(10, m.width-16)
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
	case "tests", "validation":
		m.view = viewTests
		m.focus = "detail"
		m.updateDetail()
	case "s", "steps":
		m.view = viewSteps
		m.focus = "detail"
		m.updateDetail()
	case "t", "timeline", "events", "chat", "run", "overview":
		m.showRun()
	case "clear":
		m.clearSession()
	case "history", "tasks":
		m.showHistory()
	case "trace":
		m.view = viewTrace
		m.focus = "detail"
		m.status = "trace workspace"
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
	case "tests", "validation":
		m.view = viewTests
		m.focus = "detail"
		m.updateDetail()
	case "s", "steps":
		m.view = viewSteps
		m.focus = "detail"
		m.updateDetail()
	case "t", "timeline", "events", "chat", "run", "overview":
		m.showRun()
	case "history", "tasks":
		m.showHistory()
	case "trace":
		m.view = viewTrace
		m.focus = "detail"
		m.status = "trace workspace"
		m.updateDetail()
	case "open-trace":
		return m.openTrace()
	default:
		m.status = "unknown slash command: /" + command
	}
	if m.loop && !m.running && m.mode == modeTask {
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
	m.command.Placeholder = "type a task or /history"
	m.command.Width = max(10, m.width-18)
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
	taskIndex := m.createTaskRecord(task, "running", m.startedAt)
	m.activeTask = taskIndex
	m.setSelectedTask(taskIndex)
	m.mode = modeNormal
	m.command.Blur()
	m.view = viewOverview
	m.sidebar = sidebarRun
	m.focus = "sidebar"
	m.status = "running task: " + shortString(task.Text, 48)
	m.setPhase(phaseThinking, "preparing task")
	m.updateDetail()
	return runAgent(m.runCtx, m.agent, m.task)
}

func (m *model) clearSession() {
	if m.running {
		m.status = "cannot clear while a task is running"
		return
	}
	m.tasks = nil
	m.nextTaskID = 1
	m.activeTask = -1
	m.selectedTask = -1
	m.selected = -1
	m.chatOffset = 0
	m.historyOffset = 0
	m.result = agentpkg.Result{}
	m.runErr = nil
	m.done = false
	m.query = ""
	m.pendingKey = ""
	m.view = viewOverview
	m.sidebar = sidebarRun
	m.focus = "sidebar"
	m.detail.GotoTop()
	m.drainEvents()
	m.status = "cleared"
	m.setPhase(phaseReady, "enter a task")
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
		m.setPhase(phaseProcessing, "approval accepted")
	} else {
		m.status = "denied: " + m.approval.msg.request.Call.Name
		m.setPhase(phaseProcessing, "approval denied")
	}
	m.approval = nil
	m.mode = modeNormal
	m.updateDetail()
}

func (m *model) createTaskRecord(task core.Task, status string, startedAt time.Time) int {
	if m.nextTaskID == 0 {
		m.nextTaskID = 1
	}
	if startedAt.IsZero() {
		startedAt = time.Now()
	}
	record := taskRecord{
		ID:        m.nextTaskID,
		Task:      task,
		Status:    status,
		StartedAt: startedAt,
		Selected:  -1,
	}
	m.nextTaskID++
	m.tasks = append(m.tasks, record)
	return len(m.tasks) - 1
}

func (m *model) selectedTaskRecord() *taskRecord {
	if m.selectedTask < 0 || m.selectedTask >= len(m.tasks) {
		return nil
	}
	return &m.tasks[m.selectedTask]
}

func (m *model) activeTaskRecord() *taskRecord {
	if m.activeTask < 0 || m.activeTask >= len(m.tasks) {
		return nil
	}
	return &m.tasks[m.activeTask]
}

func (m *model) findTaskByID(taskID int) *taskRecord {
	for i := range m.tasks {
		if m.tasks[i].ID == taskID {
			return &m.tasks[i]
		}
	}
	return nil
}

func (m *model) selectedEvents() []core.Event {
	record := m.selectedTaskRecord()
	if record == nil {
		return nil
	}
	return record.Events
}

func (m *model) selectedChat() []chatEntry {
	record := m.selectedTaskRecord()
	if record == nil {
		return nil
	}
	return record.Chat
}

func (m *model) selectedResult() agentpkg.Result {
	record := m.selectedTaskRecord()
	if record == nil {
		return m.result
	}
	return record.Result
}

func (m *model) selectedSteps() []StepCard {
	record := m.selectedTaskRecord()
	if record == nil {
		return nil
	}
	return m.runSnapshot(*record).Steps
}

func (m *model) runSnapshot(record taskRecord) RunSnapshot {
	return BuildRunSnapshot(record, m.trajectoryPath())
}

func (m *model) saveSelectedTaskView() {
	if record := m.selectedTaskRecord(); record != nil {
		record.Selected = m.selected
		record.ChatOffset = m.chatOffset
	}
}

func (m *model) setSelectedTask(index int) {
	m.saveSelectedTaskView()
	if len(m.tasks) == 0 {
		m.selectedTask = -1
		m.selected = -1
		m.chatOffset = 0
		return
	}
	m.selectedTask = clamp(index, 0, len(m.tasks)-1)
	record := m.selectedTaskRecord()
	if record == nil {
		m.selected = -1
		m.chatOffset = 0
		return
	}
	entries := record.Chat
	if record.Selected < 0 || len(entries) == 0 {
		m.selected = -1
		m.chatOffset = max(0, record.ChatOffset)
		return
	}
	m.selected = clamp(record.Selected, 0, len(entries)-1)
	m.chatOffset = max(0, record.ChatOffset)
	m.ensureChatVisible()
}

func (m *model) ensureEventTask(event core.Event) int {
	if m.activeTask >= 0 && m.activeTask < len(m.tasks) {
		return m.activeTask
	}
	task := m.task
	if event.Type == "user_task" {
		task.Text = strings.TrimSpace(fmt.Sprint(event.Data["task"]))
		task.Repo = strings.TrimSpace(fmt.Sprint(event.Data["repo"]))
	}
	if task.Repo == "" && m.agent != nil && m.agent.Workspace != nil {
		task.Repo = m.agent.Workspace.Root()
	}
	status := "running"
	if event.Type == "final" {
		status = strings.TrimSpace(fmt.Sprint(event.Data["status"]))
	}
	if status == "" {
		status = "running"
	}
	startedAt := event.Time
	if startedAt.IsZero() {
		startedAt = time.Now()
	}
	idx := m.createTaskRecord(task, status, startedAt)
	m.activeTask = idx
	if m.selectedTask < 0 {
		m.setSelectedTask(idx)
	}
	return idx
}

func (m *model) updateTaskFromEvent(record *taskRecord, event core.Event) {
	if record == nil {
		return
	}
	switch event.Type {
	case "user_task":
		if text := strings.TrimSpace(fmt.Sprint(event.Data["task"])); text != "" {
			record.Task.Text = text
		}
		if repo := strings.TrimSpace(fmt.Sprint(event.Data["repo"])); repo != "" {
			record.Task.Repo = repo
		}
		if record.Status == "" {
			record.Status = "running"
		}
		if !event.Time.IsZero() {
			record.StartedAt = event.Time
		}
	case "error":
		record.Status = "error"
	case "final":
		status := strings.TrimSpace(fmt.Sprint(event.Data["status"]))
		if status == "" {
			status = "done"
		}
		record.Status = status
		record.Result.Status = status
		record.Result.Steps = intValue(event.Data["steps"])
		record.Result.Submission = cleanSubmission(event.Data["submission"])
		if !event.Time.IsZero() {
			record.FinishedAt = event.Time
		}
	default:
		if record.Status == "" {
			record.Status = "running"
		}
	}
}

func (m *model) updateTaskChat(record *taskRecord, event core.Event) {
	if record == nil {
		return
	}
	switch event.Type {
	case "user_task":
		body := strings.TrimSpace(fmt.Sprint(event.Data["task"]))
		if body == "" {
			return
		}
		record.Chat = append(record.Chat, chatEntry{
			Role:  "user",
			Title: "Task",
			Body:  body,
			Time:  event.Time,
		})
	case "tool_denied":
		toolName := strings.TrimSpace(fmt.Sprint(event.Data["tool"]))
		if toolName == "" {
			toolName = "tool"
		}
		body := toolName + " denied"
		if reason := strings.TrimSpace(fmt.Sprint(event.Data["reason"])); reason != "" && reason != "<nil>" {
			body += "\n\n" + reason
		}
		record.Chat = append(record.Chat, chatEntry{
			Role:  "attention",
			Title: "Attention",
			Body:  body,
			Time:  event.Time,
		})
	case "error":
		body := strings.TrimSpace(fmt.Sprint(event.Data["error"]))
		if body == "" {
			return
		}
		record.Chat = append(record.Chat, chatEntry{
			Role:  "attention",
			Title: "Attention",
			Body:  body,
			Time:  event.Time,
		})
	case "final":
		record.Summary = taskSummary(*record)
		m.upsertSummaryEntry(record, event.Time)
	}
}

func (m *model) finishActiveTask(result agentpkg.Result, err error) {
	record := m.activeTaskRecord()
	if record == nil {
		return
	}
	wasViewingTask := m.activeTask == m.selectedTask && m.sidebar == sidebarRun
	follow := wasViewingTask && (m.selected == len(record.Chat)-1 || m.selected < 0)
	record.Result = result
	record.RunErr = err
	record.FinishedAt = time.Now()
	if err != nil {
		record.Status = "error"
	} else if strings.TrimSpace(result.Status) != "" {
		record.Status = result.Status
	} else {
		record.Status = "done"
	}
	record.Summary = taskSummary(*record)
	m.upsertSummaryEntry(record, time.Now())
	if wasViewingTask && follow {
		m.selected = -1
		m.saveSelectedTaskView()
		m.updateDetail()
		m.followTimelineBottom()
	} else if wasViewingTask {
		m.updateDetail()
	}
}

func (m *model) addEvent(event core.Event) {
	taskIndex := m.ensureEventTask(event)
	record := &m.tasks[taskIndex]
	wasViewingTask := taskIndex == m.selectedTask
	follow := wasViewingTask && m.sidebar == sidebarRun && (m.selected == len(record.Chat)-1 || m.selected < 0)
	record.Events = append(record.Events, event)
	m.updateTaskFromEvent(record, event)
	m.updateTaskChat(record, event)

	if wasViewingTask && m.sidebar == sidebarRun {
		if event.Type == "final" {
			m.selected = -1
			m.saveSelectedTaskView()
			m.updateDetail()
			if follow {
				m.followTimelineBottom()
			}
		} else if follow || m.selected < 0 {
			m.selectIndex(len(record.Chat) - 1)
			m.followTimelineBottom()
		} else {
			m.updateDetail()
		}
	} else if wasViewingTask {
		m.updateDetail()
	}
	m.updatePhaseFromEvent(event)
	m.status = summarizeEvent(event)
}

func (m *model) setPhase(phase runPhase, hint string) {
	m.phase = phase
	m.phaseHint = strings.TrimSpace(hint)
}

func (m *model) updatePhaseFromEvent(event core.Event) {
	switch event.Type {
	case "user_task":
		m.setPhase(phaseThinking, "preparing task")
	case "model_request":
		m.setPhase(phaseThinking, "waiting for model")
	case "model_response":
		m.setPhase(phaseProcessing, "processing model response")
	case "tool_call":
		toolName := strings.TrimSpace(fmt.Sprint(event.Data["tool"]))
		if toolName == "" {
			toolName = "tool"
		}
		m.setPhase(phaseTool, "running "+toolName)
	case "tool_result":
		toolName := strings.TrimSpace(fmt.Sprint(event.Data["tool"]))
		if toolName == "" {
			toolName = "tool"
		}
		m.setPhase(phaseProcessing, toolName+" finished")
	case "tool_denied":
		toolName := strings.TrimSpace(fmt.Sprint(event.Data["tool"]))
		if toolName == "" {
			toolName = "tool"
		}
		m.setPhase(phaseProcessing, toolName+" denied")
	case "error":
		m.setPhase(phaseError, shortString(event.Data["error"], 80))
	case "final":
		status := strings.TrimSpace(fmt.Sprint(event.Data["status"]))
		m.setPhase(phaseFinished, valueOrDefault(status, "finished"))
	}
}

func (m *model) phaseLabel() string {
	switch m.phase {
	case phaseStarting:
		return "Starting"
	case phaseReady:
		return "Ready"
	case phaseThinking:
		return "Thinking"
	case phaseProcessing:
		return "Processing"
	case phaseTool:
		return "Running tool"
	case phaseApproval:
		return "Waiting approval"
	case phaseFinished:
		return "Finished"
	case phaseError:
		return "Error"
	case phaseCanceled:
		return "Canceled"
	default:
		return "Ready"
	}
}

func (m *model) statusHint() string {
	if hint := strings.TrimSpace(m.phaseHint); hint != "" {
		return hint
	}
	switch m.phase {
	case phaseReady:
		return "enter a task"
	case phaseThinking:
		return "waiting for model"
	case phaseProcessing:
		return "processing"
	case phaseTool:
		return "running tool"
	case phaseApproval:
		return "waiting for approval"
	case phaseFinished:
		return "finished"
	case phaseError:
		return "error"
	case phaseCanceled:
		return "canceled"
	default:
		return "starting"
	}
}

func (m *model) moveSelection(delta int) {
	if m.sidebar == sidebarHistory {
		m.moveTaskSelection(delta)
	} else {
		m.moveRunSelection(delta)
	}
}

func (m *model) moveRunSelection(delta int) {
	entries := m.selectedChat()
	if len(entries) == 0 {
		return
	}
	m.selectIndex(clamp(m.selected+delta, 0, len(entries)-1))
}

func (m *model) moveTaskSelection(delta int) {
	if len(m.tasks) == 0 {
		return
	}
	m.selectTaskIndex(clamp(m.selectedTask+delta, 0, len(m.tasks)-1))
}

func (m *model) selectIndex(index int) {
	entries := m.selectedChat()
	if len(entries) == 0 {
		m.selected = -1
		m.saveSelectedTaskView()
		m.updateDetail()
		return
	}
	m.selected = clamp(index, 0, len(entries)-1)
	m.ensureChatVisible()
	m.saveSelectedTaskView()
	m.updateDetail()
}

func (m *model) selectTaskIndex(index int) {
	if len(m.tasks) == 0 {
		m.selectedTask = -1
		m.selected = -1
		m.historyOffset = 0
		m.updateDetail()
		return
	}
	m.setSelectedTask(index)
	m.ensureHistoryVisible()
	m.updateDetail()
}

func (m *model) selectFirst() {
	if m.sidebar == sidebarHistory {
		m.selectTaskIndex(0)
		return
	}
	m.selectIndex(0)
}

func (m *model) selectLast() {
	if m.sidebar == sidebarHistory {
		m.selectTaskIndex(len(m.tasks) - 1)
		return
	}
	m.selectIndex(len(m.selectedChat()) - 1)
}

func (m *model) centerSelection() {
	bodyHeight := m.bodyHeight()
	if bodyHeight <= 0 {
		return
	}
	if m.sidebar == sidebarHistory {
		if m.selectedTask >= 0 {
			m.historyOffset = max(0, m.selectedTask-bodyHeight/2)
		}
		return
	}
	if m.selected >= 0 {
		m.chatOffset = max(0, m.selected-bodyHeight/2)
		m.saveSelectedTaskView()
	}
}

func (m *model) toggleFocus() {
	if m.focus == "sidebar" {
		m.focus = "detail"
	} else {
		m.focus = "sidebar"
	}
}

func (m *model) cycleTraceTab(delta int) {
	total := int(traceTabCards) + 1
	next := (int(m.traceView.Tab) + delta + total) % total
	m.traceView.Tab = traceTab(next)
	m.status = "trace: " + traceTabLabel(m.traceView.Tab)
	m.updateDetail()
}

func (m *model) followTimelineBottom() {
	if m.sidebar == sidebarRun && m.view == viewOverview {
		m.detail.GotoBottom()
	}
}

func (m *model) setTraceTab(key string) {
	switch key {
	case "1":
		m.traceView.Tab = traceTabTrace
	case "2":
		m.traceView.Tab = traceTabFrontier
	case "3":
		m.traceView.Tab = traceTabMemory
	case "4":
		m.traceView.Tab = traceTabEvents
	case "5":
		m.traceView.Tab = traceTabPrompt
	case "6":
		m.traceView.Tab = traceTabCards
	}
	m.status = "trace: " + traceTabLabel(m.traceView.Tab)
	m.updateDetail()
}

func (m *model) showHistory() {
	m.sidebar = sidebarHistory
	m.view = viewOverview
	m.focus = "sidebar"
	if len(m.tasks) > 0 && m.selectedTask < 0 {
		m.setSelectedTask(len(m.tasks) - 1)
	}
	m.ensureHistoryVisible()
	m.mode = modeNormal
	m.command.Blur()
	m.status = fmt.Sprintf("history: %d tasks", len(m.tasks))
	m.updateDetail()
}

func (m *model) showRun() {
	m.sidebar = sidebarRun
	m.view = viewOverview
	m.focus = "sidebar"
	if len(m.tasks) > 0 && m.selectedTask < 0 {
		m.setSelectedTask(len(m.tasks) - 1)
	}
	m.ensureChatVisible()
	m.mode = modeNormal
	m.command.Blur()
	m.status = "run overview"
	m.updateDetail()
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
	if m.sidebar == sidebarHistory {
		m.findTaskMatch(direction)
		return
	}
	m.findRunMatch(direction)
}

func (m *model) findRunMatch(direction int) {
	entries := m.selectedChat()
	if len(entries) == 0 {
		m.status = "no run summary to search"
		return
	}
	needle := strings.ToLower(m.query)
	start := m.selected
	if start < 0 {
		start = 0
	}
	for i := 1; i <= len(entries); i++ {
		idx := (start + direction*i + len(entries)*2) % len(entries)
		haystack := strings.ToLower(chatLine(entries[idx]) + "\n" + chatDetailWidth(entries[idx], m.detail.Width))
		if strings.Contains(haystack, needle) {
			m.selectIndex(idx)
			m.status = fmt.Sprintf("match %d: %s", idx+1, m.query)
			return
		}
	}
	m.status = "no match: " + m.query
}

func (m *model) findTaskMatch(direction int) {
	if len(m.tasks) == 0 {
		m.status = "no tasks to search"
		return
	}
	needle := strings.ToLower(m.query)
	start := m.selectedTask
	if start < 0 {
		start = 0
	}
	for i := 1; i <= len(m.tasks); i++ {
		idx := (start + direction*i + len(m.tasks)*2) % len(m.tasks)
		haystack := strings.ToLower(taskHistoryLine(m.tasks[idx]) + "\n" + taskDetailWidth(m.tasks[idx], m.detail.Width))
		if strings.Contains(haystack, needle) {
			m.selectTaskIndex(idx)
			m.status = fmt.Sprintf("task match %d: %s", idx+1, m.query)
			return
		}
	}
	m.status = "no task match: " + m.query
}

func (m *model) resize() {
	m.syncDetailSize()
	m.help.Width = m.width
	m.command.Width = max(10, m.width-18)
	if m.sidebar == sidebarHistory {
		m.ensureHistoryVisible()
	} else {
		m.ensureChatVisible()
	}
	m.updateDetail()
}

func (m *model) syncDetailSize() {
	if m.width <= 0 || m.height <= 0 {
		return
	}
	m.detail.Width = m.contentWidth()
	m.detail.Height = max(1, m.bodyHeight())
}

func (m *model) contentWidth() int {
	if m.sidebar == sidebarHistory {
		return max(20, m.width-m.sidebarWidth()-1) - 2
	}
	if m.cockpitLayout() {
		return max(20, m.inspectorWidth()-2)
	}
	return max(20, m.width-2) - 2
}

func (m *model) cockpitLayout() bool {
	return m.sidebar != sidebarHistory && m.width >= 108
}

func (m *model) inspectorWidth() int {
	if m.width < 108 {
		return 0
	}
	return max(32, min(44, m.width*34/100))
}

func (m *model) bodyHeight() int {
	if m.width <= 0 || m.height <= 0 {
		return 1
	}
	reserved := lipgloss.Height(m.headerView()) + lipgloss.Height(m.footerView())
	if m.approval != nil {
		reserved += lipgloss.Height(m.approvalView(max(1, m.width-2)))
	}
	if m.composerVisible() {
		reserved += lipgloss.Height(m.inputView())
	}
	_, panelFrameH := panelStyle.GetFrameSize()
	return max(1, m.height-reserved-panelFrameH)
}

func (m *model) composerVisible() bool {
	if m.mode == modeHelp || m.mode == modeQuitConfirm {
		return false
	}
	if m.approval != nil {
		return false
	}
	if m.mode == modeCommand || m.mode == modeSearch || m.mode == modeTask {
		return true
	}
	return m.loop
}

func (m *model) sidebarWidth() int {
	if m.width < 90 {
		return max(28, m.width/2)
	}
	return max(34, min(56, m.width*42/100))
}

func (m *model) ensureChatVisible() {
	bodyHeight := m.sidebarListHeight()
	steps := m.selectedSteps()
	if m.selected < 0 || len(steps) == 0 || bodyHeight <= 0 {
		m.chatOffset = 0
		return
	}
	if m.selected < m.chatOffset {
		m.chatOffset = m.selected
	}
	if m.selected >= m.chatOffset+bodyHeight {
		m.chatOffset = m.selected - bodyHeight + 1
	}
	m.chatOffset = max(0, m.chatOffset)
}

func (m *model) ensureHistoryVisible() {
	bodyHeight := m.sidebarListHeight()
	if m.selectedTask < 0 || bodyHeight <= 0 {
		m.historyOffset = 0
		return
	}
	if m.selectedTask < m.historyOffset {
		m.historyOffset = m.selectedTask
	}
	if m.selectedTask >= m.historyOffset+bodyHeight {
		m.historyOffset = m.selectedTask - bodyHeight + 1
	}
	m.historyOffset = max(0, m.historyOffset)
}

func (m *model) sidebarListHeight() int {
	return max(1, m.bodyHeight()-3)
}

func (m *model) updateDetail() {
	m.syncDetailSize()
	m.detail.SetContent(m.detailContent())
}

func (m *model) detailContent() string {
	switch m.view {
	case viewSteps:
		if record := m.selectedTaskRecord(); record != nil {
			return stepsViewWidth(m.runSnapshot(*record), m.detail.Width)
		}
		return "No steps recorded."
	case viewDiff:
		result := m.selectedResult()
		if result.Diff != "" {
			return result.Diff
		}
		if record := m.selectedTaskRecord(); record != nil && record.Status == "running" {
			return "Diff is available after the run finishes."
		}
		return "No diff."
	case viewTests:
		if record := m.selectedTaskRecord(); record != nil {
			return validationViewWidth(m.runSnapshot(*record), m.detail.Width)
		}
		return "No validation yet."
	case viewTrace:
		if record := m.selectedTaskRecord(); record != nil {
			return traceWorkspaceViewWidth(*record, m.traceView, m.detail.Width, m.trajectoryPath())
		}
		return "Problem Trace\n\nNo run selected."
	default:
		if m.approval != nil {
			return approvalDetail(m.approval)
		}
		if m.sidebar == sidebarHistory {
			if record := m.selectedTaskRecord(); record != nil {
				return taskDetailWidth(*record, m.detail.Width)
			}
			return "No task history yet."
		}
		if record := m.selectedTaskRecord(); record != nil {
			return timelineViewWidth(*record, m.runSnapshot(*record), m.detail.Width)
		}
		if m.loop && !m.running {
			return "Timeline\n\nYou\n  Type a task below to start.\n\nComposer\n  /history  /clear  /quit  /help  /diff  /trace  /open-trace"
		}
		return "Waiting for events..."
	}
}

func (m *model) trajectoryPath() string {
	if result := m.selectedResult(); result.TrajectoryPath != "" {
		return result.TrajectoryPath
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
		return m.helpOverlayView()
	}
	return m.appView()
}

func (m *model) appView() string {
	header := m.headerView()
	body := m.bodyView()
	parts := []string{header, body}
	if m.approval != nil {
		parts = append(parts, m.approvalView(m.width-2))
	}
	if m.composerVisible() {
		parts = append(parts, m.inputView())
	}
	parts = append(parts, m.footerView())
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

func (m *model) headerView() string {
	elapsed := time.Since(m.startedAt).Round(time.Second)
	taskLabel := fmt.Sprintf("task %d/%d", max(0, m.selectedTask+1), len(m.tasks))
	indicator := " "
	if m.running {
		indicator = m.spinner.View()
	}
	left := fmt.Sprintf(" swe-agent  %s %s  %s ", indicator, m.phaseLabel(), taskLabel)
	right := fmt.Sprintf(" %s  %s  %s ", m.profileLabel(), m.modelLabel(), elapsed)
	if m.done {
		right = fmt.Sprintf(" %s  %s ", m.profileLabel(), elapsed)
	}
	header := headerStyle.Width(m.width).Render(fillLine(left, right, m.width))
	return lipgloss.JoinVertical(lipgloss.Left, header, artifactBarStyle.Width(m.width).Render(m.artifactBar()))
}

func (m *model) modelLabel() string {
	if m.agent == nil {
		return ""
	}
	modelName := strings.TrimSpace(m.agent.Config.Model.Model)
	if modelName != "" {
		return modelName
	}
	provider := strings.TrimSpace(m.agent.Config.Model.Provider)
	if provider == "" {
		return ""
	}
	return provider + ":default"
}

func (m *model) profileLabel() string {
	if m.agent == nil {
		return "profile:default"
	}
	profile := strings.TrimSpace(m.agent.Config.Model.Profile)
	if profile == "" {
		profile = strings.TrimSpace(m.agent.Config.Agent.ActionMode)
	}
	if profile == "" {
		profile = "default"
	}
	policy := "manual"
	if m.agent.Config.Policy.AutoApproveRead && m.agent.Config.Policy.AutoApproveWrite && m.agent.Config.Policy.AutoApproveExec {
		policy = "auto"
	} else if m.agent.Config.Policy.AutoApproveRead {
		policy = "safe"
	}
	return profile + "/" + policy
}

func (m *model) artifactBar() string {
	record := m.selectedTaskRecord()
	if record == nil {
		return " Files 0   Diff none   Tests unknown   Approvals 0   Trace no "
	}
	snapshot := m.runSnapshot(*record)
	diffState := "none"
	if snapshot.FinalReview.ChangedFiles > 0 || record.Result.Diff != "" {
		diffState = "available"
	}
	traceState := "no"
	if snapshot.FinalReview.Trajectory != "" {
		traceState = "yes"
	}
	approvals := 0
	if m.approval != nil {
		approvals = 1
	}
	tests := "unknown"
	if snapshot.FinalReview.TestsRun > 0 {
		tests = fmt.Sprintf("%d/%d", snapshot.FinalReview.TestsPassed, snapshot.FinalReview.TestsRun)
	}
	return fmt.Sprintf(" Files %d   Diff %s   Tests %s   Approvals %d   Trace %s ",
		snapshot.FinalReview.ChangedFiles,
		diffState,
		tests,
		approvals,
		traceState,
	)
}

func (m *model) bodyView() string {
	m.syncDetailSize()
	bodyHeight := m.bodyHeight()
	if m.sidebar != sidebarHistory {
		if m.cockpitLayout() {
			inspectorWidth := m.inspectorWidth()
			timelineWidth := max(40, m.width-inspectorWidth-1)
			contentHeight := max(1, bodyHeight)
			timeline := panelStyle.
				Width(timelineWidth).
				Height(contentHeight).
				BorderForeground(focusColor(m.focus != "detail")).
				Render(fitHeightHeadTail(m.timelinePanel(timelineWidth-2), contentHeight))
			inspector := panelStyle.
				Width(inspectorWidth).
				Height(contentHeight).
				BorderForeground(focusColor(m.focus == "detail")).
				Render(fitHeight(m.inspectorView(inspectorWidth-2), contentHeight))
			return lipgloss.JoinHorizontal(lipgloss.Top, timeline, inspector)
		}
		return panelStyle.
			Width(max(20, m.width-2)).
			Height(bodyHeight).
			BorderForeground(focusColor(m.focus == "detail")).
			Render(m.detail.View())
	}

	sidebarWidth := m.sidebarWidth()
	detailWidth := max(20, m.width-sidebarWidth-1)

	sidebar := panelStyle.
		Width(sidebarWidth).
		Height(bodyHeight).
		BorderForeground(focusColor(m.focus == "sidebar")).
		Render(m.sidebarView(sidebarWidth-2, bodyHeight))
	detail := panelStyle.
		Width(detailWidth).
		Height(bodyHeight).
		BorderForeground(focusColor(m.focus == "detail")).
		Render(m.detail.View())
	return lipgloss.JoinHorizontal(lipgloss.Top, sidebar, detail)
}

func (m *model) timelinePanel(width int) string {
	if record := m.selectedTaskRecord(); record != nil {
		return timelineViewWidth(*record, m.runSnapshot(*record), width)
	}
	if m.loop && !m.running {
		return "You\n  Type a task below to start.\n\nAgent\n  Waiting for instructions."
	}
	return "Agent\n  Waiting for events..."
}

func (m *model) inspectorView(width int) string {
	record := m.selectedTaskRecord()
	if record == nil {
		return "Inspector\n\nNo run selected."
	}
	snapshot := m.runSnapshot(*record)
	var b strings.Builder
	b.WriteString("Inspector\n")
	b.WriteString(inspectorTabs(m.view))
	b.WriteString("\n\n")
	switch m.view {
	case viewSteps:
		b.WriteString(stepsViewWidth(snapshot, width))
	case viewDiff:
		if record.Result.Diff != "" {
			b.WriteString(record.Result.Diff)
		} else if record.Status == "running" {
			b.WriteString("Diff is available after the run finishes.\n")
		} else {
			b.WriteString("No diff.\n")
		}
	case viewTests:
		b.WriteString(validationViewWidth(snapshot, width))
	case viewTrace:
		b.WriteString(traceWorkspaceViewWidth(*record, m.traceView, width, m.trajectoryPath()))
	default:
		b.WriteString(planInspectorWidth(*record, snapshot, width))
	}
	return b.String()
}

func inspectorTabs(view detailView) string {
	tabs := []struct {
		view  detailView
		label string
	}{
		{viewOverview, "Plan"},
		{viewSteps, "Steps"},
		{viewDiff, "Diff"},
		{viewTests, "Tests"},
		{viewTrace, "Trace"},
	}
	parts := make([]string, 0, len(tabs))
	for _, tab := range tabs {
		label := tab.label
		if view == tab.view {
			label = "[" + label + "]"
		}
		parts = append(parts, label)
	}
	return strings.Join(parts, " ")
}

func (m *model) inputView() string {
	prompt := "Message"
	switch m.mode {
	case modeCommand:
		prompt = "Command"
	case modeSearch:
		prompt = "Search"
	case modeTask:
		prompt = "Task"
	default:
		if m.running {
			prompt = "Busy"
		}
	}
	label := inputLabelStyle.Render(" " + prompt + " ")
	frameW, _ := inputBoxStyle.GetFrameSize()
	boxWidth := max(1, m.width-frameW)
	inputWidth := max(1, boxWidth-lipgloss.Width(label)-3)
	m.command.Width = inputWidth
	line := lipgloss.JoinHorizontal(lipgloss.Top,
		label,
		" ",
		m.command.View(),
	)
	return inputBoxStyle.Width(boxWidth).Render(line)
}

func (m *model) sidebarView(width, height int) string {
	if m.sidebar == sidebarHistory {
		return m.historyView(width, height)
	}
	return m.runSidebarView(width, height)
}

func (m *model) runSidebarView(width, height int) string {
	title := "Run"
	if record := m.selectedTaskRecord(); record != nil {
		title = fmt.Sprintf("Run #%d", record.ID)
	}
	if height <= 0 {
		return ""
	}
	lines := []string{sidebarTitleStyle.Render(truncate(title, width))}
	listHeight := max(0, height-1)
	entries := m.selectedChat()
	if len(entries) == 0 {
		message := "Waiting for agent activity..."
		if m.loop && !m.running {
			message = "Enter a task below."
		}
		lines = append(lines, mutedStyle.Render(truncate(message, width)))
		for len(lines) < height {
			lines = append(lines, "")
		}
		return strings.Join(lines, "\n")
	}
	end := min(len(entries), m.chatOffset+listHeight)
	for i := m.chatOffset; i < end; i++ {
		prefix := "  "
		style := sidebarItemStyle
		if i == m.selected {
			prefix = "> "
			style = selectedStyle
		}
		lines = append(lines, style.Render(truncate(prefix+chatLine(entries[i]), width)))
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

func (m *model) historyView(width, height int) string {
	if height <= 0 {
		return ""
	}
	lines := []string{sidebarTitleStyle.Render(truncate(fmt.Sprintf("History (%d tasks)", len(m.tasks)), width))}
	listHeight := max(0, height-1)
	if len(m.tasks) == 0 {
		lines = append(lines, mutedStyle.Render(truncate("No task history yet. Enter a task below.", width)))
		for len(lines) < height {
			lines = append(lines, "")
		}
		return strings.Join(lines, "\n")
	}
	end := min(len(m.tasks), m.historyOffset+listHeight)
	for i := m.historyOffset; i < end; i++ {
		prefix := "  "
		style := sidebarItemStyle
		if i == m.selectedTask {
			prefix = "> "
			style = selectedStyle
		}
		lines = append(lines, style.Render(truncate(prefix+taskHistoryLine(m.tasks[i]), width)))
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
	shortcuts := m.help.ShortHelpView(m.shortHelp())
	status := valueOrDefault(m.status, m.statusHint())
	left := fmt.Sprintf(" %s: %s", m.phaseLabel(), status)
	shortcutWidth := lipgloss.Width(shortcuts)
	if shortcutWidth+4 >= m.width {
		return footerStyle.Width(m.width).Render(truncate(left, max(1, m.width)))
	}
	left = truncate(left, max(1, m.width-shortcutWidth-4))
	return footerStyle.Width(m.width).Render(fillLine(left, shortcuts+" ", m.width))
}

func (m *model) helpOverlayView() string {
	previousMode := m.beforeHelp
	if previousMode == modeHelp {
		previousMode = modeNormal
	}
	currentMode := m.mode
	m.mode = previousMode
	base := m.appView()
	m.mode = currentMode
	return overlayRows(base, m.helpDialog(), m.width, m.height)
}

func (m *model) helpDialog() string {
	if m.width <= 0 || m.height <= 0 {
		return ""
	}
	frameW, frameH := helpDialogStyle.GetFrameSize()

	outerWidth := min(88, max(1, m.width-4))
	if m.width >= 36 {
		outerWidth = max(32, outerWidth)
	}
	outerWidth = min(outerWidth, m.width)

	outerHeight := min(22, max(1, m.height-2))
	if m.height >= 10 {
		outerHeight = max(8, outerHeight)
	}
	outerHeight = min(outerHeight, m.height)

	if outerWidth <= frameW || outerHeight <= frameH {
		return fitHeight(wrapText("Help\nesc/q/? close", max(1, m.width)), max(1, m.height))
	}

	innerWidth := max(1, outerWidth-frameW)
	innerHeight := max(1, outerHeight-frameH)
	m.help.Width = innerWidth
	content := fitHeight(m.helpContent(innerWidth), innerHeight)
	return helpDialogStyle.
		Width(innerWidth).
		Height(innerHeight).
		Render(content)
}

func (m *model) helpContent(width int) string {
	sections := []string{
		"Help",
		"Close: esc/q/?",
		"",
		wrapText("The bottom composer is the stable place for tasks and slash commands. During a run it stays visible as a status area while the timeline and inspector update above it.", width),
		"",
		"Composer",
		wrapText("  i starts task input. Enter submits. Slash commands work from the composer, for example /history or /diff.", width),
		"",
		"Slash commands",
		wrapText("  /help opens this guide. /history shows prior runs. /clear resets the visible session. /diff, /tests, /steps, /trace, and /open-trace switch inspector views. /quit exits or cancels.", width),
		"",
		"Navigation",
		m.help.FullHelpView(m.fullHelp()),
		"",
		"Close",
		"  esc, q, or ? closes help and returns to the current TUI.",
	}
	return strings.Join(sections, "\n")
}

func overlayRows(base, popup string, width, height int) string {
	if width <= 0 || height <= 0 {
		return popup
	}
	lines := strings.Split(strings.TrimRight(base, "\n"), "\n")
	for len(lines) < height {
		lines = append(lines, "")
	}
	if len(lines) > height {
		lines = lines[:height]
	}
	popupLines := strings.Split(strings.TrimRight(popup, "\n"), "\n")
	if len(popupLines) > height {
		popupLines = popupLines[:height]
	}
	startY := max(0, (height-len(popupLines))/2)
	for i, line := range popupLines {
		row := startY + i
		if row >= len(lines) {
			break
		}
		lines[row] = lipgloss.PlaceHorizontal(width, lipgloss.Center, line)
	}
	return strings.Join(lines, "\n")
}

func (m *model) shortHelp() []key.Binding {
	switch m.mode {
	case modeApproval:
		return []key.Binding{keyAllow, keyDeny, keyRemember, keyHelp}
	case modeTask:
		return []key.Binding{keyEnter, keySlashHelp, keySlashHistory, keySlashClear, keyEsc}
	case modeCommand, modeSearch:
		return []key.Binding{keyEnter, keyEsc}
	default:
		return []key.Binding{keyMove, keyOpen, keyTaskInput, keyHistory, keyCommand, keySearch, keyHelp, keyQuit}
	}
}

func (m *model) fullHelp() [][]key.Binding {
	return [][]key.Binding{
		{keyMove, keyLeftRight, keyTop, keyBottom, keyCenter},
		{keyScrollHalf, keyScrollPage, keyTab, keyOpen},
		{keySearch, keyNext, keyPrev, keyCommand, keyHelp},
		{keyDiff, keyTests, keySteps, keyTimeline, keyHistory, keyTrace, keyOpenTrace},
		{keyTaskInput, keySlashHelp, keySlashHistory, keySlashClear, keySlashQuit},
		{keyAllow, keyDeny, keyRemember, keyExpandApproval},
		{keyQuit, keyCancel},
	}
}

func summarizeEvent(event core.Event) string {
	switch event.Type {
	case "user_task":
		return "Task started: " + shortString(event.Data["task"], 48)
	case "model_request":
		return "Waiting for model"
	case "model_response":
		return "Planning next action"
	case "tool_call":
		toolName := strings.TrimSpace(fmt.Sprint(event.Data["tool"]))
		if toolName == "" {
			toolName = "command"
		}
		return "Running " + toolName
	case "tool_result":
		toolName := strings.TrimSpace(fmt.Sprint(event.Data["tool"]))
		if toolName == "" {
			toolName = "Command"
		}
		if code := intValue(event.Data["code"]); code != 0 {
			return fmt.Sprintf("%s failed: exit %d", toolName, code)
		}
		return toolName + " finished"
	case "tool_denied":
		return fmt.Sprintf("%v denied", event.Data["tool"])
	case "error":
		return "Error: " + shortString(event.Data["error"], 48)
	case "final":
		return "Finished: " + strings.TrimSpace(fmt.Sprint(event.Data["status"]))
	default:
		if event.Type == "" {
			return "Working"
		}
		return event.Type
	}
}

func eventDetail(event core.Event) string {
	return eventDetailWidth(event, 0)
}

func eventDetailWidth(event core.Event, width int) string {
	var b strings.Builder
	b.WriteString("Event\n")
	writeField(&b, "Type", event.Type, width)
	if !event.Time.IsZero() {
		writeField(&b, "Time", event.Time.Format(time.RFC3339), width)
	}

	switch event.Type {
	case "user_task":
		writeSection(&b, "Task")
		writeField(&b, "Task", event.Data["task"], width)
		writeField(&b, "Repository", event.Data["repo"], width)
	case "model_request":
		writeSection(&b, "Request")
		writeField(&b, "Step", event.Data["step"], width)
		writeField(&b, "Messages", event.Data["messages"], width)
	case "model_response":
		writeSection(&b, "Response")
		writeField(&b, "Step", event.Data["step"], width)
		writeField(&b, "Content", event.Data["content"], width)
		if usage, ok := event.Data["usage"]; ok {
			writeSection(&b, "Usage")
			writeValueTree(&b, "", usage, 0, width)
		}
	case "tool_call":
		writeSection(&b, "Tool Call")
		writeField(&b, "Tool", event.Data["tool"], width)
		if args, ok := event.Data["args"]; ok {
			writeSection(&b, "Arguments")
			writeValueTree(&b, "", args, 0, width)
		}
	case "tool_result":
		writeSection(&b, "Tool Result")
		writeField(&b, "Tool", event.Data["tool"], width)
		writeField(&b, "Code", event.Data["code"], width)
		writeField(&b, "Timed Out", event.Data["timed_out"], width)
		writeField(&b, "Output", event.Data["output"], width)
	case "tool_denied":
		writeSection(&b, "Denied Tool")
		writeField(&b, "Tool", event.Data["tool"], width)
		writeField(&b, "Reason", event.Data["reason"], width)
	case "error":
		writeSection(&b, "Error")
		writeField(&b, "Error", event.Data["error"], width)
	case "final":
		writeSection(&b, "Final Result")
		writeField(&b, "Status", event.Data["status"], width)
		writeField(&b, "Steps", event.Data["steps"], width)
		writeField(&b, "Submission", event.Data["submission"], width)
	default:
		if len(event.Data) > 0 {
			writeSection(&b, "Data")
			writeValueTree(&b, "", event.Data, 0, width)
		}
	}

	return b.String()
}

func chatLine(entry chatEntry) string {
	role := chatRoleLabel(entry)
	body := shortString(entry.Body, 56)
	if body == "" {
		body = "(empty)"
	}
	if entry.Time.IsZero() {
		return fmt.Sprintf("%-8s %s", role, body)
	}
	return fmt.Sprintf("%s %-8s %s", entry.Time.Format("15:04:05"), role, body)
}

func chatRoleLabel(entry chatEntry) string {
	switch strings.ToLower(strings.TrimSpace(entry.Role)) {
	case "user":
		return "User"
	case "assistant":
		return "Agent"
	case "tool":
		return "Tool"
	case "result":
		return "Result"
	case "denied":
		return "Attention"
	case "attention":
		return "Attention"
	case "error":
		return "Attention"
	case "summary":
		return "Outcome"
	default:
		return valueOrDefault(entry.Title, valueOrDefault(entry.Role, "Message"))
	}
}

func chatDetailWidth(entry chatEntry, width int) string {
	title := valueOrDefault(entry.Title, valueOrDefault(entry.Role, "Message"))
	var b strings.Builder
	b.WriteString(title)
	b.WriteString("\n\n")
	if !entry.Time.IsZero() {
		writeField(&b, "Time", entry.Time.Format(time.RFC3339), width)
		b.WriteByte('\n')
	}
	body := strings.TrimSpace(entry.Body)
	if body == "" {
		body = "No content."
	}
	b.WriteString(wrapText(body, width))
	b.WriteByte('\n')
	return b.String()
}

func taskSummary(record taskRecord) string {
	status := valueOrDefault(record.Status, "pending")
	conclusion := taskConclusion(record)
	errText := strings.TrimSpace(lastErrorText(record))

	switch {
	case status == "running":
		return "Running.\n\nSummary:\n  Waiting for result.\n\nEvidence:\n  Diff: pending\n  Tests: unknown"
	case errText != "":
		return fmt.Sprintf("%s.\n\nNeed attention:\n  %s", titleStatus(status), errText)
	case conclusion != "":
		return fmt.Sprintf("%s.\n\nSummary:\n  %s\n\nEvidence:\n%s", titleStatus(status), conclusion, indentText(taskEvidence(record), 2))
	case record.Result.Diff != "":
		return fmt.Sprintf("%s.\n\nSummary:\n  No final summary recorded.\n\nEvidence:\n%s", titleStatus(status), indentText(taskEvidence(record), 2))
	default:
		return fmt.Sprintf("%s.\n\nSummary:\n  No final summary recorded.\n\nEvidence:\n%s", titleStatus(status), indentText(taskEvidence(record), 2))
	}
}

func titleStatus(status string) string {
	status = strings.TrimSpace(status)
	if status == "" {
		return "Done"
	}
	return strings.ToUpper(status[:1]) + status[1:]
}

func taskEvidence(record taskRecord) string {
	snapshot := BuildRunSnapshot(record, record.Result.TrajectoryPath)
	diff := "not available"
	if snapshot.FinalReview.ChangedFiles > 0 || record.Result.Diff != "" {
		diff = fmt.Sprintf("%d files changed", snapshot.FinalReview.ChangedFiles)
		if snapshot.FinalReview.ChangedFiles == 0 {
			diff = "available"
		}
	}
	tests := "not recorded"
	if snapshot.FinalReview.TestsRun > 0 {
		tests = fmt.Sprintf("%d/%d passed", snapshot.FinalReview.TestsPassed, snapshot.FinalReview.TestsRun)
	}
	return fmt.Sprintf("Diff: %s\nTests: %s", diff, tests)
}

func taskConclusion(record taskRecord) string {
	if submission := cleanSubmission(record.Result.Submission); submission != "" {
		return submission
	}
	if submission := cleanSubmission(lastEventData(record.Events, "final", "submission")); submission != "" {
		return submission
	}
	for i := len(record.Events) - 1; i >= 0; i-- {
		event := record.Events[i]
		if event.Type != "tool_call" || fmt.Sprint(event.Data["tool"]) != "submit" {
			continue
		}
		if args, ok := normalizeValue(event.Data["args"]).(map[string]any); ok {
			if submission := cleanSubmission(args["submission"]); submission != "" {
				return submission
			}
		}
	}
	return lastAssistantConclusion(record)
}

func cleanSubmission(value any) string {
	if value == nil {
		return ""
	}
	submission := strings.TrimSpace(fmt.Sprint(value))
	if submission == "" || submission == "<nil>" || strings.EqualFold(submission, "submitted") {
		return ""
	}
	return submission
}

func lastAssistantConclusion(record taskRecord) string {
	for i := len(record.Events) - 1; i >= 0; i-- {
		event := record.Events[i]
		if event.Type != "model_response" {
			continue
		}
		content := stripFencedBlocks(strings.TrimSpace(fmt.Sprint(event.Data["content"])))
		if content != "" {
			return content
		}
	}
	return ""
}

func stripFencedBlocks(value string) string {
	return strings.TrimSpace(fencedBlockPattern.ReplaceAllString(value, ""))
}

func (m *model) upsertSummaryEntry(record *taskRecord, at time.Time) {
	if record == nil {
		return
	}
	if at.IsZero() {
		at = time.Now()
	}
	entry := chatEntry{
		Role:  "summary",
		Title: "Summary",
		Body:  valueOrDefault(record.Summary, taskSummary(*record)),
		Time:  at,
	}
	for i := len(record.Chat) - 1; i >= 0; i-- {
		if record.Chat[i].Role == "summary" {
			record.Chat[i] = entry
			return
		}
	}
	record.Chat = append(record.Chat, entry)
}

func lastErrorText(record taskRecord) string {
	if record.RunErr != nil {
		return record.RunErr.Error()
	}
	value := lastEventData(record.Events, "error", "error")
	if value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func lastEventData(events []core.Event, eventType string, key string) any {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type == eventType {
			if events[i].Data == nil {
				return nil
			}
			return events[i].Data[key]
		}
	}
	return nil
}

func taskHistoryLine(record taskRecord) string {
	status := strings.TrimSpace(record.Status)
	if status == "" {
		status = "pending"
	}
	text := shortString(record.Task.Text, 48)
	if text == "" {
		text = "(empty task)"
	}
	return fmt.Sprintf("#%d %-10s %s", record.ID, status, text)
}

func taskDetailWidth(record taskRecord, width int) string {
	snapshot := BuildRunSnapshot(record, record.Result.TrajectoryPath)
	var b strings.Builder
	fmt.Fprintf(&b, "Task #%d\n", record.ID)
	writeField(&b, "Status", valueOrDefault(record.Status, "pending"), width)
	writeField(&b, "Repository", record.Task.Repo, width)
	writeField(&b, "Task", record.Task.Text, width)
	if !record.StartedAt.IsZero() {
		writeField(&b, "Started", record.StartedAt.Format(time.RFC3339), width)
	}
	if !record.FinishedAt.IsZero() {
		writeField(&b, "Finished", record.FinishedAt.Format(time.RFC3339), width)
	}
	if duration := taskDuration(record); duration != "" {
		writeField(&b, "Duration", duration, width)
	}
	if summary := strings.TrimSpace(taskConclusion(record)); summary != "" {
		writeSection(&b, "Outcome")
		b.WriteString(wrapText(summary, width))
		b.WriteByte('\n')
	}

	writeSection(&b, "Evidence")
	diff := "not available"
	if snapshot.FinalReview.ChangedFiles > 0 || record.Result.Diff != "" {
		diff = summarizeDiff(record.Result.Diff)
	}
	writeField(&b, "Diff", diff, width)
	writeField(&b, "Validation", validationSummary(snapshot), width)
	trace := "not available"
	if snapshot.FinalReview.Trajectory != "" {
		trace = "available via /trace"
	}
	writeField(&b, "Trace", trace, width)

	writeSection(&b, "Need attention")
	if errText := strings.TrimSpace(lastErrorText(record)); errText != "" {
		b.WriteString(wrapText(errText, width))
		b.WriteByte('\n')
	} else {
		b.WriteString("none\n")
	}
	return b.String()
}

func stepLine(step StepCard) string {
	command := shortString(step.Command, 36)
	if command == "" {
		command = shortString(step.Tool, 36)
	}
	duration := ""
	if step.Duration > 0 {
		duration = " " + step.Duration.String()
	}
	return fmt.Sprintf("Step %-2d %-9s %-10s %-8s%s", step.Index, step.Phase, command, step.Outcome, duration)
}

func stepDetailWidth(step StepCard, width int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Step %d\n", step.Index)
	writeField(&b, "Phase", step.Phase, width)
	writeField(&b, "Tool", step.Tool, width)
	writeField(&b, "Outcome", step.Outcome, width)
	if step.Command != "" {
		writeField(&b, "Command", step.Command, width)
	}
	if !step.Started.IsZero() {
		writeField(&b, "Started", step.Started.Format(time.RFC3339), width)
	}
	if step.Duration > 0 {
		writeField(&b, "Duration", step.Duration.String(), width)
	}
	if len(step.EventIDs) > 0 {
		writeField(&b, "Events", formatEventIDs(step.EventIDs), width)
	}
	if step.Why != "" {
		writeSection(&b, "Why")
		b.WriteString(wrapText(step.Why, width))
		b.WriteByte('\n')
	}
	if step.Action != "" {
		writeSection(&b, "Action")
		b.WriteString(wrapText(step.Action, width))
		b.WriteByte('\n')
	}
	if step.Output != "" {
		writeSection(&b, "Observation")
		b.WriteString(wrapText(step.Output, width))
		b.WriteByte('\n')
	}
	return b.String()
}

func stepsViewWidth(snapshot RunSnapshot, width int) string {
	var b strings.Builder
	b.WriteString("Steps\n")
	if len(snapshot.Steps) == 0 {
		b.WriteString("\nNo steps recorded.\n")
		return b.String()
	}
	for _, step := range snapshot.Steps {
		writeField(&b, fmt.Sprintf("#%d", step.Index), stepLine(step), width)
	}
	return b.String()
}

func finalReviewWidth(record taskRecord, snapshot RunSnapshot, width int) string {
	if body := strings.TrimSpace(record.Narrative.Body); body != "" {
		return "Review\n\n" + wrapText(body, width) + "\n"
	}
	if record.Narrative.Status == "pending" {
		return fallbackReviewWidth(snapshot, width) + "\nGenerating review...\n"
	}
	return fallbackReviewWidth(snapshot, width)
}

func fallbackReviewWidth(snapshot RunSnapshot, width int) string {
	var b strings.Builder
	b.WriteString("Review\n")
	writeField(&b, "Status", snapshot.FinalReview.Status, width)
	writeField(&b, "Goal", snapshot.Task.Text, width)
	writeField(&b, "Repository", snapshot.Task.Repo, width)
	if snapshot.FinalReview.Submission != "" {
		writeSection(&b, "Outcome")
		b.WriteString(wrapText(snapshot.FinalReview.Submission, width))
		b.WriteByte('\n')
	}
	writeSection(&b, "Evidence")
	diff := "not available"
	if snapshot.FinalReview.ChangedFiles > 0 {
		diff = fmt.Sprintf("%d files changed", snapshot.FinalReview.ChangedFiles)
	}
	writeField(&b, "Diff", diff, width)
	writeField(&b, "Validation", validationSummary(snapshot), width)
	trace := "not available"
	if snapshot.FinalReview.Trajectory != "" {
		trace = "available via /trace"
	}
	writeField(&b, "Trace", trace, width)
	writeSection(&b, "Need attention")
	attention := "none"
	for _, artifact := range snapshot.Artifacts {
		if artifact.Kind != "error" || strings.TrimSpace(artifact.Body) == "" {
			continue
		}
		attention = artifact.Body
		break
	}
	b.WriteString(wrapText(attention, width))
	b.WriteByte('\n')
	writeSection(&b, "Actions")
	for _, action := range []string{
		"d inspect diff",
		"v inspect validation",
		"s inspect steps",
		"x inspect trace",
		"i new task",
		"q quit",
	} {
		b.WriteString("  ")
		b.WriteString(action)
		b.WriteByte('\n')
	}
	return b.String()
}

func validationSummary(snapshot RunSnapshot) string {
	if snapshot.FinalReview.TestsRun == 0 {
		return "not recorded"
	}
	if snapshot.FinalReview.TestsPassed == snapshot.FinalReview.TestsRun {
		return fmt.Sprintf("passed (%d/%d)", snapshot.FinalReview.TestsPassed, snapshot.FinalReview.TestsRun)
	}
	return fmt.Sprintf("failed (%d/%d passed)", snapshot.FinalReview.TestsPassed, snapshot.FinalReview.TestsRun)
}

func validationViewWidth(snapshot RunSnapshot, width int) string {
	var b strings.Builder
	b.WriteString("Validation\n")
	found := false
	for _, step := range snapshot.Steps {
		if step.Phase != "validate" && step.Tool != "run_tests" {
			continue
		}
		found = true
		writeSection(&b, valueOrDefault(step.Command, step.Tool))
		writeField(&b, "Status", step.Outcome, width)
		if step.Duration > 0 {
			writeField(&b, "Duration", step.Duration.String(), width)
		}
		if step.Output != "" {
			writeField(&b, "Evidence", step.Output, width)
		}
	}
	if !found {
		b.WriteString("\nNo validation command recorded.\n")
	}
	return b.String()
}

func timelineViewWidth(record taskRecord, snapshot RunSnapshot, width int) string {
	items := BuildTimeline(record, snapshot)
	if len(items) == 0 {
		return "Timeline\n\nWaiting for agent activity.\n"
	}
	var b strings.Builder
	b.WriteString("Timeline\n")
	for _, item := range items {
		writeTimelineItem(&b, item, width)
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

func writeTimelineItem(b *strings.Builder, item TimelineItem, width int) {
	icon := timelineIcon(item)
	title := valueOrDefault(item.Title, string(item.Kind))
	summary := strings.TrimSpace(item.Summary)
	if summary == "" {
		summary = strings.TrimSpace(item.Detail)
	}
	status := string(item.Status)
	if status != "" && status != string(itemOK) {
		status = "  " + status
	} else {
		status = ""
	}
	duration := ""
	if item.Duration > 0 {
		duration = "  " + item.Duration.String()
	}
	line := strings.TrimSpace(fmt.Sprintf("%s %-7s %s%s%s", icon, title, summary, status, duration))
	b.WriteString(wrapText(line, width))
	b.WriteByte('\n')
	if len(item.Artifacts) > 0 {
		b.WriteString(mutedStyle.Render(wrapText("  "+formatArtifactRefs(item.Artifacts), width)))
		b.WriteByte('\n')
	}
}

func timelineIcon(item TimelineItem) string {
	switch item.Status {
	case itemRunning:
		return ">"
	case itemFailed:
		return "!"
	case itemWaiting:
		return "?"
	case itemSkipped:
		return "-"
	}
	switch item.Kind {
	case itemUser:
		return "u"
	case itemAgent:
		return "a"
	case itemFile:
		return "M"
	case itemTest:
		return "T"
	case itemFinal:
		return "*"
	default:
		return ">"
	}
}

func formatArtifactRefs(refs []ArtifactRef) string {
	parts := make([]string, 0, len(refs))
	for _, ref := range refs {
		label := strings.TrimSpace(ref.Kind)
		if ref.Title != "" {
			label += ":" + strings.TrimSpace(ref.Title)
		}
		parts = append(parts, label)
	}
	return strings.Join(parts, "  ")
}

func planInspectorWidth(record taskRecord, snapshot RunSnapshot, width int) string {
	var b strings.Builder
	b.WriteString("Plan\n")
	writeField(&b, "Goal", record.Task.Text, width)
	writeField(&b, "Status", valueOrDefault(record.Status, "pending"), width)
	if duration := taskDuration(record); duration != "" {
		writeField(&b, "Elapsed", duration, width)
	}
	writeSection(&b, "Files")
	diff := "none"
	if snapshot.FinalReview.ChangedFiles > 0 {
		diff = fmt.Sprintf("%d changed", snapshot.FinalReview.ChangedFiles)
	} else if record.Result.Diff != "" {
		diff = "available"
	}
	writeField(&b, "Diff", diff, width)
	writeField(&b, "Tests", validationSummary(snapshot), width)
	trace := "not available"
	if snapshot.FinalReview.Trajectory != "" {
		trace = "available"
	}
	writeField(&b, "Trace", trace, width)
	if errText := strings.TrimSpace(lastErrorText(record)); errText != "" {
		writeSection(&b, "Risk")
		b.WriteString(wrapText(errText, width))
		b.WriteByte('\n')
	}
	return b.String()
}

func formatEventIDs(ids []int) string {
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, fmt.Sprintf("#%d", id+1))
	}
	return strings.Join(parts, ", ")
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
		return "empty"
	}
	var b strings.Builder
	writeValueTree(&b, "", args, 0, 0)
	return strings.TrimRight(b.String(), "\n")
}

func formatArgsMap(value any) string {
	normalized := normalizeValue(value)
	args, ok := normalized.(map[string]any)
	if !ok {
		return strings.TrimSpace(formatScalar(normalized))
	}
	return formatArgs(args)
}

func writeSection(b *strings.Builder, title string) {
	if b.Len() > 0 && !strings.HasSuffix(b.String(), "\n\n") {
		b.WriteByte('\n')
	}
	b.WriteString(title)
	b.WriteByte('\n')
}

func writeField(b *strings.Builder, key string, value any, width int) {
	text := strings.TrimSpace(formatScalar(normalizeValue(value)))
	if text == "" {
		return
	}
	if strings.Contains(text, "\n") || (width > 0 && len([]rune(key+": "+text)) > width) {
		fmt.Fprintf(b, "%s:\n", key)
		b.WriteString(indentText(wrapText(text, remainingWidth(width, 2)), 2))
		b.WriteByte('\n')
		return
	}
	fmt.Fprintf(b, "%s: %s\n", key, text)
}

func writeValueTree(b *strings.Builder, key string, value any, indent int, width int) {
	value = normalizeValue(value)
	prefix := strings.Repeat("  ", indent)
	switch typed := value.(type) {
	case map[string]any:
		if key != "" {
			if len(typed) == 0 {
				fmt.Fprintf(b, "%s%s: empty\n", prefix, key)
				return
			}
			fmt.Fprintf(b, "%s%s:\n", prefix, key)
			indent++
			prefix = strings.Repeat("  ", indent)
		}
		keys := make([]string, 0, len(typed))
		for childKey := range typed {
			keys = append(keys, childKey)
		}
		sort.Strings(keys)
		for _, childKey := range keys {
			writeValueTree(b, childKey, typed[childKey], indent, width)
		}
	case []any:
		if key != "" {
			if len(typed) == 0 {
				fmt.Fprintf(b, "%s%s: empty list\n", prefix, key)
				return
			}
			fmt.Fprintf(b, "%s%s:\n", prefix, key)
		}
		for _, item := range typed {
			writeListItem(b, item, indent+1, width)
		}
	default:
		text := strings.TrimSpace(formatScalar(typed))
		if text == "" {
			return
		}
		if key == "" {
			b.WriteString(indentText(wrapText(text, remainingWidth(width, len(prefix))), indent*2))
			b.WriteByte('\n')
			return
		}
		line := fmt.Sprintf("%s%s: %s", prefix, key, text)
		if width > 0 && len([]rune(line)) > width {
			fmt.Fprintf(b, "%s%s:\n", prefix, key)
			b.WriteString(indentText(wrapText(text, remainingWidth(width, len(prefix)+2)), indent*2+2))
			b.WriteByte('\n')
			return
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
}

func writeListItem(b *strings.Builder, value any, indent int, width int) {
	value = normalizeValue(value)
	prefix := strings.Repeat("  ", indent)
	switch typed := value.(type) {
	case map[string]any:
		fmt.Fprintf(b, "%s-\n", prefix)
		writeValueTree(b, "", typed, indent+1, width)
	case []any:
		fmt.Fprintf(b, "%s-\n", prefix)
		writeValueTree(b, "", typed, indent+1, width)
	default:
		text := strings.TrimSpace(formatScalar(typed))
		if text == "" {
			return
		}
		line := fmt.Sprintf("%s- %s", prefix, text)
		if width > 0 && len([]rune(line)) > width {
			fmt.Fprintf(b, "%s-\n", prefix)
			b.WriteString(indentText(wrapText(text, remainingWidth(width, len(prefix)+2)), indent*2+2))
			b.WriteByte('\n')
			return
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
}

func normalizeValue(value any) any {
	switch typed := value.(type) {
	case nil, string, bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
		return typed
	case map[string]any, []any:
		return typed
	default:
		data, err := json.Marshal(typed)
		if err != nil {
			return fmt.Sprint(typed)
		}
		var out any
		if err := json.Unmarshal(data, &out); err != nil {
			return fmt.Sprint(typed)
		}
		return out
	}
}

func formatScalar(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case bool:
		return fmt.Sprint(typed)
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return fmt.Sprint(typed)
	case float32:
		return formatFloat(float64(typed))
	case float64:
		return formatFloat(typed)
	default:
		return fmt.Sprint(typed)
	}
}

func formatFloat(value float64) string {
	if value == float64(int64(value)) {
		return fmt.Sprintf("%.0f", value)
	}
	return fmt.Sprintf("%g", value)
}

func indentText(text string, spaces int) string {
	if text == "" {
		return ""
	}
	prefix := strings.Repeat(" ", spaces)
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		if line != "" {
			lines[i] = prefix + line
		}
	}
	return strings.Join(lines, "\n")
}

func remainingWidth(width int, used int) int {
	if width <= 0 {
		return 0
	}
	return max(1, width-used)
}

func valueOrDefault(value string, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

func taskDuration(record taskRecord) string {
	if record.StartedAt.IsZero() {
		return ""
	}
	end := record.FinishedAt
	if end.IsZero() {
		end = time.Now()
	}
	if end.Before(record.StartedAt) {
		return ""
	}
	return end.Sub(record.StartedAt).Round(time.Second).String()
}

func summarizeDiff(diff string) string {
	lines := 0
	for _, line := range strings.Split(diff, "\n") {
		if strings.TrimSpace(line) != "" {
			lines++
		}
	}
	if lines == 0 {
		return "empty"
	}
	return fmt.Sprintf("%d non-empty lines; press d for full diff", lines)
}

func intValue(value any) int {
	switch typed := normalizeValue(value).(type) {
	case int:
		return typed
	case int8:
		return int(typed)
	case int16:
		return int(typed)
	case int32:
		return int(typed)
	case int64:
		return int(typed)
	case uint:
		return int(typed)
	case uint8:
		return int(typed)
	case uint16:
		return int(typed)
	case uint32:
		return int(typed)
	case uint64:
		return int(typed)
	case float32:
		return int(typed)
	case float64:
		return int(typed)
	case string:
		i, _ := strconv.Atoi(strings.TrimSpace(typed))
		return i
	default:
		return 0
	}
}

func shortString(value any, limit int) string {
	s := strings.TrimSpace(fmt.Sprint(value))
	s = strings.ReplaceAll(s, "\n", " ")
	return truncate(s, limit)
}

func wrapText(s string, width int) string {
	if width <= 0 {
		return s
	}
	var out []string
	for _, line := range strings.Split(s, "\n") {
		out = append(out, wrapLine(line, width)...)
	}
	return strings.Join(out, "\n")
}

func wrapLine(line string, width int) []string {
	if width <= 0 || len([]rune(line)) <= width {
		return []string{line}
	}
	runes := []rune(line)
	lines := make([]string, 0, len(runes)/width+1)
	for len(runes) > width {
		breakAt := width
		for i := width; i > 0; i-- {
			if runes[i-1] == ' ' || runes[i-1] == '\t' {
				breakAt = i
				break
			}
		}
		part := strings.TrimRight(string(runes[:breakAt]), " \t")
		if part == "" {
			part = string(runes[:width])
			breakAt = width
		}
		lines = append(lines, part)
		runes = []rune(strings.TrimLeft(string(runes[breakAt:]), " \t"))
	}
	lines = append(lines, string(runes))
	return lines
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

func fillLine(left, right string, width int) string {
	if width <= 0 {
		return ""
	}
	leftRunes := []rune(left)
	rightRunes := []rune(right)
	if len(leftRunes)+len(rightRunes) >= width {
		return truncate(left+right, width)
	}
	return left + strings.Repeat(" ", width-len(leftRunes)-len(rightRunes)) + right
}

func fitHeight(content string, height int) string {
	if height <= 0 {
		return ""
	}
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	if len(lines) > height {
		if height == 1 {
			return truncate(lines[0], max(1, len([]rune(lines[0]))))
		}
		lines = append(lines[:height-1], "...")
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

func fitHeightHeadTail(content string, height int) string {
	if height <= 0 {
		return ""
	}
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	if len(lines) > height {
		if height == 1 {
			return truncate(lines[0], max(1, len([]rune(lines[0]))))
		}
		if height == 2 {
			lines = []string{lines[0], "..."}
		} else {
			tail := append([]string(nil), lines[len(lines)-(height-2):]...)
			lines = append([]string{lines[0], "..."}, tail...)
		}
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
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

var fencedBlockPattern = regexp.MustCompile("(?s)```(?:swe_shell|bash|sh|shell)?\\s*\\n.*?\\n```")

var (
	colorAccent = lipgloss.Color("39")
	colorBorder = lipgloss.Color("238")
	colorMuted  = lipgloss.Color("244")
	colorPanel  = lipgloss.Color("236")

	headerStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("15")).
			Background(lipgloss.Color("237")).
			Bold(true)
	artifactBarStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("250")).
				Background(lipgloss.Color("235"))
	panelStyle = lipgloss.NewStyle().
			Border(lipgloss.NormalBorder()).
			BorderForeground(colorPanel).
			Padding(0, 1)
	footerStyle = lipgloss.NewStyle().
			Foreground(colorMuted)
	statusStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("250")).
			Background(lipgloss.Color("235"))
	mutedStyle = lipgloss.NewStyle().
			Foreground(colorMuted)
	sidebarTitleStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("250")).
				Bold(true)
	sidebarItemStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("252"))
	selectedStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("15")).
			Background(lipgloss.Color("24")).
			Bold(true)
	approvalStyle = lipgloss.NewStyle().
			Border(lipgloss.NormalBorder()).
			BorderForeground(lipgloss.Color("214")).
			Padding(0, 1)
	helpDialogStyle = lipgloss.NewStyle().
			Border(lipgloss.NormalBorder()).
			BorderForeground(colorAccent).
			Padding(0, 2)
	inputBoxStyle = lipgloss.NewStyle().
			Border(lipgloss.NormalBorder()).
			BorderForeground(colorAccent).
			Padding(0, 1)
	inputLabelStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("15")).
			Background(lipgloss.Color("24")).
			Bold(true)
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
	keyTests          = key.NewBinding(key.WithKeys("v", ":tests"), key.WithHelp("v", "validation"))
	keySteps          = key.NewBinding(key.WithKeys("s", ":steps"), key.WithHelp("s", "steps"))
	keyTimeline       = key.NewBinding(key.WithKeys("t", ":overview"), key.WithHelp("t", "overview"))
	keyHistory        = key.NewBinding(key.WithKeys(":history"), key.WithHelp(":history", "task history"))
	keyTrace          = key.NewBinding(key.WithKeys("x", ":trace"), key.WithHelp("x", "trace path"))
	keyOpenTrace      = key.NewBinding(key.WithKeys(":open-trace"), key.WithHelp(":open-trace", "$EDITOR trace"))
	keySlashHelp      = key.NewBinding(key.WithKeys("/help"), key.WithHelp("/help", "help"))
	keySlashHistory   = key.NewBinding(key.WithKeys("/history"), key.WithHelp("/history", "history"))
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
