package config

import (
	"errors"
	"os"

	"github.com/local/swe-agent/internal/agent"
	"gopkg.in/yaml.v3"
)

func Load(path string) (agent.Config, error) {
	cfg := agent.DefaultConfig()
	if path == "" {
		return cfg, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return cfg, err
	}
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
