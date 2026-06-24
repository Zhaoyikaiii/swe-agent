package agent

import "time"

type Config struct {
	Agent      AgentConfig      `yaml:"agent"`
	Model      ModelConfig      `yaml:"model"`
	Runtime    RuntimeConfig    `yaml:"runtime"`
	Tools      ToolsConfig      `yaml:"tools"`
	Policy     PolicyConfig     `yaml:"policy"`
	Trajectory TrajectoryConfig `yaml:"trajectory"`
}

type AgentConfig struct {
	MaxSteps        int     `yaml:"max_steps"`
	MaxCostUSD      float64 `yaml:"max_cost_usd"`
	WallTimeSeconds int     `yaml:"wall_time_seconds"`
	ActionMode      string  `yaml:"action_mode"`
	SystemPrompt    string  `yaml:"system_prompt"`
}

type ModelConfig struct {
	Provider       string   `yaml:"provider"`
	Model          string   `yaml:"model"`
	BaseURL        string   `yaml:"base_url"`
	APIKeyEnv      string   `yaml:"api_key_env"`
	Temperature    float64  `yaml:"temperature"`
	MaxTokens      int      `yaml:"max_tokens"`
	Command        string   `yaml:"command"`
	Profile        string   `yaml:"profile"`
	OSS            bool     `yaml:"oss"`
	LocalProvider  string   `yaml:"local_provider"`
	Sandbox        string   `yaml:"sandbox"`
	ApprovalPolicy string   `yaml:"approval_policy"`
	ExtraArgs      []string `yaml:"extra_args"`
}

type RuntimeConfig struct {
	Type           string            `yaml:"type"`
	TimeoutSeconds int               `yaml:"timeout_seconds"`
	Env            map[string]string `yaml:"env"`
}

type ToolsConfig struct {
	Enabled []string `yaml:"enabled"`
}

type PolicyConfig struct {
	AutoApproveRead  bool `yaml:"auto_approve_read"`
	AutoApproveWrite bool `yaml:"auto_approve_write"`
	AutoApproveExec  bool `yaml:"auto_approve_exec"`
}

type TrajectoryConfig struct {
	Dir string `yaml:"dir"`
}

func DefaultConfig() Config {
	return Config{
		Agent: AgentConfig{
			MaxSteps:        40,
			MaxCostUSD:      3,
			WallTimeSeconds: 3600,
			ActionMode:      "shell",
			SystemPrompt:    defaultSystemPrompt,
		},
		Model: ModelConfig{
			Provider:       "mock",
			Model:          "mock",
			APIKeyEnv:      "OPENAI_API_KEY",
			Temperature:    0.1,
			MaxTokens:      2048,
			Command:        "codex",
			Sandbox:        "read-only",
			ApprovalPolicy: "never",
		},
		Runtime: RuntimeConfig{
			Type:           "local",
			TimeoutSeconds: 60,
			Env: map[string]string{
				"PAGER":            "cat",
				"MANPAGER":         "cat",
				"PIP_PROGRESS_BAR": "off",
				"TQDM_DISABLE":     "1",
			},
		},
		Tools: ToolsConfig{
			Enabled: []string{"read_file", "grep", "list_files", "git_diff", "apply_patch", "shell", "run_tests", "submit"},
		},
		Policy: PolicyConfig{
			AutoApproveRead:  true,
			AutoApproveWrite: false,
			AutoApproveExec:  false,
		},
		Trajectory: TrajectoryConfig{Dir: "trajectories"},
	}
}

func (c Config) RuntimeTimeout() time.Duration {
	if c.Runtime.TimeoutSeconds <= 0 {
		return 60 * time.Second
	}
	return time.Duration(c.Runtime.TimeoutSeconds) * time.Second
}

func (c Config) WallTimeLimit() time.Duration {
	if c.Agent.WallTimeSeconds <= 0 {
		return 0
	}
	return time.Duration(c.Agent.WallTimeSeconds) * time.Second
}

const defaultSystemPrompt = `You are a software engineering agent working in a repository.

Solve the user's task by inspecting files, editing code, and running focused verification.
Use exactly one action per response.

To run shell commands, respond with a fenced code block:

` + "```swe_shell" + `
go test ./...
` + "```" + `

When the task is complete, call the submit tool or run:

` + "```swe_shell" + `
echo COMPLETE_TASK_AND_SUBMIT_FINAL_OUTPUT
` + "```" + `

Keep commands scoped to the repository. Prefer grep/read_file/apply_patch style actions over broad destructive shell commands.`
