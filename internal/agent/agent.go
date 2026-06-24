package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/local/swe-agent/internal/core"
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
	_ = a.Trajectory.Append(ctx, core.Event{Type: "user_task", Time: time.Now(), Data: map[string]any{"task": task.Text, "repo": task.Repo}})

	var runErr error
	for a.shouldContinue(state) {
		if err := a.Step(ctx, state); err != nil {
			state.Status = "error"
			runErr = err
			_ = a.Trajectory.Append(ctx, core.Event{Type: "error", Time: time.Now(), Data: map[string]any{"error": err.Error()}})
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
	_ = a.Trajectory.Append(ctx, core.Event{Type: "final", Time: time.Now(), Data: map[string]any{"status": result.Status, "steps": result.Steps, "submission": result.Submission}})
	return result, runErr
}

func (a *Agent) Step(ctx context.Context, state *State) error {
	state.Steps++
	req := core.ModelRequest{
		Messages:    state.Messages,
		Tools:       a.Tools.List(),
		Temperature: a.Config.Model.Temperature,
		MaxTokens:   a.Config.Model.MaxTokens,
	}
	_ = a.Trajectory.Append(ctx, core.Event{Type: "model_request", Time: time.Now(), Data: map[string]any{"step": state.Steps, "messages": len(req.Messages)}})

	resp, err := a.Model.Complete(ctx, req)
	if err != nil {
		return fmt.Errorf("model complete: %w", err)
	}
	state.Usage.InputTokens += resp.Usage.InputTokens
	state.Usage.OutputTokens += resp.Usage.OutputTokens
	state.Usage.CostUSD += resp.Usage.CostUSD
	state.Messages = append(state.Messages, resp.Message)
	_ = a.Trajectory.Append(ctx, core.Event{Type: "model_response", Time: time.Now(), Data: map[string]any{"step": state.Steps, "content": resp.Message.Content, "usage": resp.Usage}})

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
		_ = a.Trajectory.Append(ctx, core.Event{Type: "tool_denied", Time: time.Now(), Data: map[string]any{"tool": call.Name, "reason": decision.Reason}})
		return nil
	}

	_ = a.Trajectory.Append(ctx, core.Event{Type: "tool_call", Time: time.Now(), Data: map[string]any{"tool": call.Name, "args": call.Args}})
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
	_ = a.Trajectory.Append(ctx, core.Event{Type: "tool_result", Time: time.Now(), Data: map[string]any{"tool": call.Name, "code": result.Code, "timed_out": result.TimedOut, "output": result.Output}})

	if call.Name == "submit" || isSubmitOutput(result.Output) {
		state.Submitted = true
		state.Submission = extractSubmission(result.Output)
	}
	state.Messages = append(state.Messages, core.Message{Role: core.RoleTool, Name: call.Name, Content: formatToolObservation(result)})
	return nil
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
