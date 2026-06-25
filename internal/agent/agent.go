package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/local/swe-agent/internal/core"
	"github.com/local/swe-agent/internal/problemtrace"
)

type ActionParser interface {
	Parse(resp core.ModelResponse) ([]core.ToolCall, error)
}

type Workspace interface {
	Root() string
	Diff(ctx context.Context) (string, error)
}

type Result struct {
	Status         string     `json:"status"`
	Submission     string     `json:"submission,omitempty"`
	Diff           string     `json:"diff,omitempty"`
	Steps          int        `json:"steps"`
	Usage          core.Usage `json:"usage"`
	TrajectoryPath string     `json:"trajectory_path"`
}

type State struct {
	Task       core.Task
	Messages   []core.Message
	Steps      int
	Usage      core.Usage
	Submitted  bool
	Submission string
	Status     string
	startedAt  time.Time
}

type Agent struct {
	Config     Config
	Model      core.Model
	Runtime    core.Runtime
	Tools      core.ToolRegistry
	Parser     ActionParser
	Policy     core.Policy
	Trajectory core.TrajectoryStore
	Workspace  Workspace
	EventSink  EventSink
	Trace      *problemtrace.Manager
}

type EventSink interface {
	EmitEvent(ctx context.Context, event core.Event) error
}

func (a *Agent) Run(ctx context.Context, task core.Task) (Result, error) {
	if err := a.validate(); err != nil {
		return Result{}, err
	}
	if err := a.Policy.ValidateUserInput(ctx, task.Text); err != nil {
		return Result{}, err
	}

	state := &State{
		Task:      task,
		Status:    "running",
		startedAt: time.Now(),
		Messages: []core.Message{
			{Role: core.RoleSystem, Content: a.Config.Agent.SystemPrompt},
			{Role: core.RoleUser, Content: a.initialUserMessage(task)},
		},
	}
	a.appendEvent(ctx, core.Event{Type: "user_task", Data: map[string]any{"task": task.Text, "repo": task.Repo}})
	if a.Trace != nil {
		a.appendEvents(ctx, a.Trace.StartRun(ctx, task, a.traceResource(task)))
	}

	var runErr error
	for a.shouldContinue(state) {
		if err := a.Step(ctx, state); err != nil {
			state.Status = "error"
			runErr = err
			a.appendEvent(ctx, core.Event{Type: "error", Data: map[string]any{"error": err.Error()}})
			break
		}
		if state.Submitted {
			state.Status = "submitted"
			break
		}
	}
	if state.Status == "running" {
		state.Status = "limit_reached"
	}

	diff := ""
	if a.Workspace != nil {
		if d, err := a.Workspace.Diff(ctx); err == nil {
			diff = d
		}
	}
	result := Result{
		Status:         state.Status,
		Submission:     state.Submission,
		Diff:           diff,
		Steps:          state.Steps,
		Usage:          state.Usage,
		TrajectoryPath: a.Trajectory.Path(),
	}
	if a.Trace != nil {
		a.appendEvents(ctx, a.Trace.FinishRun(ctx, result.Status, result.Submission))
	}
	a.appendEvent(ctx, core.Event{Type: "final", Data: map[string]any{"status": result.Status, "steps": result.Steps, "submission": result.Submission}})
	return result, runErr
}

func (a *Agent) Step(ctx context.Context, state *State) error {
	state.Steps++
	messages := state.Messages
	var promptSnapshot problemtrace.PromptSnapshot
	var promptContext problemtrace.TraceContext
	if a.Trace != nil {
		promptResult := a.Trace.BuildPrompt(ctx, problemtrace.PromptInput{
			Step:        state.Steps,
			Model:       a.Config.Model.Model,
			Provider:    a.Config.Model.Provider,
			Messages:    state.Messages,
			Tools:       a.Tools.List(),
			Temperature: a.Config.Model.Temperature,
			MaxTokens:   a.Config.Model.MaxTokens,
			WorkingDir:  state.Task.Repo,
		})
		messages = promptResult.Messages
		promptSnapshot = promptResult.Snapshot
		promptContext = promptResult.Context
		a.appendEvents(ctx, promptResult.Events)
	}
	req := core.ModelRequest{
		Messages:    messages,
		Tools:       a.Tools.List(),
		Temperature: a.Config.Model.Temperature,
		MaxTokens:   a.Config.Model.MaxTokens,
		WorkingDir:  state.Task.Repo,
	}
	modelContext := promptContext
	if a.Trace != nil {
		var events []core.Event
		modelContext, events = a.Trace.StartModelCall(ctx, state.Steps, promptSnapshot.ID, a.Config.Model.Provider, a.Config.Model.Model)
		a.appendEvents(ctx, events)
	}
	requestData := map[string]any{
		"step":     state.Steps,
		"messages": len(req.Messages),
	}
	if promptSnapshot.ID != "" {
		requestData["prompt_snapshot_id"] = promptSnapshot.ID
		requestData["prompt_snapshot"] = promptSnapshot
	} else {
		requestData["prompt_snapshot"] = buildPromptSnapshot(req)
	}
	if modelContext.TraceID != "" {
		requestData["trace_context"] = modelContext
	}
	a.appendEvent(ctx, core.Event{Type: "model_request", Data: requestData})

	resp, err := a.Model.Complete(ctx, req)
	if a.Trace != nil {
		a.appendEvents(ctx, a.Trace.EndModelCall(ctx, modelContext, resp, err))
	}
	if err != nil {
		return fmt.Errorf("model complete: %w", err)
	}
	state.Usage.InputTokens += resp.Usage.InputTokens
	state.Usage.OutputTokens += resp.Usage.OutputTokens
	state.Usage.CostUSD += resp.Usage.CostUSD
	state.Messages = append(state.Messages, resp.Message)
	responseData := map[string]any{"step": state.Steps, "content": resp.Message.Content, "usage": resp.Usage}
	if modelContext.TraceID != "" {
		responseData["trace_context"] = modelContext
	}
	a.appendEvent(ctx, core.Event{Type: "model_response", Data: responseData})

	calls, err := a.Parser.Parse(resp)
	if err != nil {
		return fmt.Errorf("parse action: %w", err)
	}
	if len(calls) == 0 {
		return errors.New("model response did not contain an action")
	}

	for _, call := range calls {
		if err := a.executeCall(ctx, state, call); err != nil {
			return err
		}
		if state.Submitted {
			return nil
		}
	}
	return nil
}

func (a *Agent) executeCall(ctx context.Context, state *State, call core.ToolCall) error {
	t, ok := a.Tools.Get(call.Name)
	if !ok {
		msg := fmt.Sprintf("unknown tool %q", call.Name)
		state.Messages = append(state.Messages, core.Message{Role: core.RoleTool, Name: call.Name, Content: msg})
		return nil
	}
	decision, err := a.Policy.AllowTool(ctx, call, t.Spec(), t.Risk())
	if err != nil {
		return fmt.Errorf("policy check %s: %w", call.Name, err)
	}
	if !decision.Allowed {
		msg := "tool denied: " + decision.Reason
		state.Messages = append(state.Messages, core.Message{Role: core.RoleTool, Name: call.Name, Content: msg})
		a.appendEvent(ctx, core.Event{Type: "tool_denied", Data: map[string]any{"tool": call.Name, "reason": decision.Reason}})
		return nil
	}

	var toolContext problemtrace.TraceContext
	if a.Trace != nil {
		var events []core.Event
		toolContext, events = a.Trace.StartToolCall(ctx, state.Steps, call)
		a.appendEvents(ctx, events)
	}
	callData := map[string]any{"tool": call.Name, "args": call.Args}
	if toolContext.TraceID != "" {
		callData["trace_context"] = toolContext
	}
	a.appendEvent(ctx, core.Event{Type: "tool_call", Data: callData})
	result, err := t.Execute(ctx, core.ToolInput{
		Call:          call,
		Runtime:       a.Runtime,
		WorkspaceRoot: a.Workspace.Root(),
		Timeout:       a.Config.RuntimeTimeout(),
	})
	if err != nil {
		return fmt.Errorf("execute tool %s: %w", call.Name, err)
	}
	result = a.Policy.FilterObservation(ctx, result)
	resultData := buildToolResultEventData(call, result)
	if toolContext.TraceID != "" {
		resultData["trace_context"] = toolContext
	}
	a.appendEvent(ctx, core.Event{Type: "tool_result", Data: resultData})
	if a.Trace != nil {
		a.appendEvents(ctx, a.Trace.ObserveToolResult(ctx, toolContext, call, result))
	}

	if call.Name == "submit" || isSubmitOutput(result.Output) {
		state.Submitted = true
		state.Submission = extractSubmission(result.Output)
	}
	state.Messages = append(state.Messages, core.Message{Role: core.RoleTool, Name: call.Name, Content: formatToolObservation(result)})
	return nil
}

func (a *Agent) appendEvent(ctx context.Context, event core.Event) {
	if event.Time.IsZero() {
		event.Time = time.Now()
	}
	_ = a.Trajectory.Append(ctx, event)
	if a.EventSink != nil {
		_ = a.EventSink.EmitEvent(ctx, event)
	}
}

func (a *Agent) appendEvents(ctx context.Context, events []core.Event) {
	for _, event := range events {
		a.appendEvent(ctx, event)
	}
}

func (a *Agent) shouldContinue(state *State) bool {
	if state.Submitted {
		return false
	}
	if a.Config.Agent.MaxSteps > 0 && state.Steps >= a.Config.Agent.MaxSteps {
		return false
	}
	if a.Config.Agent.MaxCostUSD > 0 && state.Usage.CostUSD >= a.Config.Agent.MaxCostUSD {
		return false
	}
	if limit := a.Config.WallTimeLimit(); limit > 0 && time.Since(state.startedAt) >= limit {
		return false
	}
	return true
}

func (a *Agent) initialUserMessage(task core.Task) string {
	return fmt.Sprintf("Task:\n%s\n\nRepository root: %s\n\nWork step by step. Inspect relevant files before editing. Run focused verification before submitting.", task.Text, task.Repo)
}

func (a *Agent) traceResource(task core.Task) problemtrace.TraceResource {
	return problemtrace.TraceResource{
		RepoPath:      task.Repo,
		RepoLanguage:  "unknown",
		AgentVersion:  "dev",
		Runtime:       a.Config.Runtime.Type,
		ModelProvider: a.Config.Model.Provider,
		Model:         a.Config.Model.Model,
	}
}

func buildPromptSnapshot(req core.ModelRequest) map[string]any {
	blocks := []map[string]any{
		promptBlock("System Rules", countMessages(req.Messages, core.RoleSystem), "agent system prompt"),
		promptBlock("User Task", countMessages(req.Messages, core.RoleUser), "current task and repository root"),
		promptBlock("Recent Observations", countMessages(req.Messages, core.RoleTool), "tool observations from this run"),
		promptBlock("Conversation State", countMessages(req.Messages, core.RoleAssistant), "visible assistant responses from this run"),
		promptBlock("Tool Schema", len(req.Tools), "available tool names and schemas"),
	}
	return map[string]any{
		"working_dir":     req.WorkingDir,
		"temperature":     req.Temperature,
		"max_tokens":      req.MaxTokens,
		"message_count":   len(req.Messages),
		"tool_count":      len(req.Tools),
		"token_estimate":  estimatePromptTokens(req.Messages, req.Tools),
		"blocks":          blocks,
		"message_summary": summarizePromptMessages(req.Messages),
	}
}

const traceOutputPreviewLimit = 1600

func buildToolResultEventData(call core.ToolCall, result core.ToolResult) map[string]any {
	redacted := redactTraceOutput(result.Output)
	preview, truncated := truncateRunes(redacted, traceOutputPreviewLimit)
	data := map[string]any{
		"tool":             call.Name,
		"code":             result.Code,
		"timed_out":        result.TimedOut,
		"output_preview":   preview,
		"output_hash":      hashTraceOutput(result.Output),
		"output_chars":     len([]rune(result.Output)),
		"output_truncated": truncated,
	}
	if redacted != result.Output {
		data["output_redacted"] = true
	}
	if len(result.Artifacts) > 0 {
		data["artifacts"] = result.Artifacts
	}
	if len(result.Metadata) > 0 {
		data["metadata"] = result.Metadata
	}
	return data
}

func truncateRunes(value string, limit int) (string, bool) {
	runes := []rune(value)
	if len(runes) <= limit {
		return value, false
	}
	if limit <= 3 {
		return string(runes[:limit]), true
	}
	return string(runes[:limit-3]) + "...", true
}

func hashTraceOutput(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func redactTraceOutput(value string) string {
	out := value
	assignment := regexp.MustCompile(`(?i)(OPENAI_API_KEY|ANTHROPIC_API_KEY|GITHUB_TOKEN|GH_TOKEN|AWS_SECRET_ACCESS_KEY|AWS_ACCESS_KEY_ID|SECRET|TOKEN|PASSWORD)=([^\s]+)`)
	privateKey := regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----`)
	out = assignment.ReplaceAllString(out, "$1=[REDACTED]")
	out = privateKey.ReplaceAllString(out, "[REDACTED PRIVATE KEY]")
	return out
}

func promptBlock(name string, count int, summary string) map[string]any {
	return map[string]any{
		"name":     name,
		"count":    count,
		"included": count > 0,
		"summary":  summary,
	}
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

func estimatePromptTokens(messages []core.Message, tools []core.ToolSpec) int {
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

func summarizePromptMessages(messages []core.Message) []map[string]any {
	summaries := make([]map[string]any, 0, len(messages))
	for i, msg := range messages {
		name := strings.TrimSpace(msg.Name)
		summaries = append(summaries, map[string]any{
			"index":   i + 1,
			"role":    string(msg.Role),
			"name":    name,
			"chars":   len([]rune(msg.Content)),
			"summary": shortPromptContent(msg.Content, 240),
		})
	}
	return summaries
}

func shortPromptContent(content string, limit int) string {
	content = strings.Join(strings.Fields(content), " ")
	runes := []rune(content)
	if len(runes) <= limit {
		return content
	}
	if limit <= 3 {
		return string(runes[:limit])
	}
	return string(runes[:limit-3]) + "..."
}

func (a *Agent) validate() error {
	if a.Model == nil {
		return errors.New("agent model is nil")
	}
	if a.Runtime == nil {
		return errors.New("agent runtime is nil")
	}
	if a.Tools == nil {
		return errors.New("agent tools registry is nil")
	}
	if a.Parser == nil {
		return errors.New("agent action parser is nil")
	}
	if a.Policy == nil {
		return errors.New("agent policy is nil")
	}
	if a.Trajectory == nil {
		return errors.New("agent trajectory store is nil")
	}
	if a.Workspace == nil {
		return errors.New("agent workspace is nil")
	}
	return nil
}

func formatToolObservation(result core.ToolResult) string {
	var b strings.Builder
	if result.Code != 0 {
		fmt.Fprintf(&b, "<returncode>%d</returncode>\n", result.Code)
	}
	if result.TimedOut {
		b.WriteString("<timeout>true</timeout>\n")
	}
	b.WriteString("<output>\n")
	b.WriteString(result.Output)
	if !strings.HasSuffix(result.Output, "\n") {
		b.WriteByte('\n')
	}
	b.WriteString("</output>")
	return b.String()
}

func isSubmitOutput(out string) bool {
	return strings.Contains(out, "COMPLETE_TASK_AND_SUBMIT_FINAL_OUTPUT")
}

func extractSubmission(out string) string {
	lines := strings.Split(out, "\n")
	for i, line := range lines {
		if strings.TrimSpace(line) == "COMPLETE_TASK_AND_SUBMIT_FINAL_OUTPUT" {
			return strings.TrimSpace(strings.Join(lines[i+1:], "\n"))
		}
	}
	return strings.TrimSpace(out)
}
