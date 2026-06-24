package model

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	if err := cmd.Run(); err != nil {
		return core.ModelResponse{}, fmt.Errorf("codex exec failed: %w stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	contentBytes, err := os.ReadFile(outputPath)
	if err != nil {
		return core.ModelResponse{}, fmt.Errorf("read codex last message: %w stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	content := strings.TrimSpace(string(contentBytes))
	if content == "" {
		content = strings.TrimSpace(stdout.String())
	}
	if content == "" {
		return core.ModelResponse{}, fmt.Errorf("codex exec returned an empty message")
	}
	return core.ModelResponse{
		Message: core.Message{Role: core.RoleAssistant, Content: content},
		Usage: core.Usage{
			OutputTokens: len(content) / 4,
		},
		FinishReason: fmt.Sprintf("codex-cli elapsed=%s", time.Since(start).Truncate(time.Millisecond)),
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
	args = append(args, "exec", "--ephemeral", "--skip-git-repo-check", "--output-last-message", outputPath)
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
