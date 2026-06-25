package model

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/local/swe-agent/internal/core"
)

type CodexCLIOptions struct {
	Command        string
	Model          string
	Profile        string
	OSS            bool
	LocalProvider  string
	Sandbox        string
	ApprovalPolicy string
	ExtraArgs      []string
}

type CodexCLI struct {
	Command        string
	Model          string
	Profile        string
	OSS            bool
	LocalProvider  string
	Sandbox        string
	ApprovalPolicy string
	ExtraArgs      []string
}

func NewCodexCLI(opts CodexCLIOptions) (*CodexCLI, error) {
	if opts.Command == "" {
		opts.Command = "codex"
	}
	if _, err := exec.LookPath(opts.Command); err != nil {
		return nil, fmt.Errorf("codex CLI command %q not found: %w", opts.Command, err)
	}
	if opts.Sandbox == "" {
		opts.Sandbox = "read-only"
	}
	if opts.ApprovalPolicy == "" {
		opts.ApprovalPolicy = "never"
	}
	return &CodexCLI{
		Command:        opts.Command,
		Model:          opts.Model,
		Profile:        opts.Profile,
		OSS:            opts.OSS,
		LocalProvider:  opts.LocalProvider,
		Sandbox:        opts.Sandbox,
		ApprovalPolicy: opts.ApprovalPolicy,
		ExtraArgs:      append([]string(nil), opts.ExtraArgs...),
	}, nil
}

func (m *CodexCLI) Complete(ctx context.Context, req core.ModelRequest) (core.ModelResponse, error) {
	tmpDir, err := os.MkdirTemp("", "swe-agent-codex-*")
	if err != nil {
		return core.ModelResponse{}, err
	}
	defer os.RemoveAll(tmpDir)

	outputPath := filepath.Join(tmpDir, "last-message.txt")
	args := m.args(req.WorkingDir, outputPath)
	cmd := exec.CommandContext(ctx, m.Command, args...)
	cmd.Stdin = strings.NewReader(buildCodexPrompt(req))
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	start := time.Now()
	runErr := cmd.Run()
	elapsed := time.Since(start)

	contentBytes, readErr := os.ReadFile(outputPath)
	diag := codexExecDiagnostics{
		command:    m.Command,
		args:       args,
		elapsed:    elapsed,
		outputPath: outputPath,
		outputSize: len(contentBytes),
		readErr:    readErr,
		stdout:     stdout.String(),
		stderr:     stderr.String(),
	}
	if runErr != nil {
		return core.ModelResponse{}, fmt.Errorf("codex exec failed: %w\n%s", runErr, diag.String())
	}
	content := ""
	if readErr == nil {
		content = strings.TrimSpace(string(contentBytes))
	}
	if content == "" {
		content = extractCodexJSONMessage(stdout.String())
	}
	if content == "" && readErr != nil {
		return core.ModelResponse{}, fmt.Errorf("read codex last message: %w\n%s", readErr, diag.String())
	}
	if content == "" {
		return core.ModelResponse{}, fmt.Errorf("codex exec returned an empty message\n%s", diag.String())
	}
	return core.ModelResponse{
		Message: core.Message{Role: core.RoleAssistant, Content: content},
		Usage: core.Usage{
			OutputTokens: len(content) / 4,
		},
		FinishReason: fmt.Sprintf("codex-cli elapsed=%s", elapsed.Truncate(time.Millisecond)),
	}, nil
}

func (m *CodexCLI) args(workingDir, outputPath string) []string {
	args := []string{}
	if m.Sandbox != "" {
		args = append(args, "--sandbox", m.Sandbox)
	}
	if m.ApprovalPolicy != "" {
		args = append(args, "--ask-for-approval", m.ApprovalPolicy)
	}
	args = append(args, "exec", "--ephemeral", "--skip-git-repo-check", "--json", "--output-last-message", outputPath)
	if workingDir != "" {
		args = append(args, "-C", workingDir)
	}
	if m.Model != "" && m.Model != "mock" {
		args = append(args, "-m", m.Model)
	}
	if m.Profile != "" {
		args = append(args, "-p", m.Profile)
	}
	if m.OSS {
		args = append(args, "--oss")
	}
	if m.LocalProvider != "" {
		args = append(args, "--local-provider", m.LocalProvider)
	}
	args = append(args, m.ExtraArgs...)
	args = append(args, "-")
	return args
}

type codexExecDiagnostics struct {
	command    string
	args       []string
	elapsed    time.Duration
	outputPath string
	outputSize int
	readErr    error
	stdout     string
	stderr     string
}

func (d codexExecDiagnostics) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "codex command: %s\n", shellQuote(append([]string{d.command}, d.args...)))
	fmt.Fprintf(&b, "elapsed: %s\n", d.elapsed.Truncate(time.Millisecond))
	fmt.Fprintf(&b, "output_last_message: %s size=%d", d.outputPath, d.outputSize)
	if d.readErr != nil {
		fmt.Fprintf(&b, " read_error=%v", d.readErr)
	}
	b.WriteByte('\n')
	fmt.Fprintf(&b, "stdout:\n%s\n", indentDiagnostic(previewDiagnostic(d.stdout)))
	fmt.Fprintf(&b, "stderr:\n%s", indentDiagnostic(previewDiagnostic(d.stderr)))
	return b.String()
}

func shellQuote(args []string) string {
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		if arg == "" {
			quoted = append(quoted, "''")
			continue
		}
		if strings.IndexFunc(arg, func(r rune) bool {
			return !(r == '-' || r == '_' || r == '.' || r == '/' || r == ':' || r == '=' || r == '+' || r == ',' ||
				(r >= '0' && r <= '9') || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z'))
		}) == -1 {
			quoted = append(quoted, arg)
			continue
		}
		quoted = append(quoted, strconv.Quote(arg))
	}
	return strings.Join(quoted, " ")
}

func previewDiagnostic(s string) string {
	const limit = 4096
	s = strings.TrimSpace(s)
	if s == "" {
		return "<empty>"
	}
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "\n<truncated>"
}

func indentDiagnostic(s string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = "  " + line
	}
	return strings.Join(lines, "\n")
}

func extractCodexJSONMessage(stdout string) string {
	scanner := bufio.NewScanner(strings.NewReader(stdout))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var last string
	var delta strings.Builder
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var value any
		if err := json.Unmarshal([]byte(line), &value); err != nil {
			continue
		}
		if message := extractAssistantMessage(value); strings.TrimSpace(message) != "" {
			if isAssistantDelta(value) {
				delta.WriteString(message)
				last = delta.String()
				continue
			}
			delta.Reset()
			last = message
		}
	}
	return strings.TrimSpace(last)
}

func isAssistantDelta(value any) bool {
	obj, ok := value.(map[string]any)
	if !ok {
		return false
	}
	typeHint := strings.ToLower(firstString(obj, "type", "event", "kind", "name"))
	if strings.Contains(typeHint, "delta") {
		return true
	}
	if _, ok := obj["delta"]; ok {
		return true
	}
	for _, key := range []string{"msg", "message", "item"} {
		if nested, ok := obj[key]; ok && isAssistantDelta(nested) {
			return true
		}
	}
	return false
}

func extractAssistantMessage(value any) string {
	obj, ok := value.(map[string]any)
	if !ok {
		return ""
	}

	if nested, ok := obj["msg"]; ok {
		if message := extractAssistantMessage(nested); message != "" {
			return message
		}
	}
	if nested, ok := obj["message"]; ok {
		if message := extractAssistantMessage(nested); message != "" {
			return message
		}
	}
	if nested, ok := obj["item"]; ok {
		if message := extractAssistantMessage(nested); message != "" {
			return message
		}
	}

	typeHint := strings.ToLower(firstString(obj, "type", "event", "kind", "name"))
	role := strings.ToLower(firstString(obj, "role"))
	if role == "assistant" || strings.Contains(typeHint, "assistant") || strings.Contains(typeHint, "agent_message") {
		return extractText(obj)
	}
	return ""
}

func firstString(obj map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := obj[key].(string); ok {
			return value
		}
	}
	return ""
}

func extractText(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case []any:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			if text := strings.TrimSpace(extractText(item)); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "\n")
	case map[string]any:
		for _, key := range []string{"content", "text", "message", "output", "delta"} {
			if text := extractText(v[key]); strings.TrimSpace(text) != "" {
				return text
			}
		}
	}
	return ""
}

func buildCodexPrompt(req core.ModelRequest) string {
	var b strings.Builder
	b.WriteString("You are the decision model inside a Go SWE-agent.\n")
	b.WriteString("Do not solve the task by editing files yourself. Do not make final prose unless submitting.\n")
	b.WriteString("Return exactly one fenced shell action block and no other fenced blocks.\n")
	b.WriteString("Use this exact format:\n\n")
	b.WriteString("```swe_shell\n")
	b.WriteString("<one command for the outer SWE-agent to run>\n")
	b.WriteString("```\n\n")
	b.WriteString("When the task is complete, return:\n\n")
	b.WriteString("```swe_shell\nsubmit\n```\n\n")
	if req.WorkingDir != "" {
		fmt.Fprintf(&b, "Outer agent workspace: %s\n\n", req.WorkingDir)
	}
	if len(req.Tools) > 0 {
		b.WriteString("Outer agent tools:\n")
		for _, tool := range req.Tools {
			fmt.Fprintf(&b, "- %s: %s\n", tool.Name, tool.Description)
		}
		b.WriteByte('\n')
	}
	b.WriteString("Conversation so far:\n")
	for _, msg := range req.Messages {
		name := msg.Name
		if name != "" {
			name = " name=" + name
		}
		fmt.Fprintf(&b, "\n<%s%s>\n%s\n</%s>\n", msg.Role, name, msg.Content, msg.Role)
	}
	return b.String()
}
