package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRunCommandMockSubmit(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	repo := t.TempDir()
	trajectoryDir := t.TempDir()
	configPath := filepath.Join(wd, "..", "..", "configs", "default.yaml")
	err = run([]string{"run", "--config", configPath, "--repo", repo, "--task", "finish", "--trajectory-dir", trajectoryDir, "--json"})
	if err != nil {
		t.Fatalf("run command returned error: %v", err)
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
