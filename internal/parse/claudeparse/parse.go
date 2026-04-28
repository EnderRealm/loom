package claudeparse

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"loom/internal/parse/summary"
)

// Parse consumes a Claude Code session JSONL stream and returns the
// normalized summary. Unknown record types are folded into Unknown rather
// than dropped or erroring — schema drift is observable, not fatal.
func Parse(r io.Reader) (*summary.SessionSummary, error) {
	s := &summary.SessionSummary{Agent: summary.AgentClaude}
	state := newState(s)

	br := bufio.NewReader(r)
	lineNo := 0
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			lineNo++
			line = trimNewline(line)
			if len(line) > 0 {
				if perr := state.feed(line); perr != nil {
					return nil, fmt.Errorf("line %d: %w", lineNo, perr)
				}
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	state.finalize()
	return s, nil
}

// MalformedLineMarker is bumped into Unknown when a single line fails to
// decode. Parsing continues so partial corruption doesn't lose the rest of
// the session.
const MalformedLineMarker = "__malformed__"

func trimNewline(b []byte) []byte {
	if len(b) > 0 && b[len(b)-1] == '\n' {
		b = b[:len(b)-1]
	}
	if len(b) > 0 && b[len(b)-1] == '\r' {
		b = b[:len(b)-1]
	}
	return b
}

// state holds parser-local indexes used while folding records into the
// summary. Most fields are accumulators we close out in finalize.
type state struct {
	s *summary.SessionSummary

	// turnByPromptID maps Claude's promptId to a turn index. Each new
	// promptId opens a new turn.
	turnByPromptID map[string]int
	currentTurnIdx int

	// toolCallByID lets tool-result records (delivered as user messages)
	// link back to the assistant's tool_use that started them.
	toolCallByID map[string]int

	// progressByToolUse aggregates progress records per toolUseID so we can
	// derive durations and counts without keeping each line.
	progressByToolUse map[string]*progressAccum

	unknown map[string]*summary.UnknownRecord
}

type progressAccum struct {
	first time.Time
	last  time.Time
	count int
	kind  string
}

func newState(s *summary.SessionSummary) *state {
	return &state{
		s:                 s,
		turnByPromptID:    map[string]int{},
		toolCallByID:      map[string]int{},
		progressByToolUse: map[string]*progressAccum{},
		unknown:           map[string]*summary.UnknownRecord{},
		currentTurnIdx:    -1,
	}
}

func (st *state) feed(line []byte) error {
	var probe struct {
		Type      string `json:"type"`
		Timestamp string `json:"timestamp"`
	}
	if err := json.Unmarshal(line, &probe); err != nil {
		// Receiver-level corruption (interleaved writes, truncated lines)
		// is expected at scale. Surface it as drift rather than aborting.
		st.bumpUnknown(MalformedLineMarker, "", time.Time{})
		return nil
	}
	switch probe.Type {
	case "user":
		return st.handleUser(line)
	case "assistant":
		return st.handleAssistant(line)
	case "system":
		return st.handleSystem(line)
	case "attachment":
		return st.handleAttachment(line)
	case "progress":
		return st.handleProgress(line)
	case "queue-operation":
		return st.handleQueueOperation(line)
	case "file-history-snapshot":
		return st.handleFileHistorySnapshot(line)
	case "permission-mode":
		return st.handlePermissionMode(line)
	case "agent-name":
		return st.handleAgentName(line)
	case "custom-title":
		return st.handleCustomTitle(line)
	case "last-prompt":
		return st.handleLastPrompt(line)
	case "pr-link":
		return st.handlePRLink(line)
	default:
		st.bumpUnknown(probe.Type, "", parseTime(probe.Timestamp))
		return nil
	}
}

func (st *state) handleUser(line []byte) error {
	var rec userRecord
	if err := json.Unmarshal(line, &rec); err != nil {
		return err
	}
	st.applyHeader(rec.header)

	ts := parseTime(rec.Timestamp)
	st.touchTimeRange(ts)

	// Tool-result carrier: link back to the tool_use that started it.
	if len(rec.ToolUseResult) > 0 {
		st.applyToolResult(rec, ts)
		return nil
	}

	// Sidechain (Task subagent) messages: track but don't open a top-level
	// turn for them.
	if rec.IsSidechain {
		return nil
	}

	// Meta / system-injected: not a real user prompt.
	if rec.IsMeta != nil && *rec.IsMeta {
		return nil
	}

	text := decodeUserContent(rec.Message.Content)
	if rec.PromptID != "" {
		idx, ok := st.turnByPromptID[rec.PromptID]
		if !ok {
			idx = st.openTurn(rec.PromptID, ts)
			st.turnByPromptID[rec.PromptID] = idx
		}
		t := &st.s.Turns[idx]
		if t.UserMessage == "" {
			t.UserMessage = text
		}
		if t.StartedAt.IsZero() {
			t.StartedAt = ts
		}
	} else if text != "" {
		idx := st.openTurn("", ts)
		st.s.Turns[idx].UserMessage = text
	}
	return nil
}

func (st *state) handleAssistant(line []byte) error {
	var rec assistantRecord
	if err := json.Unmarshal(line, &rec); err != nil {
		return err
	}
	st.applyHeader(rec.header)
	ts := parseTime(rec.Timestamp)
	st.touchTimeRange(ts)

	if rec.IsSidechain {
		// Subagent assistant turns roll up into the Subagent record at
		// finalize time; we don't surface them as top-level turns.
		return nil
	}

	if st.s.Model == "" && rec.Message.Model != "" {
		st.s.Model = rec.Message.Model
	}

	turnIdx := st.currentTurnIdx
	if turnIdx < 0 {
		turnIdx = st.openTurn("", ts)
	}
	t := &st.s.Turns[turnIdx]
	t.EndedAt = ts

	if rec.Message.StopReason != "" {
		t.StopReason = rec.Message.StopReason
		t.CompletionStatus = mapClaudeStopReason(rec.Message.StopReason)
	}

	for _, block := range rec.Message.Content {
		switch block.Type {
		case "text":
			if block.Text != "" {
				if t.AssistantText != "" {
					t.AssistantText += "\n"
				}
				t.AssistantText += block.Text
			}
		case "thinking":
			t.ReasoningPresent = true
			t.ReasoningChars += len(block.Thinking)
		case "tool_use":
			tc := summary.ToolCall{
				TurnIdx:   turnIdx,
				CallID:    block.ID,
				ToolName:  block.Name,
				Kind:      classifyClaudeTool(block.Name),
				KeyArg:    extractKeyArg(block.Name, block.Input),
				StartedAt: ts,
			}
			st.s.ToolCalls = append(st.s.ToolCalls, tc)
			st.toolCallByID[block.ID] = len(st.s.ToolCalls) - 1
			collectFilesTouched(st.s, block.Name, block.Input)
		}
	}

	if rec.Message.Usage != nil {
		u := rec.Message.Usage
		t.InputTokens += u.InputTokens
		t.OutputTokens += u.OutputTokens
		t.CacheReadTokens += u.CacheReadInputTokens
		st.s.InputTokens += u.InputTokens
		st.s.OutputTokens += u.OutputTokens
		st.s.CacheReadTokens += u.CacheReadInputTokens
		st.s.TokenCounts = append(st.s.TokenCounts, summary.TokenCount{
			TurnIdx: turnIdx,
			Time:    ts,
			Input:   u.InputTokens,
			Output:  u.OutputTokens,
			Cached:  u.CacheReadInputTokens,
		})
	}
	return nil
}

func (st *state) handleSystem(line []byte) error {
	var rec systemRecord
	if err := json.Unmarshal(line, &rec); err != nil {
		return err
	}
	st.applyHeader(rec.header)
	ts := parseTime(rec.Timestamp)
	st.touchTimeRange(ts)

	switch rec.Subtype {
	case "api_error":
		st.s.Errors = append(st.s.Errors, summary.ErrorEvent{
			TurnIdx: st.currentTurnIdx,
			Source:  "api_error",
			Message: rec.Content,
			Time:    ts,
		})
	case "compact_boundary":
		st.s.Compacted = true
		st.s.Compactions = append(st.s.Compactions, summary.Compaction{
			Time:   ts,
			Anchor: rec.LogicalParentUUID,
		})
	case "stop_hook_summary":
		// Captured as a soft signal — useful diagnostic, not strictly an error.
		st.s.Errors = append(st.s.Errors, summary.ErrorEvent{
			TurnIdx: st.currentTurnIdx,
			Source:  "stop_hook",
			Message: truncate(rec.Content, 1000),
			Time:    ts,
		})
	case "turn_duration", "bridge_status", "informational",
		"local_command", "away_summary":
		// Recognized but currently no-op at the summary level.
	default:
		st.bumpUnknown("system", rec.Subtype, ts)
	}
	return nil
}

func (st *state) handleAttachment(line []byte) error {
	// Unmarshal attachment.attachment.type via a minimal probe; the inner
	// payload shape varies by subtype.
	var probe struct {
		header
		Attachment struct {
			Type string `json:"type"`
		} `json:"attachment"`
	}
	if err := json.Unmarshal(line, &probe); err != nil {
		return err
	}
	st.applyHeader(probe.header)
	st.touchTimeRange(parseTime(probe.Timestamp))

	switch probe.Attachment.Type {
	case "deferred_tools_delta", "skill_listing", "diagnostics",
		"plan_mode", "edited_text_file", "task_reminder",
		"companion_intro", "date_change",
		"command_permissions", "hook_success", "queued_command":
		// All recognized; no-op at summary level for now. Could be wired
		// up later (e.g. plan_mode transitions are interesting).
	default:
		st.bumpUnknown("attachment", probe.Attachment.Type, parseTime(probe.Timestamp))
	}
	return nil
}

func (st *state) handleProgress(line []byte) error {
	var rec progressRecord
	if err := json.Unmarshal(line, &rec); err != nil {
		return err
	}
	st.applyHeader(rec.header)
	ts := parseTime(rec.Timestamp)
	st.touchTimeRange(ts)

	if rec.ToolUseID == "" {
		return nil
	}
	pa := st.progressByToolUse[rec.ToolUseID]
	if pa == nil {
		pa = &progressAccum{first: ts, kind: rec.Data.Type}
		st.progressByToolUse[rec.ToolUseID] = pa
	}
	pa.last = ts
	pa.count++
	if pa.kind == "" {
		pa.kind = rec.Data.Type
	}
	return nil
}

func (st *state) handleQueueOperation(line []byte) error {
	var rec queueOperationRecord
	if err := json.Unmarshal(line, &rec); err != nil {
		return err
	}
	if st.s.SessionID == "" && rec.SessionID != "" {
		st.s.SessionID = rec.SessionID
	}
	return nil
}

func (st *state) handleFileHistorySnapshot(line []byte) error {
	var rec fileHistorySnapshotRecord
	if err := json.Unmarshal(line, &rec); err != nil {
		return err
	}
	st.touchTimeRange(parseTime(rec.Snapshot.Timestamp))
	return nil
}

func (st *state) handlePermissionMode(line []byte) error {
	var rec permissionModeRecord
	if err := json.Unmarshal(line, &rec); err != nil {
		return err
	}
	if st.s.SessionID == "" {
		st.s.SessionID = rec.SessionID
	}
	return nil
}

func (st *state) handleAgentName(line []byte) error {
	var rec agentNameRecord
	if err := json.Unmarshal(line, &rec); err != nil {
		return err
	}
	if st.s.SessionID == "" {
		st.s.SessionID = rec.SessionID
	}
	st.s.AgentName = rec.AgentName
	return nil
}

func (st *state) handleCustomTitle(line []byte) error {
	var rec customTitleRecord
	if err := json.Unmarshal(line, &rec); err != nil {
		return err
	}
	if st.s.SessionID == "" {
		st.s.SessionID = rec.SessionID
	}
	st.s.CustomTitle = rec.CustomTitle
	return nil
}

func (st *state) handleLastPrompt(line []byte) error {
	var rec lastPromptRecord
	if err := json.Unmarshal(line, &rec); err != nil {
		return err
	}
	if st.s.SessionID == "" {
		st.s.SessionID = rec.SessionID
	}
	return nil
}

func (st *state) handlePRLink(line []byte) error {
	var rec prLinkRecord
	if err := json.Unmarshal(line, &rec); err != nil {
		return err
	}
	if st.s.SessionID == "" {
		st.s.SessionID = rec.SessionID
	}
	st.s.PRURL = rec.PRURL
	st.touchTimeRange(parseTime(rec.Timestamp))
	return nil
}

func (st *state) applyHeader(h header) {
	if st.s.SessionID == "" {
		st.s.SessionID = h.SessionID
	}
	if st.s.Cwd == "" {
		st.s.Cwd = h.Cwd
	}
	if st.s.GitBranch == "" {
		st.s.GitBranch = h.GitBranch
	}
	if st.s.CLIVersion == "" {
		st.s.CLIVersion = h.Version
	}
}

func (st *state) touchTimeRange(t time.Time) {
	if t.IsZero() {
		return
	}
	if st.s.StartTime.IsZero() || t.Before(st.s.StartTime) {
		st.s.StartTime = t
	}
	if t.After(st.s.EndTime) {
		st.s.EndTime = t
	}
}

func (st *state) openTurn(promptID string, ts time.Time) int {
	idx := len(st.s.Turns)
	st.s.Turns = append(st.s.Turns, summary.Turn{
		Idx:       idx,
		TurnID:    promptID,
		StartedAt: ts,
	})
	st.currentTurnIdx = idx
	return idx
}

func (st *state) applyToolResult(rec userRecord, ts time.Time) {
	// userMessage.content for a tool-result carrier is an array of blocks
	// with at least one tool_result block.
	var blocks []userContentBlock
	if err := json.Unmarshal(rec.Message.Content, &blocks); err != nil {
		return
	}
	for _, b := range blocks {
		if b.Type != "tool_result" {
			continue
		}
		idx, ok := st.toolCallByID[b.ToolUseID]
		if !ok {
			continue
		}
		tc := &st.s.ToolCalls[idx]
		tc.IsError = b.IsError
		tc.ResultSummary = truncate(decodeToolResultContent(b.Content), 800)
		if !ts.IsZero() && !tc.StartedAt.IsZero() {
			tc.DurationMs = ts.Sub(tc.StartedAt).Milliseconds()
		}
		if b.IsError {
			st.s.Errors = append(st.s.Errors, summary.ErrorEvent{
				TurnIdx: tc.TurnIdx,
				Source:  "tool_error",
				Message: tc.ResultSummary,
				Time:    ts,
			})
		}
	}
}

// bumpUnknown records a Claude record whose discriminator is not in the
// catalog. FirstSeen comes from the record's own timestamp so re-summarizing
// the same input produces identical output. Zero ts is acceptable for cases
// (malformed lines, top-level probe miss) where no timestamp is available.
func (st *state) bumpUnknown(typ, sub string, ts time.Time) {
	key := typ + "::" + sub
	u := st.unknown[key]
	if u == nil {
		u = &summary.UnknownRecord{
			Agent:   summary.AgentClaude,
			Type:    typ,
			Subtype: sub,
		}
		if !ts.IsZero() {
			u.FirstSeen = ts.UTC()
		}
		st.unknown[key] = u
	}
	u.Count++
}

func (st *state) finalize() {
	keys := make([]string, 0, len(st.unknown))
	for k := range st.unknown {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		st.s.Unknown = append(st.s.Unknown, *st.unknown[k])
	}
	// Backfill tool-call durations from progress streams when a result
	// carrier didn't land.
	for i := range st.s.ToolCalls {
		tc := &st.s.ToolCalls[i]
		if tc.DurationMs > 0 {
			continue
		}
		pa, ok := st.progressByToolUse[tc.CallID]
		if !ok || pa.count == 0 {
			continue
		}
		if !pa.last.IsZero() && !tc.StartedAt.IsZero() {
			tc.DurationMs = pa.last.Sub(tc.StartedAt).Milliseconds()
		}
	}
}

func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	return time.Time{}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func decodeUserContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var blocks []userContentBlock
	if err := json.Unmarshal(raw, &blocks); err == nil {
		var parts []string
		for _, b := range blocks {
			if b.Type == "text" && b.Text != "" {
				parts = append(parts, b.Text)
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

func decodeToolResultContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var blocks []userContentBlock
	if err := json.Unmarshal(raw, &blocks); err == nil {
		var parts []string
		for _, b := range blocks {
			if b.Text != "" {
				parts = append(parts, b.Text)
			}
		}
		return strings.Join(parts, "\n")
	}
	return string(raw)
}

// classifyClaudeTool maps a tool name to a normalized ToolKind. mcp__* names
// follow a stable convention we exploit; Task is special-cased; everything
// else falls into Other.
func classifyClaudeTool(name string) summary.ToolKind {
	switch name {
	case "Bash":
		return summary.KindBash
	case "Edit":
		return summary.KindEdit
	case "Write":
		return summary.KindWrite
	case "Read", "NotebookEdit":
		return summary.KindRead
	case "Grep":
		return summary.KindGrep
	case "Glob":
		return summary.KindGlob
	case "Task", "Agent":
		return summary.KindTask
	case "WebFetch":
		return summary.KindWebFetch
	case "WebSearch":
		return summary.KindWebSearch
	case "ExitPlanMode", "EnterPlanMode":
		return summary.KindPlan
	}
	if strings.HasPrefix(name, "mcp__") {
		return summary.KindMCP
	}
	return summary.KindOther
}

// extractKeyArg pulls the most identifying argument from a tool_use input
// payload, keyed off the tool name. Best-effort and bounded in size.
func extractKeyArg(name string, input json.RawMessage) string {
	if len(input) == 0 {
		return ""
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(input, &m); err != nil {
		return ""
	}
	keys := keyArgPriority(name)
	for _, k := range keys {
		if v, ok := m[k]; ok {
			var s string
			if err := json.Unmarshal(v, &s); err == nil {
				return truncate(s, 200)
			}
		}
	}
	return ""
}

func keyArgPriority(name string) []string {
	switch name {
	case "Bash":
		return []string{"command", "description"}
	case "Edit", "Write", "Read":
		return []string{"file_path"}
	case "Grep":
		return []string{"pattern"}
	case "Glob":
		return []string{"pattern"}
	case "Task", "Agent":
		return []string{"description", "subagent_type"}
	case "WebFetch", "WebSearch":
		return []string{"url", "query", "prompt"}
	}
	return []string{"file_path", "command", "pattern", "query", "url",
		"description"}
}

// collectFilesTouched updates the FilesTouched accumulator from a tool_use
// input. We aggregate at finalize-time elsewhere; here we just append.
func collectFilesTouched(s *summary.SessionSummary, name string,
	input json.RawMessage) {
	if len(input) == 0 {
		return
	}
	var op string
	switch name {
	case "Read":
		op = "read"
	case "Edit":
		op = "edit"
	case "Write":
		op = "write"
	default:
		return
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(input, &m); err != nil {
		return
	}
	v, ok := m["file_path"]
	if !ok {
		return
	}
	var path string
	if err := json.Unmarshal(v, &path); err != nil {
		return
	}
	for i := range s.FilesTouched {
		if s.FilesTouched[i].Path == path && s.FilesTouched[i].Op == op {
			s.FilesTouched[i].Count++
			return
		}
	}
	s.FilesTouched = append(s.FilesTouched, summary.FileTouch{
		Path: path, Op: op, Count: 1,
	})
}

func mapClaudeStopReason(r string) summary.CompletionStatus {
	switch r {
	case "end_turn":
		return summary.CompletionEndTurn
	case "tool_use":
		return summary.CompletionToolUse
	case "stop_sequence":
		return summary.CompletionStopSequence
	}
	return summary.CompletionUnknown
}
