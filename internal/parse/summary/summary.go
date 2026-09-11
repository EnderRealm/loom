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

	Turns        []Turn
	ToolCalls    []ToolCall
	Errors       []ErrorEvent
	Compactions  []Compaction
	TokenCounts  []TokenCount
	FilesTouched []FileTouch
	Subagents    []Subagent
	Unknown      []UnknownRecord
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

	// Model, Effort and CLIVersion are the values in force for this turn:
	// on Claude the first the turn's assistant records carried, on Codex
	// the turn_context's model and effort plus session_meta's CLI version.
	// Empty when those carried none.
	Model      string
	Effort     string
	CLIVersion string

	InputTokens     int64
	OutputTokens    int64
	CacheReadTokens int64
	// CacheCreationTokens is the prompt-cache write count; CacheCreation1hTokens
	// is the part of it the transcript labelled 1-hour TTL, the remainder being
	// 5-minute or unlabelled. Kept apart because the two TTLs are priced
	// differently. Speed is usage.speed as recorded ("standard"/"fast"), empty
	// when absent. Claude only; Codex carries none of these.
	CacheCreationTokens   int64
	CacheCreation1hTokens int64
	Speed                 string
	// Mixed is true when the turn's records did not all agree on model and
	// speed: claudeparse sets it when a later assistant record carries a
	// non-empty model different from Model (the "<synthetic>" placeholder is
	// skipped, not a disagreement) or a non-empty speed different from
	// Speed. Such a turn's tokens have no single rate, so it is unpriceable.
	Mixed bool

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
	// DurationMs is the span of the dispatch's own sidechain records. Nil
	// when that span can't be resolved, so "not measured" stays distinct
	// from "returned instantly" — a background dispatch's tool_result lands
	// on acknowledgement, not completion.
	DurationMs *int64
	ErrorCount int
	// Usage is the dispatch's own token usage and model, read from its own
	// transcript. Nil when that transcript was not available or did not
	// parse, so "not measured" stays distinct from zero — the same reasoning
	// as DurationMs.
	Usage *SubagentUsage
	// ToolUseID is the dispatching tool_use id. Carried in-process for the
	// parent-turn join and for debugging; not persisted.
	ToolUseID string
}

// SubagentUsage is what one dispatch consumed, summed over its transcript's
// turns. Model and Speed are those of the first token-carrying turn: a
// dispatch is assumed to run at a single model and speed end to end. Mixed
// is true when that assumption failed — any turn is itself Mixed, or a later
// token-carrying turn disagrees with that pair (a turn with tokens but no
// Model beside one that has a Model is a disagreement: those tokens have no
// rate; a turn with no tokens never sets Model or Speed and counts neither
// way) — and a mixed dispatch is unpriceable rather than priced at any one
// pair.
type SubagentUsage struct {
	Model                 string
	Speed                 string
	Mixed                 bool
	InputTokens           int64
	OutputTokens          int64
	CacheReadTokens       int64
	CacheCreationTokens   int64
	CacheCreation1hTokens int64
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
