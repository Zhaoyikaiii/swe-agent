package problemtrace

import "time"

type ProblemTrace struct {
	RunID      string                   `json:"run_id,omitempty"`
	TraceID    string                   `json:"trace_id,omitempty"`
	Problem    ProblemContext           `json:"problem"`
	Symptoms   []Symptom                `json:"symptoms,omitempty"`
	Directions []InvestigationDirection `json:"directions,omitempty"`
	Spans      []TraceSpan              `json:"spans,omitempty"`
	Links      []TraceLink              `json:"links,omitempty"`
	History    []TraceNode              `json:"history,omitempty"`
	Frontier   InvestigationFrontier    `json:"frontier"`
	Prompts    []PromptSnapshot         `json:"prompts,omitempty"`
	Memories   []MemoryUsage            `json:"memories,omitempty"`
	Cards      []MemoryCard             `json:"cards,omitempty"`
	Resource   TraceResource            `json:"resource,omitempty"`
	CreatedAt  time.Time                `json:"created_at,omitempty"`
	UpdatedAt  time.Time                `json:"updated_at,omitempty"`
}

type ProblemContext struct {
	UserTask      string            `json:"user_task,omitempty"`
	Repo          string            `json:"repo,omitempty"`
	ReproCommands []string          `json:"repro_commands,omitempty"`
	ErrorSummary  string            `json:"error_summary,omitempty"`
	Constraints   []string          `json:"constraints,omitempty"`
	Environment   map[string]string `json:"environment,omitempty"`
}

type Symptom struct {
	ID         string    `json:"id"`
	Kind       string    `json:"kind"`
	Summary    string    `json:"summary"`
	RawExcerpt string    `json:"raw_excerpt,omitempty"`
	ErrorType  string    `json:"error_type,omitempty"`
	Command    string    `json:"command,omitempty"`
	Files      []string  `json:"files,omitempty"`
	Packages   []string  `json:"packages,omitempty"`
	Symbols    []string  `json:"symbols,omitempty"`
	EventIDs   []int     `json:"event_ids,omitempty"`
	CreatedAt  time.Time `json:"created_at,omitempty"`
}

type DirectionStatus string

const (
	DirectionOpen      DirectionStatus = "open"
	DirectionActive    DirectionStatus = "active"
	DirectionSupported DirectionStatus = "supported"
	DirectionRefuted   DirectionStatus = "refuted"
	DirectionFixed     DirectionStatus = "fixed"
	DirectionBlocked   DirectionStatus = "blocked"
)

type InvestigationDirection struct {
	ID                 string          `json:"id"`
	Hypothesis         string          `json:"hypothesis"`
	Rationale          string          `json:"rationale,omitempty"`
	Status             DirectionStatus `json:"status"`
	Priority           int             `json:"priority,omitempty"`
	SupportingEvidence []Evidence      `json:"supporting_evidence,omitempty"`
	RefutingEvidence   []Evidence      `json:"refuting_evidence,omitempty"`
	NextActions        []NextAction    `json:"next_actions,omitempty"`
	ExpectedEvidence   []string        `json:"expected_evidence,omitempty"`
	MemoryIDs          []string        `json:"memory_ids,omitempty"`
	PromptIDs          []string        `json:"prompt_ids,omitempty"`
	ToolEventIDs       []int           `json:"tool_event_ids,omitempty"`
}

type EvidenceRelation string

const (
	EvidenceSupports EvidenceRelation = "supports"
	EvidenceRefutes  EvidenceRelation = "refutes"
	EvidenceNeutral  EvidenceRelation = "neutral"
)

type Evidence struct {
	ID           string           `json:"id"`
	Summary      string           `json:"summary"`
	Detail       string           `json:"detail,omitempty"`
	Relation     EvidenceRelation `json:"relation,omitempty"`
	Source       string           `json:"source,omitempty"`
	SourceSpanID string           `json:"source_span_id,omitempty"`
	EventIDs     []int            `json:"event_ids,omitempty"`
	CreatedAt    time.Time        `json:"created_at,omitempty"`
}

type NextAction struct {
	ID               string   `json:"id"`
	Action           string   `json:"action"`
	Tool             string   `json:"tool,omitempty"`
	Command          string   `json:"command,omitempty"`
	Rationale        string   `json:"rationale,omitempty"`
	ExpectedEvidence []string `json:"expected_evidence,omitempty"`
	DirectionID      string   `json:"direction_id,omitempty"`
	Priority         int      `json:"priority,omitempty"`
	EventIDs         []int    `json:"event_ids,omitempty"`
}

type InvestigationFrontier struct {
	ActiveDirectionID   string       `json:"active_direction_id,omitempty"`
	CandidateDirections []string     `json:"candidate_directions,omitempty"`
	OpenQuestions       []string     `json:"open_questions,omitempty"`
	RecommendedActions  []NextAction `json:"recommended_actions,omitempty"`
	StopConditions      []string     `json:"stop_conditions,omitempty"`
	Risks               []string     `json:"risks,omitempty"`
}

type TraceNode struct {
	ID          string    `json:"id"`
	ParentID    string    `json:"parent_id,omitempty"`
	Kind        string    `json:"kind"`
	Title       string    `json:"title"`
	Summary     string    `json:"summary,omitempty"`
	Status      string    `json:"status,omitempty"`
	EventIDs    []int     `json:"event_ids,omitempty"`
	DirectionID string    `json:"direction_id,omitempty"`
	PromptID    string    `json:"prompt_id,omitempty"`
	Time        time.Time `json:"time,omitempty"`
}

type PromptSnapshot struct {
	ID            string        `json:"id"`
	Step          int           `json:"step,omitempty"`
	Model         string        `json:"model,omitempty"`
	Blocks        []PromptBlock `json:"blocks,omitempty"`
	MessageCount  int           `json:"message_count,omitempty"`
	ToolCount     int           `json:"tool_count,omitempty"`
	InputTokens   int           `json:"input_tokens,omitempty"`
	TokenEstimate int           `json:"token_estimate,omitempty"`
	MemoryIDs     []string      `json:"memory_ids,omitempty"`
	DirectionIDs  []string      `json:"direction_ids,omitempty"`
	CreatedAt     time.Time     `json:"created_at,omitempty"`
}

type PromptBlock struct {
	Kind      string   `json:"kind,omitempty"`
	Title     string   `json:"title,omitempty"`
	Content   string   `json:"content,omitempty"`
	SourceIDs []string `json:"source_ids,omitempty"`
	Count     int      `json:"count,omitempty"`
	Included  bool     `json:"included,omitempty"`
	Summary   string   `json:"summary,omitempty"`
}

type MemoryUsage struct {
	ID           string    `json:"id"`
	Summary      string    `json:"summary"`
	Reason       string    `json:"reason,omitempty"`
	Similarity   float64   `json:"similarity,omitempty"`
	SourceRunID  string    `json:"source_run_id,omitempty"`
	Status       string    `json:"status,omitempty"`
	PromptIDs    []string  `json:"prompt_ids,omitempty"`
	DirectionIDs []string  `json:"direction_ids,omitempty"`
	CreatedAt    time.Time `json:"created_at,omitempty"`
}

type MemoryCard struct {
	ID           string    `json:"id"`
	Kind         string    `json:"kind"`
	Summary      string    `json:"summary"`
	ProblemSig   string    `json:"problem_sig,omitempty"`
	Evidence     []string  `json:"evidence,omitempty"`
	FixPattern   string    `json:"fix_pattern,omitempty"`
	Verification string    `json:"verification,omitempty"`
	Tags         []string  `json:"tags,omitempty"`
	SourceRunID  string    `json:"source_run_id,omitempty"`
	Status       string    `json:"status,omitempty"`
	CreatedAt    time.Time `json:"created_at,omitempty"`
}

type ChangeSet struct {
	Initialized     bool
	Symptoms        []Symptom
	Directions      []InvestigationDirection
	Evidence        []DirectionEvidence
	FrontierUpdated bool
	Prompts         []PromptSnapshot
	Cards           []MemoryCard
}

type DirectionEvidence struct {
	DirectionID string   `json:"direction_id"`
	Evidence    Evidence `json:"evidence"`
}

type TraceFlags struct {
	Recording bool `json:"recording,omitempty"`
	Sampled   bool `json:"sampled,omitempty"`
}

type TraceContext struct {
	TraceID          string     `json:"trace_id,omitempty"`
	SpanID           string     `json:"span_id,omitempty"`
	ParentSpanID     string     `json:"parent_span_id,omitempty"`
	DirectionID      string     `json:"direction_id,omitempty"`
	PromptSnapshotID string     `json:"prompt_snapshot_id,omitempty"`
	MemoryIDs        []string   `json:"memory_ids,omitempty"`
	Flags            TraceFlags `json:"flags,omitempty"`
}

type SpanStatus string

const (
	SpanStatusUnset     SpanStatus = "unset"
	SpanStatusOK        SpanStatus = "ok"
	SpanStatusError     SpanStatus = "error"
	SpanStatusCancelled SpanStatus = "cancelled"
	SpanStatusTimeout   SpanStatus = "timeout"
)

type TraceSpan struct {
	TraceID      string         `json:"trace_id,omitempty"`
	SpanID       string         `json:"span_id,omitempty"`
	ParentSpanID string         `json:"parent_span_id,omitempty"`
	Name         string         `json:"name,omitempty"`
	Kind         string         `json:"kind,omitempty"`
	StartTime    time.Time      `json:"start_time,omitempty"`
	EndTime      time.Time      `json:"end_time,omitempty"`
	Status       SpanStatus     `json:"status,omitempty"`
	Attributes   map[string]any `json:"attributes,omitempty"`
	Events       []TraceEvent   `json:"events,omitempty"`
	Links        []TraceLink    `json:"links,omitempty"`
	Resource     TraceResource  `json:"resource,omitempty"`
}

type TraceEvent struct {
	Name       string         `json:"name,omitempty"`
	Time       time.Time      `json:"time,omitempty"`
	Attributes map[string]any `json:"attributes,omitempty"`
	Body       string         `json:"body,omitempty"`
}

type TraceLink struct {
	TraceID    string         `json:"trace_id,omitempty"`
	SpanID     string         `json:"span_id,omitempty"`
	FromID     string         `json:"from_id,omitempty"`
	ToID       string         `json:"to_id,omitempty"`
	Kind       string         `json:"kind,omitempty"`
	Attributes map[string]any `json:"attributes,omitempty"`
}

type TraceResource struct {
	RepoPath      string `json:"repo_path,omitempty"`
	RepoLanguage  string `json:"repo_language,omitempty"`
	AgentVersion  string `json:"agent_version,omitempty"`
	Runtime       string `json:"runtime,omitempty"`
	ModelProvider string `json:"model_provider,omitempty"`
	Model         string `json:"model,omitempty"`
}
