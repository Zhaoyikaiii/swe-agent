package agent_test

import (
	"context"
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
