package agent_test

import (
	"context"
	"strings"
	"testing"

	"github.com/local/swe-agent/internal/action"
	agentpkg "github.com/local/swe-agent/internal/agent"
	"github.com/local/swe-agent/internal/core"
	"github.com/local/swe-agent/internal/model"
	"github.com/local/swe-agent/internal/policy"
	localruntime "github.com/local/swe-agent/internal/runtime"
	"github.com/local/swe-agent/internal/tool"
	"github.com/local/swe-agent/internal/trajectory"
	"github.com/local/swe-agent/internal/workspace"
)

func TestAgentRunWithMockSubmit(t *testing.T) {
	ctx := context.Background()
	cfg := agentpkg.DefaultConfig()
	cfg.Trajectory.Dir = t.TempDir()
	cfg.Policy.AutoApproveRead = true
	cfg.Policy.AutoApproveWrite = false
	cfg.Policy.AutoApproveExec = false

	ws, err := workspace.New(t.TempDir())
	if err != nil {
		t.Fatalf("workspace.New: %v", err)
	}
	store, err := trajectory.NewJSONLStore(cfg.Trajectory.Dir)
	if err != nil {
		t.Fatalf("trajectory.NewJSONLStore: %v", err)
	}
	defer store.Close()

	ag := &agentpkg.Agent{
		Config:     cfg,
		Model:      model.NewMock(nil),
		Runtime:    localruntime.NewLocal(cfg.Runtime.Env),
		Tools:      tool.NewRegistry(cfg.Tools.Enabled),
		Parser:     action.NewParser(),
		Policy:     policy.NewSimple(cfg.Policy),
		Trajectory: store,
		Workspace:  ws,
	}
	result, err := ag.Run(ctx, core.Task{Text: "finish", Repo: ws.Root()})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result.Status != "submitted" {
		t.Fatalf("expected submitted status, got %q", result.Status)
	}
	if result.Steps != 1 {
		t.Fatalf("expected 1 step, got %d", result.Steps)
	}
}

func TestSubmitGuardRejectsEarlyFixSubmit(t *testing.T) {
	ctx := context.Background()
	cfg := agentpkg.DefaultConfig()
	cfg.Agent.MaxSteps = 1
	cfg.Trajectory.Dir = t.TempDir()
	cfg.Policy.AutoApproveRead = true

	ws, err := workspace.New(t.TempDir())
	if err != nil {
		t.Fatalf("workspace.New: %v", err)
	}
	store, err := trajectory.NewJSONLStore(cfg.Trajectory.Dir)
	if err != nil {
		t.Fatalf("trajectory.NewJSONLStore: %v", err)
	}
	defer store.Close()

	ag := &agentpkg.Agent{
		Config:     cfg,
		Model:      model.NewMock([]string{"```swe_shell\nsubmit\n```"}),
		Runtime:    localruntime.NewLocal(cfg.Runtime.Env),
		Tools:      tool.NewRegistry(cfg.Tools.Enabled),
		Parser:     action.NewParser(),
		Policy:     policy.NewSimple(cfg.Policy),
		Trajectory: store,
		Workspace:  ws,
	}
	result, err := ag.Run(ctx, core.Task{Text: "fix the failing test", Repo: ws.Root()})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result.Status == "submitted" {
		t.Fatalf("early submit should not be accepted: %#v", result)
	}
	events, err := store.Load(ctx)
	if err != nil {
		t.Fatalf("load trajectory: %v", err)
	}
	found := false
	for _, event := range events {
		if event.Type == "submit_rejected" && strings.Contains(event.Data["reason"].(string), "no workspace change") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected submit_rejected event, got %#v", events)
	}
}

func TestAgentEmitsToolProposedBeforePolicyDecision(t *testing.T) {
	ctx := context.Background()
	cfg := agentpkg.DefaultConfig()
	cfg.Agent.MaxSteps = 1
	cfg.Trajectory.Dir = t.TempDir()
	cfg.Policy.AutoApproveExec = false

	ws, err := workspace.New(t.TempDir())
	if err != nil {
		t.Fatalf("workspace.New: %v", err)
	}
	store, err := trajectory.NewJSONLStore(cfg.Trajectory.Dir)
	if err != nil {
		t.Fatalf("trajectory.NewJSONLStore: %v", err)
	}
	defer store.Close()

	ag := &agentpkg.Agent{
		Config:     cfg,
		Model:      model.NewMock([]string{"```swe_shell\necho hi\n```"}),
		Runtime:    localruntime.NewLocal(cfg.Runtime.Env),
		Tools:      tool.NewRegistry(cfg.Tools.Enabled),
		Parser:     action.NewParser(),
		Policy:     policy.NewSimple(cfg.Policy),
		Trajectory: store,
		Workspace:  ws,
	}
	if _, err := ag.Run(ctx, core.Task{Text: "run a command", Repo: ws.Root()}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	events, err := store.Load(ctx)
	if err != nil {
		t.Fatalf("load trajectory: %v", err)
	}
	proposedIndex := -1
	deniedIndex := -1
	for i, event := range events {
		switch event.Type {
		case "tool_proposed":
			proposedIndex = i
			risk, _ := event.Data["risk"].(string)
			if event.Data["tool"] != "shell" || risk != string(core.RiskExec) {
				t.Fatalf("unexpected tool_proposed data: %#v", event.Data)
			}
		case "tool_denied":
			deniedIndex = i
		case "tool_call":
			t.Fatalf("denied tool should not emit tool_call: %#v", event)
		}
	}
	if proposedIndex < 0 {
		t.Fatalf("expected tool_proposed event, got %#v", events)
	}
	if deniedIndex < 0 {
		t.Fatalf("expected tool_denied event, got %#v", events)
	}
	if proposedIndex > deniedIndex {
		t.Fatalf("tool_proposed should precede tool_denied, proposed=%d denied=%d", proposedIndex, deniedIndex)
	}
}

func TestPRPreflightBlocksWhenGHUnavailable(t *testing.T) {
	ctx := context.Background()
	cfg := agentpkg.DefaultConfig()
	cfg.Trajectory.Dir = t.TempDir()
	cfg.Policy.AutoApproveRead = true
	rt := &recordingRuntime{result: core.ExecResult{Stdout: "gh auth failed\n", Code: 1}}

	ws, err := workspace.New(t.TempDir())
	if err != nil {
		t.Fatalf("workspace.New: %v", err)
	}
	store, err := trajectory.NewJSONLStore(cfg.Trajectory.Dir)
	if err != nil {
		t.Fatalf("trajectory.NewJSONLStore: %v", err)
	}
	defer store.Close()

	ag := &agentpkg.Agent{
		Config:     cfg,
		Model:      model.NewMock([]string{"```swe_shell\nsubmit\n```"}),
		Runtime:    rt,
		Tools:      tool.NewRegistry(cfg.Tools.Enabled),
		Parser:     action.NewParser(),
		Policy:     policy.NewSimple(cfg.Policy),
		Trajectory: store,
		Workspace:  ws,
	}
	result, err := ag.Run(ctx, core.Task{
		Text: "修复 https://github.com/TencentBlueKing/bk-monitor/pull/11216 的 unresolved comments，然后 commit push",
		Repo: ws.Root(),
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result.Status != "blocked" {
		t.Fatalf("expected blocked status, got %#v", result)
	}
	if result.Steps != 0 {
		t.Fatalf("preflight should block before model steps, got %d", result.Steps)
	}
	if len(rt.requests) != 1 {
		t.Fatalf("expected one preflight command, got %d", len(rt.requests))
	}
	command := rt.requests[0].Command
	for _, want := range []string{"gh auth status", "gh pr view 'https://github.com/TencentBlueKing/bk-monitor/pull/11216'", "git remote -v"} {
		if !strings.Contains(command, want) {
			t.Fatalf("preflight command missing %q:\n%s", want, command)
		}
	}
}

type recordingRuntime struct {
	requests []core.ExecRequest
	result   core.ExecResult
}

func (r *recordingRuntime) Execute(ctx context.Context, req core.ExecRequest) (core.ExecResult, error) {
	r.requests = append(r.requests, req)
	return r.result, nil
}

func (r *recordingRuntime) TemplateVars(ctx context.Context) map[string]string {
	return map[string]string{}
}

func (r *recordingRuntime) Close(ctx context.Context) error {
	return nil
}
