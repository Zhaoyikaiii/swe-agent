package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultPathUsesSweAgentHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv(EnvHome, filepath.Join(home, ".custom-swe-agent"))
	t.Setenv(EnvConfigPath, "")

	want := filepath.Join(home, ".custom-swe-agent", "config.yaml")
	if got := DefaultPath(); got != want {
		t.Fatalf("DefaultPath() = %q, want %q", got, want)
	}
}

func TestDefaultPathUsesConfigEnv(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(EnvHome, "")
	t.Setenv(EnvConfigPath, "~/.swe-agent/custom.yaml")

	want := filepath.Join(home, ".swe-agent", "custom.yaml")
	if got := DefaultPath(); got != want {
		t.Fatalf("DefaultPath() = %q, want %q", got, want)
	}
}

func TestLoadExpandsTrajectoryDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SWE_AGENT_TRAJECTORIES", filepath.Join(home, "runs"))

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	data := []byte("trajectory:\n  dir: $SWE_AGENT_TRAJECTORIES\n")
	if err := os.WriteFile(configPath, data, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.Trajectory.Dir != filepath.Join(home, "runs") {
		t.Fatalf("Trajectory.Dir = %q", cfg.Trajectory.Dir)
	}
}
