package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/local/swe-agent/internal/agent"
	"gopkg.in/yaml.v3"
)

const (
	EnvHome       = "SWE_AGENT_HOME"
	EnvConfigPath = "SWE_AGENT_CONFIG"
)

func DefaultDir() string {
	if dir := strings.TrimSpace(os.Getenv(EnvHome)); dir != "" {
		return ExpandPath(dir)
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ".swe-agent"
	}
	return filepath.Join(home, ".swe-agent")
}

func DefaultPath() string {
	if path := strings.TrimSpace(os.Getenv(EnvConfigPath)); path != "" {
		return ExpandPath(path)
	}
	return filepath.Join(DefaultDir(), "config.yaml")
}

func ExpandPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return path
	}
	path = os.Expand(path, func(key string) string {
		if value, ok := os.LookupEnv(key); ok {
			return value
		}
		return "$" + key
	})
	if path == "~" {
		home, err := os.UserHomeDir()
		if err == nil && home != "" {
			return home
		}
	}
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err == nil && home != "" {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

func Load(path string) (agent.Config, error) {
	cfg := agent.DefaultConfig()
	if path == "" {
		return cfg, nil
	}
	data, err := os.ReadFile(ExpandPath(path))
	if err != nil {
		return cfg, err
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return cfg, err
	}
	cfg.Trajectory.Dir = ExpandPath(cfg.Trajectory.Dir)
	return cfg, Validate(cfg)
}

func Validate(cfg agent.Config) error {
	if cfg.Agent.MaxSteps < 0 {
		return errors.New("agent.max_steps must be >= 0")
	}
	if cfg.Model.Provider == "" {
		return errors.New("model.provider is required")
	}
	if cfg.Runtime.Type == "" {
		return errors.New("runtime.type is required")
	}
	if cfg.Trajectory.Dir == "" {
		return errors.New("trajectory.dir is required")
	}
	return nil
}
