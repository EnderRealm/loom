// Package summary defines the agent-agnostic shape that both Claude Code and
// Codex CLI session transcripts collapse into. Per-agent parsers in sibling
// packages are responsible for the mapping; downstream code (storage, queries,
// extractors) only sees this shape.
package summary

import "time"

// Agent identifies the producer that generated a session.
type Agent string

const (
	AgentClaude Agent = "claude-code"
	AgentCodex  Agent = "codex-cli"
)

// SessionSummary is the normalized view of one session transcript.
type SessionSummary struct {
	SessionID string
	Agent     Agent
	Project   string
	Cwd       string
	GitBranch string

	CLIVersion    string
	ModelProvider string
	Model         string
	Personality   string

	CustomTitle string
	AgentName   string
	PRURL       string

	StartTime time.Time
	EndTime   time.Time

	InputTokens     int64
	OutputTokens    int64
	CacheReadTokens int64

	Compacted bool

	Turns         []Turn
	ToolCalls     []ToolCall
	Errors        []ErrorEvent
	Compactions   []Compaction
	TokenCounts   []TokenCount
	FilesTouched  []FileTouch
	Subagents     []Subagent
	Unknown       []UnknownRecord
}

// CompletionStatus normalizes how a turn ended across producers.
type CompletionStatus string

const (
	CompletionEndTurn      CompletionStatus = "end_turn"
	CompletionToolUse      CompletionStatus = "tool_use"
	CompletionStopSequence CompletionStatus = "stop_sequence"
	CompletionAborted      CompletionStatus = "aborted"
	CompletionTaskComplete CompletionStatus = "task_complete"
	CompletionUnknown      CompletionStatus = ""
)

// Turn is one user-prompt → assistant-response cycle. For Claude this is
// promptId-bounded; for Codex it follows turn_context records.
type Turn struct {
	Idx              int
	TurnID           string
	UserMessage      string
	AssistantText    string
	ReasoningPresent bool
	ReasoningChars   int
	StopReason       string
	CompletionStatus CompletionStatus

	InputTokens     int64
	OutputTokens    int64
	CacheReadTokens int64

	StartedAt time.Time
	EndedAt   time.Time
}

// ToolKind is the normalized category of a tool call. The original tool name
// is kept in ToolCall.ToolName so cross-agent queries can drill in.
type ToolKind string

const (
	KindBash       ToolKind = "bash"
	KindEdit       ToolKind = "edit"
	KindWrite      ToolKind = "write"
	KindRead       ToolKind = "read"
	KindGrep       ToolKind = "grep"
	KindGlob       ToolKind = "glob"
	KindTask       ToolKind = "task"
	KindWebFetch   ToolKind = "web_fetch"
	KindWebSearch  ToolKind = "web_search"
	KindMCP        ToolKind = "mcp"
	KindPatchApply ToolKind = "patch_apply"
	KindPlan       ToolKind = "plan"
	KindCustom     ToolKind = "custom"
	KindFunction   ToolKind = "function"
	KindOther      ToolKind = "other"
)

// ToolCall is a single tool invocation by the model.
type ToolCall struct {
	TurnIdx       int
	CallID        string
	Kind          ToolKind
	ToolName      string
	KeyArg        string
	StartedAt     time.Time
	DurationMs    int64
	ExitCode      *int
	IsError       bool
	ResultSummary string
}

// ErrorEvent is anything the producer flagged as a failure.
type ErrorEvent struct {
	TurnIdx int
	Source  string
	Message string
	Time    time.Time
}

// Compaction marks where conversation history was compacted.
type Compaction struct {
	Time         time.Time
	Anchor       string
	TokensBefore int64
	TokensAfter  int64
}

// TokenCount is a usage observation. Claude embeds usage on each assistant
// turn; Codex emits standalone token_count events with rate-limit info.
type TokenCount struct {
	TurnIdx          int
	Time             time.Time
	Input            int64
	Output           int64
	Cached           int64
	Reasoning        int64
	LimitID          string
	LimitUsedPercent float64
}

// FileTouch records a file the session interacted with.
type FileTouch struct {
	Path  string
	Op    string // read / edit / write / patch
	Count int
}

// Subagent is a Task / sidechain conversation.
type Subagent struct {
	ParentTurnIdx int
	AgentType     string
	Prompt        string
	ResultSummary string
	DurationMs    int64
	ErrorCount    int
}

// UnknownRecord is the drift alarm. Any record whose discriminator is not in
// the per-agent catalog lands here so we can see it before downstream code
// silently swallows it.
type UnknownRecord struct {
	Agent     Agent
	Type      string
	Subtype   string
	Count     int
	FirstSeen time.Time
}
