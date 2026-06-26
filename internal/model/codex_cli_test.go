package model

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/local/swe-agent/internal/core"
)

func TestCodexCLICompleteUsesOutputLastMessage(t *testing.T) {
	dir := t.TempDir()
	capturePrompt := filepath.Join(dir, "prompt.txt")
	captureArgs := filepath.Join(dir, "args.txt")
	fakeCodex := filepath.Join(dir, "codex")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$@\" > \"$CAPTURE_ARGS\"\n" +
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
		"cat > \"$CAPTURE_PROMPT\"\n" +
		"printf 'debug stdout\\n'\n" +
		"printf 'debug stderr\\n' >&2\n" +
		"printf '```swe_shell\\nsubmit\\n```' > \"$out\"\n"
	if err := os.WriteFile(fakeCodex, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}
	t.Setenv("CAPTURE_PROMPT", capturePrompt)
	t.Setenv("CAPTURE_ARGS", captureArgs)

	m, err := NewCodexCLI(CodexCLIOptions{
		Command:        fakeCodex,
		Model:          "gpt-5",
		Sandbox:        "read-only",
		ApprovalPolicy: "never",
	})
	if err != nil {
		t.Fatalf("NewCodexCLI: %v", err)
	}
	resp, err := m.Complete(context.Background(), core.ModelRequest{
		WorkingDir: "/repo",
		Messages: []core.Message{
			{Role: core.RoleSystem, Content: "system prompt"},
			{Role: core.RoleUser, Content: "fix task"},
			{Role: core.RoleTool, Name: "shell", Content: "<output>test failed</output>"},
		},
		Tools: []core.ToolSpec{{Name: "shell", Description: "Run a command."}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if got := resp.Message.Content; got != "```swe_shell\nsubmit\n```" {
		t.Fatalf("unexpected content: %q", got)
	}
	for key, want := range map[string]string{
		"codex_command":              "--output-last-message",
		"codex_stdout_preview":       "debug stdout",
		"codex_stderr_preview":       "debug stderr",
		"codex_last_message_preview": "```swe_shell\nsubmit\n```",
	} {
		if got := resp.Message.Extra[key]; !strings.Contains(got, want) {
			t.Fatalf("extra[%s] missing %q: %#v", key, want, resp.Message.Extra)
		}
	}

	promptBytes, err := os.ReadFile(capturePrompt)
	if err != nil {
		t.Fatalf("read captured prompt: %v", err)
	}
	prompt := string(promptBytes)
	for _, want := range []string{
		"Return exactly one fenced shell action block",
		"Never submit immediately for tasks that mention fixes",
		"If the task references a GitHub PR, inspect the PR and review comments first.",
		"Outer agent workspace: /repo",
		"- shell: Run a command.",
		"<tool name=shell>",
		"<output>test failed</output>",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}

	argsBytes, err := os.ReadFile(captureArgs)
	if err != nil {
		t.Fatalf("read captured args: %v", err)
	}
	args := string(argsBytes)
	for _, want := range []string{"exec", "--ephemeral", "--skip-git-repo-check", "--json", "--sandbox", "read-only", "--ask-for-approval", "never", "-C", "/repo", "-m", "gpt-5"} {
		if !strings.Contains(args, want) {
			t.Fatalf("args missing %q:\n%s", want, args)
		}
	}
	lines := strings.Split(strings.TrimSpace(args), "\n")
	if len(lines) < 5 || lines[0] != "--sandbox" || lines[2] != "--ask-for-approval" || lines[4] != "exec" {
		t.Fatalf("approval and sandbox flags should be top-level args before exec:\n%s", args)
	}
}

func TestCodexCLICompleteFallsBackToJSONStdout(t *testing.T) {
	dir := t.TempDir()
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
		": > \"$out\"\n" +
		"printf '%s\\n' '{\"type\":\"agent_message\",\"message\":\"```swe_shell\\nsubmit\\n```\"}'\n"
	if err := os.WriteFile(fakeCodex, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}

	m, err := NewCodexCLI(CodexCLIOptions{Command: fakeCodex})
	if err != nil {
		t.Fatalf("NewCodexCLI: %v", err)
	}
	resp, err := m.Complete(context.Background(), core.ModelRequest{
		Messages: []core.Message{{Role: core.RoleUser, Content: "finish"}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if got := resp.Message.Content; got != "```swe_shell\nsubmit\n```" {
		t.Fatalf("unexpected content: %q", got)
	}
}

func TestCodexCLICompleteReportsDiagnosticsForEmptyMessage(t *testing.T) {
	dir := t.TempDir()
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
		": > \"$out\"\n" +
		"printf '%s\\n' '{\"type\":\"session.started\"}'\n" +
		"printf '%s\\n' 'auth or config detail' >&2\n"
	if err := os.WriteFile(fakeCodex, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}

	m, err := NewCodexCLI(CodexCLIOptions{Command: fakeCodex})
	if err != nil {
		t.Fatalf("NewCodexCLI: %v", err)
	}
	_, err = m.Complete(context.Background(), core.ModelRequest{
		Messages: []core.Message{{Role: core.RoleUser, Content: "finish"}},
	})
	if err == nil {
		t.Fatal("expected empty message error")
	}
	msg := err.Error()
	for _, want := range []string{
		"codex exec returned an empty message",
		"codex command:",
		"--json",
		"output_last_message:",
		"stdout:",
		"session.started",
		"stderr:",
		"auth or config detail",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error missing %q:\n%s", want, msg)
		}
	}
}

func TestExtractCodexJSONMessageCombinesDeltas(t *testing.T) {
	first, err := json.Marshal(map[string]string{"type": "agent_message_delta", "delta": "```swe_shell\n"})
	if err != nil {
		t.Fatalf("marshal first delta: %v", err)
	}
	second, err := json.Marshal(map[string]string{"type": "agent_message_delta", "delta": "submit\n```"})
	if err != nil {
		t.Fatalf("marshal second delta: %v", err)
	}
	stdout := string(first) + "\n" + string(second)

	got := extractCodexJSONMessage(stdout)
	if got != "```swe_shell\nsubmit\n```" {
		t.Fatalf("extractCodexJSONMessage() = %q", got)
	}
}

func TestBuildCodexPromptRequiresOneShellAction(t *testing.T) {
	prompt := buildCodexPrompt(core.ModelRequest{
		Messages: []core.Message{{Role: core.RoleUser, Content: "task"}},
	})
	for _, want := range []string{"```swe_shell", "submit", "Before submit:", "Conversation so far:"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}
