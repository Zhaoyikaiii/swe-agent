package workspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type Workspace struct {
	root string
}

func New(root string) (*Workspace, error) {
	if root == "" {
		root = "."
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(abs); err != nil {
		return nil, err
	}
	return &Workspace{root: abs}, nil
}

func (w *Workspace) Root() string {
	return w.root
}

func (w *Workspace) Diff(ctx context.Context) (string, error) {
	if !w.isGitRepo(ctx) {
		return "", nil
	}
	cmd := exec.CommandContext(ctx, "git", "diff", "--", ".")
	cmd.Dir = w.root
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), nil
	}
	return string(out), nil
}

func (w *Workspace) Status(ctx context.Context) (string, error) {
	if !w.isGitRepo(ctx) {
		return "", nil
	}
	cmd := exec.CommandContext(ctx, "git", "status", "--short")
	cmd.Dir = w.root
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (w *Workspace) isGitRepo(ctx context.Context) bool {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--is-inside-work-tree")
	cmd.Dir = w.root
	out, err := cmd.CombinedOutput()
	return err == nil && strings.TrimSpace(string(out)) == "true"
}
