package model

import (
	"context"
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

	promptBytes, err := os.ReadFile(capturePrompt)
	if err != nil {
		t.Fatalf("read captured prompt: %v", err)
	}
	prompt := string(promptBytes)
	for _, want := range []string{
		"Return exactly one fenced shell action block",
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
	for _, want := range []string{"exec", "--ephemeral", "--skip-git-repo-check", "--sandbox", "read-only", "--ask-for-approval", "never", "-C", "/repo", "-m", "gpt-5"} {
		if !strings.Contains(args, want) {
			t.Fatalf("args missing %q:\n%s", want, args)
		}
	}
	lines := strings.Split(strings.TrimSpace(args), "\n")
	if len(lines) < 5 || lines[0] != "--sandbox" || lines[2] != "--ask-for-approval" || lines[4] != "exec" {
		t.Fatalf("approval and sandbox flags should be top-level args before exec:\n%s", args)
	}
}

func TestBuildCodexPromptRequiresOneShellAction(t *testing.T) {
	prompt := buildCodexPrompt(core.ModelRequest{
		Messages: []core.Message{{Role: core.RoleUser, Content: "task"}},
	})
	for _, want := range []string{"```swe_shell", "submit", "Conversation so far:"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}
