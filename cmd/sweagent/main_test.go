package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/local/swe-agent/internal/problemtrace"
	"github.com/local/swe-agent/internal/trajectory"
)

func TestRunCommandMockSubmit(t *testing.T) {
	dir := t.TempDir()
	repo := t.TempDir()
	trajectoryDir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	config := "model:\n" +
		"  provider: mock\n" +
		"trajectory:\n" +
		"  dir: " + trajectoryDir + "\n"
	if err := os.WriteFile(configPath, []byte(config), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	err := run([]string{"run", "--config", configPath, "--repo", repo, "--task", "finish", "--json"})
	if err != nil {
		t.Fatalf("run command returned error: %v", err)
	}
}

func TestRunCommandRejectsTUIWithJSON(t *testing.T) {
	err := run([]string{"run", "--task", "finish", "--tui", "--json"})
	if err == nil {
		t.Fatal("expected --tui and --json to be rejected")
	}
}

func TestRunCommandCodexCLIProvider(t *testing.T) {
	dir := t.TempDir()
	repo := t.TempDir()
	trajectoryDir := t.TempDir()
	fakeCodex := filepath.Join(dir, "codex")
	script := "#!/bin/sh\n" +
		"out=\"\"\n" +
		"while [ \"$#\" -gt 0 ]; do\n" +
		"  case \"$1\" in\n" +
		"    --output-last-message)\n" +
		"      shift\n" +
		"      out=\"$1\"\n" +
		"      ;;\n" +
		"  esac\n" +
		"  shift\n" +
		"done\n" +
		"cat >/dev/null\n" +
		"printf '```swe_shell\\nsubmit\\n```' > \"$out\"\n"
	if err := os.WriteFile(fakeCodex, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}
	configPath := filepath.Join(dir, "config.yaml")
	config := "model:\n" +
		"  provider: codex-cli\n" +
		"  command: " + fakeCodex + "\n" +
		"  sandbox: read-only\n" +
		"  approval_policy: never\n" +
		"trajectory:\n" +
		"  dir: " + trajectoryDir + "\n"
	if err := os.WriteFile(configPath, []byte(config), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	err := run([]string{"run", "--config", configPath, "--repo", repo, "--task", "finish", "--json"})
	if err != nil {
		t.Fatalf("run command returned error: %v", err)
	}
}

func TestBuildAgentCodexCLIProviderDoesNotInheritMockModel(t *testing.T) {
	dir := t.TempDir()
	repo := t.TempDir()
	trajectoryDir := t.TempDir()
	fakeCodex := filepath.Join(dir, "codex")
	if err := os.WriteFile(fakeCodex, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}
	configPath := filepath.Join(dir, "config.yaml")
	config := "model:\n" +
		"  provider: codex-cli\n" +
		"  command: " + fakeCodex + "\n" +
		"trajectory:\n" +
		"  dir: " + trajectoryDir + "\n"
	if err := os.WriteFile(configPath, []byte(config), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	ag, _, store, err := buildAgent(agentOptions{configPath: configPath, repo: repo}, false)
	if err != nil {
		t.Fatalf("buildAgent returned error: %v", err)
	}
	defer store.Close()

	if ag.Config.Model.Model != "" {
		t.Fatalf("expected codex-cli default model to be empty, got %q", ag.Config.Model.Model)
	}
}

func TestPreviewFixtureWritesLoadableTrace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trace-preview.jsonl")
	if err := run([]string{"preview-fixture", "--output", path}); err != nil {
		t.Fatalf("preview-fixture returned error: %v", err)
	}

	events, err := trajectory.LoadFile(context.Background(), path)
	if err != nil {
		t.Fatalf("load preview fixture: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("expected preview fixture events")
	}
	trace := problemtrace.Replay(events)
	if trace.TraceID != "trace-preview" {
		t.Fatalf("expected trace-preview id, got %q", trace.TraceID)
	}
	if len(trace.Directions) == 0 {
		t.Fatalf("expected fixture directions, got %#v", trace)
	}
}

func TestPreviewCommandRenderLoadsTrace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trace-preview.jsonl")
	if err := run([]string{"preview-fixture", "--output", path}); err != nil {
		t.Fatalf("preview-fixture returned error: %v", err)
	}

	output := captureStdout(t, func() {
		if err := run([]string{"preview", "--trace", path, "--render", "--width", "140", "--height", "24"}); err != nil {
			t.Fatalf("preview --render returned error: %v", err)
		}
	})
	for _, want := range []string{
		"Problem Trace Workspace",
		"Trace Tree",
		"Selected Detail",
		"Fix unresolved PR review comments",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("expected preview render to contain %q, got:\n%s", want, output)
		}
	}
	if strings.Contains(output, "[active]") {
		t.Fatalf("expected active pane focus to be styled, not rendered as text:\n%s", output)
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	readFile, writeFile, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe stdout: %v", err)
	}
	os.Stdout = writeFile
	defer func() {
		os.Stdout = old
	}()

	fn()
	if err := writeFile.Close(); err != nil {
		t.Fatalf("close stdout pipe: %v", err)
	}
	data, err := io.ReadAll(readFile)
	if err != nil {
		t.Fatalf("read stdout pipe: %v", err)
	}
	return string(data)
}
