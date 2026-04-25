// Package codexparse decodes Codex CLI rollout JSONL transcripts and folds
// them into a summary.SessionSummary. Top-level types: session_meta,
// turn_context, response_item, event_msg, compacted. Discriminators are
// nested under .payload.type.
package codexparse

import "encoding/json"

// envelope is the universal wrapper around every Codex record.
type envelope struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

// sessionMetaPayload is the first record of every rollout.
type sessionMetaPayload struct {
	ID               string `json:"id"`
	Timestamp        string `json:"timestamp"`
	Cwd              string `json:"cwd"`
	Originator       string `json:"originator"`
	CLIVersion       string `json:"cli_version"`
	Source           string `json:"source"`
	ModelProvider    string `json:"model_provider"`
	BaseInstructions json.RawMessage `json:"base_instructions"`
	Git              struct {
		Branch         string `json:"branch"`
		CommitHash     string `json:"commit_hash"`
		RepositoryURL  string `json:"repository_url"`
	} `json:"git"`
}

// turnContextPayload starts a new model turn. Only the fields we currently
// consume are typed; the rest pass through unread.
type turnContextPayload struct {
	TurnID      string `json:"turn_id"`
	Cwd         string `json:"cwd"`
	Model       string `json:"model"`
	Personality string `json:"personality"`
}

// responseItemPayload covers all .payload.type variants under response_item.
type responseItemPayload struct {
	Type      string                `json:"type"`
	Role      string                `json:"role"`
	Content   []responseContentItem `json:"content"`
	Phase     string                `json:"phase"`
	Summary   json.RawMessage       `json:"summary"`
	Name      string                `json:"name"`
	Arguments string                `json:"arguments"`
	Input     string                `json:"input"`
	Output    json.RawMessage       `json:"output"`
	CallID    string                `json:"call_id"`
	Status    string                `json:"status"`
	Action    json.RawMessage       `json:"action"`
}

type responseContentItem struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// eventMsgPayload covers all .payload.type variants under event_msg.
type eventMsgPayload struct {
	Type    string `json:"type"`
	TurnID  string `json:"turn_id"`
	Message string `json:"message"`

	// exec_command_end
	CallID         string          `json:"call_id"`
	Command        json.RawMessage `json:"command"`
	Cwd            string          `json:"cwd"`
	Stdout         string          `json:"stdout"`
	Stderr         string          `json:"stderr"`
	ExitCode       *int            `json:"exit_code"`
	Duration       json.RawMessage `json:"duration"`
	StatusStr      string          `json:"status"`

	// patch_apply_end
	Success bool                       `json:"success"`
	Changes map[string]json.RawMessage `json:"changes"`

	// mcp_tool_call_end
	Invocation json.RawMessage `json:"invocation"`
	Result     json.RawMessage `json:"result"`

	// task_complete
	LastAgentMessage string `json:"last_agent_message"`

	// turn_aborted
	Reason string `json:"reason"`

	// web_search_end
	Query string `json:"query"`

	// token_count
	Info       *tokenCountInfo `json:"info"`
	RateLimits *rateLimits     `json:"rate_limits"`
}

type tokenCountInfo struct {
	TotalTokenUsage     tokenUsage `json:"total_token_usage"`
	LastTokenUsage      tokenUsage `json:"last_token_usage"`
	ModelContextWindow  int64      `json:"model_context_window"`
}

type tokenUsage struct {
	InputTokens           int64 `json:"input_tokens"`
	CachedInputTokens     int64 `json:"cached_input_tokens"`
	OutputTokens          int64 `json:"output_tokens"`
	ReasoningOutputTokens int64 `json:"reasoning_output_tokens"`
	TotalTokens           int64 `json:"total_tokens"`
}

type rateLimits struct {
	LimitID   string         `json:"limit_id"`
	LimitName string         `json:"limit_name"`
	Primary   *rateLimitInfo `json:"primary"`
	Secondary *rateLimitInfo `json:"secondary"`
	PlanType  string         `json:"plan_type"`
}

type rateLimitInfo struct {
	UsedPercent   float64 `json:"used_percent"`
	WindowMinutes int     `json:"window_minutes"`
	ResetsAt      int64   `json:"resets_at"`
}
