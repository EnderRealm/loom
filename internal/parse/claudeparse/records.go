// Package claudeparse decodes Claude Code session JSONL transcripts and folds
// them into a summary.SessionSummary. The catalog of record types this parser
// understands is documented in docs/claude-jsonl-records.md.
package claudeparse

import "encoding/json"

// header is the field set shared by most Claude Code records. Sparse-header
// metadata records (agent-name, custom-title, last-prompt, permission-mode,
// pr-link, file-history-snapshot) carry only a subset.
type header struct {
	UUID              string `json:"uuid"`
	ParentUUID        string `json:"parentUuid"`
	LogicalParentUUID string `json:"logicalParentUuid"`
	SessionID         string `json:"sessionId"`
	Timestamp         string `json:"timestamp"`
	Cwd               string `json:"cwd"`
	Version           string `json:"version"`
	GitBranch         string `json:"gitBranch"`
	UserType          string `json:"userType"`
	Entrypoint        string `json:"entrypoint"`
	IsSidechain       bool   `json:"isSidechain"`
	AgentID           string `json:"agentId"`
	Slug              string `json:"slug"`
	PromptID          string `json:"promptId"`
	RequestID         string `json:"requestId"`
}

// rawRecord is the discriminated union shape. We unmarshal into this once,
// inspect Type, then re-decode Data into the matching typed payload.
type rawRecord struct {
	Type string          `json:"type"`
	Raw  json.RawMessage `json:"-"`
}

// userRecord covers role=user messages and tool-result carriers.
type userRecord struct {
	header
	Message                 userMessage     `json:"message"`
	ToolUseResult           json.RawMessage `json:"toolUseResult"`
	IsMeta                  *bool           `json:"isMeta"`
	SourceToolAssistantUUID string          `json:"sourceToolAssistantUUID"`
}

type userMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// userContentBlock is one element when message.content is an array.
type userContentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

// assistantRecord is a model turn.
type assistantRecord struct {
	header
	Message assistantMessage `json:"message"`
}

type assistantMessage struct {
	ID         string                  `json:"id"`
	Role       string                  `json:"role"`
	Model      string                  `json:"model"`
	Content    []assistantContentBlock `json:"content"`
	StopReason string                  `json:"stop_reason"`
	Usage      *assistantUsage         `json:"usage"`
}

type assistantContentBlock struct {
	Type     string          `json:"type"`
	Text     string          `json:"text"`
	Thinking string          `json:"thinking"`
	ID       string          `json:"id"`
	Name     string          `json:"name"`
	Input    json.RawMessage `json:"input"`
}

type assistantUsage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
}

// systemRecord covers all system.* subtypes.
type systemRecord struct {
	header
	Subtype         string          `json:"subtype"`
	Level           string          `json:"level"`
	Content         string          `json:"content"`
	URL             string          `json:"url"`
	IsMeta          *bool           `json:"isMeta"`
	CompactMetadata json.RawMessage `json:"compactMetadata"`
}

// attachmentRecord is the side-channel payload carrier.
type attachmentRecord struct {
	header
	Attachment attachmentPayload `json:"attachment"`
}

type attachmentPayload struct {
	Type         string          `json:"type"`
	AddedNames   []string        `json:"addedNames"`
	RemovedNames []string        `json:"removedNames"`
	Raw          json.RawMessage `json:"-"`
}

// progressRecord streams output from a long-running tool.
type progressRecord struct {
	header
	ToolUseID       string          `json:"toolUseID"`
	ParentToolUseID string          `json:"parentToolUseID"`
	Data            progressData    `json:"data"`
	RawData         json.RawMessage `json:"-"`
}

type progressData struct {
	Type      string `json:"type"`
	HookEvent string `json:"hookEvent"`
	HookName  string `json:"hookName"`
	Command   string `json:"command"`
}

// queueOperationRecord is a task-queue event.
type queueOperationRecord struct {
	Type      string `json:"type"`
	SessionID string `json:"sessionId"`
	Operation string `json:"operation"`
	Timestamp string `json:"timestamp"`
	Content   string `json:"content"`
}

// fileHistorySnapshotRecord is sparse-headered.
type fileHistorySnapshotRecord struct {
	Type             string                  `json:"type"`
	MessageID        string                  `json:"messageId"`
	IsSnapshotUpdate bool                    `json:"isSnapshotUpdate"`
	Snapshot         fileHistorySnapshotBody `json:"snapshot"`
}

type fileHistorySnapshotBody struct {
	MessageID          string                     `json:"messageId"`
	Timestamp          string                     `json:"timestamp"`
	TrackedFileBackups map[string]json.RawMessage `json:"trackedFileBackups"`
}

// permissionModeRecord is sparse-headered.
type permissionModeRecord struct {
	Type           string `json:"type"`
	SessionID      string `json:"sessionId"`
	PermissionMode string `json:"permissionMode"`
}

// agentNameRecord is sparse-headered.
type agentNameRecord struct {
	Type      string `json:"type"`
	SessionID string `json:"sessionId"`
	AgentName string `json:"agentName"`
}

// customTitleRecord is sparse-headered.
type customTitleRecord struct {
	Type        string `json:"type"`
	SessionID   string `json:"sessionId"`
	CustomTitle string `json:"customTitle"`
}

// lastPromptRecord is sparse-headered.
type lastPromptRecord struct {
	Type       string `json:"type"`
	SessionID  string `json:"sessionId"`
	LastPrompt string `json:"lastPrompt"`
}

// prLinkRecord is sparse-headered.
type prLinkRecord struct {
	Type         string `json:"type"`
	SessionID    string `json:"sessionId"`
	Timestamp    string `json:"timestamp"`
	PRNumber     int    `json:"prNumber"`
	PRURL        string `json:"prUrl"`
	PRRepository string `json:"prRepository"`
}
