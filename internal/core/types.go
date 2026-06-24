package core

import (
	"context"
	"time"
)

type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

type Message struct {
	Role    Role              `json:"role" yaml:"role"`
	Content string            `json:"content" yaml:"content"`
	Name    string            `json:"name,omitempty" yaml:"name,omitempty"`
	Extra   map[string]string `json:"extra,omitempty" yaml:"extra,omitempty"`
}

type Task struct {
	Text string `json:"text" yaml:"text"`
	Repo string `json:"repo" yaml:"repo"`
}

type Usage struct {
	InputTokens  int     `json:"input_tokens" yaml:"input_tokens"`
	OutputTokens int     `json:"output_tokens" yaml:"output_tokens"`
	CostUSD      float64 `json:"cost_usd" yaml:"cost_usd"`
}

type ModelRequest struct {
	Messages    []Message  `json:"messages"`
	Tools       []ToolSpec `json:"tools,omitempty"`
	Temperature float64    `json:"temperature"`
	MaxTokens   int        `json:"max_tokens"`
	WorkingDir  string     `json:"working_dir,omitempty"`
}

type ModelResponse struct {
	Message      Message    `json:"message"`
	ToolCalls    []ToolCall `json:"tool_calls,omitempty"`
	Usage        Usage      `json:"usage"`
	FinishReason string     `json:"finish_reason,omitempty"`
}

type Model interface {
	Complete(ctx context.Context, req ModelRequest) (ModelResponse, error)
}

type ToolCall struct {
	ID   string         `json:"id,omitempty"`
	Name string         `json:"name"`
	Args map[string]any `json:"args,omitempty"`
}

type ToolSpec struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Schema      map[string]any `json:"schema,omitempty"`
}

type RiskLevel string

const (
	RiskRead   RiskLevel = "read"
	RiskWrite  RiskLevel = "write"
	RiskExec   RiskLevel = "exec"
	RiskDanger RiskLevel = "danger"
)

type ToolInput struct {
	Call          ToolCall
	Runtime       Runtime
	WorkspaceRoot string
	Timeout       time.Duration
}

type ToolResult struct {
	Output    string            `json:"output"`
	Code      int               `json:"code,omitempty"`
	TimedOut  bool              `json:"timed_out,omitempty"`
	Artifacts map[string]string `json:"artifacts,omitempty"`
	Metadata  map[string]any    `json:"metadata,omitempty"`
}

type Tool interface {
	Spec() ToolSpec
	Risk() RiskLevel
	Execute(ctx context.Context, input ToolInput) (ToolResult, error)
}

type ToolRegistry interface {
	List() []ToolSpec
	Get(name string) (Tool, bool)
}

type ExecRequest struct {
	Command string
	Cwd     string
	Env     map[string]string
	Timeout time.Duration
}

type ExecResult struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr,omitempty"`
	Code     int    `json:"code"`
	TimedOut bool   `json:"timed_out"`
}

type Runtime interface {
	Execute(ctx context.Context, req ExecRequest) (ExecResult, error)
	TemplateVars(ctx context.Context) map[string]string
	Close(ctx context.Context) error
}

type Decision struct {
	Allowed bool
	Reason  string
}

type Policy interface {
	AllowTool(ctx context.Context, call ToolCall, spec ToolSpec, risk RiskLevel) (Decision, error)
	FilterObservation(ctx context.Context, result ToolResult) ToolResult
	ValidateUserInput(ctx context.Context, input string) error
}

type Event struct {
	Type string         `json:"type"`
	Time time.Time      `json:"time"`
	Data map[string]any `json:"data,omitempty"`
}

type TrajectoryStore interface {
	Append(ctx context.Context, event Event) error
	Load(ctx context.Context) ([]Event, error)
	Path() string
}
