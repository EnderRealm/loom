package codexparse

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

// Parse consumes a Codex CLI rollout JSONL stream and returns the normalized
// summary. Unknown record types or unknown payload subtypes are folded into
// Unknown rather than dropped.
func Parse(r io.Reader) (*summary.SessionSummary, error) {
	s := &summary.SessionSummary{Agent: summary.AgentCodex}
	st := newState(s)

	br := bufio.NewReader(r)
	lineNo := 0
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			lineNo++
			line = trimNewline(line)
			if len(line) > 0 {
				if perr := st.feed(line); perr != nil {
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
	st.finalize()
	return s, nil
}

func trimNewline(b []byte) []byte {
	if len(b) > 0 && b[len(b)-1] == '\n' {
		b = b[:len(b)-1]
	}
	if len(b) > 0 && b[len(b)-1] == '\r' {
		b = b[:len(b)-1]
	}
	return b
}

type state struct {
	s *summary.SessionSummary

	turnByID       map[string]int
	currentTurnIdx int

	toolCallByID map[string]int

	unknown map[string]*summary.UnknownRecord
}

func newState(s *summary.SessionSummary) *state {
	return &state{
		s:              s,
		turnByID:       map[string]int{},
		toolCallByID:   map[string]int{},
		unknown:        map[string]*summary.UnknownRecord{},
		currentTurnIdx: -1,
	}
}

// MalformedLineMarker is bumped into Unknown when a single line fails to
// decode. Parsing continues so partial corruption doesn't lose the rest of
// the session.
const MalformedLineMarker = "__malformed__"

func (st *state) feed(line []byte) error {
	var env envelope
	if err := json.Unmarshal(line, &env); err != nil {
		st.bumpUnknown(MalformedLineMarker, "", time.Time{})
		return nil
	}
	ts := parseTime(env.Timestamp)
	st.touchTimeRange(ts)

	switch env.Type {
	case "session_meta":
		return st.handleSessionMeta(env, ts)
	case "turn_context":
		return st.handleTurnContext(env, ts)
	case "response_item":
		return st.handleResponseItem(env, ts)
	case "event_msg":
		return st.handleEventMsg(env, ts)
	case "compacted":
		st.s.Compacted = true
		st.s.Compactions = append(st.s.Compactions, summary.Compaction{
			Time: ts, Anchor: "compacted",
		})
		return nil
	default:
		st.bumpUnknown(env.Type, "", ts)
		return nil
	}
}

func (st *state) handleSessionMeta(env envelope, ts time.Time) error {
	var p sessionMetaPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		return err
	}
	if st.s.SessionID == "" {
		st.s.SessionID = p.ID
	}
	if st.s.Cwd == "" {
		st.s.Cwd = p.Cwd
	}
	if st.s.CLIVersion == "" {
		st.s.CLIVersion = p.CLIVersion
	}
	if st.s.ModelProvider == "" {
		st.s.ModelProvider = p.ModelProvider
	}
	if st.s.GitBranch == "" {
		st.s.GitBranch = p.Git.Branch
	}
	if st.s.StartTime.IsZero() {
		st.s.StartTime = parseTime(p.Timestamp)
		if st.s.StartTime.IsZero() {
			st.s.StartTime = ts
		}
	}
	return nil
}

func (st *state) handleTurnContext(env envelope, ts time.Time) error {
	var p turnContextPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		return err
	}
	if st.s.Cwd == "" && p.Cwd != "" {
		st.s.Cwd = p.Cwd
	}
	if st.s.Model == "" {
		st.s.Model = p.Model
	}
	if st.s.Personality == "" {
		st.s.Personality = p.Personality
	}

	idx, ok := st.turnByID[p.TurnID]
	if !ok {
		idx = len(st.s.Turns)
		st.s.Turns = append(st.s.Turns, summary.Turn{
			Idx:       idx,
			TurnID:    p.TurnID,
			StartedAt: ts,
		})
		st.turnByID[p.TurnID] = idx
	}
	st.currentTurnIdx = idx
	return nil
}

func (st *state) handleResponseItem(env envelope, ts time.Time) error {
	var p responseItemPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		return err
	}
	turnIdx := st.currentTurnIdx

	switch p.Type {
	case "message":
		text := joinContent(p.Content)
		if turnIdx < 0 {
			turnIdx = st.openImplicitTurn(ts)
		}
		t := &st.s.Turns[turnIdx]
		switch p.Role {
		case "user":
			if t.UserMessage == "" {
				t.UserMessage = text
			}
		case "assistant":
			if t.AssistantText != "" {
				t.AssistantText += "\n"
			}
			t.AssistantText += text
			t.EndedAt = ts
		case "developer":
			// Developer-role messages are system prompts / instructions
			// injected by the harness — not user intent.
		}
	case "reasoning":
		if turnIdx >= 0 {
			t := &st.s.Turns[turnIdx]
			t.ReasoningPresent = true
			t.ReasoningChars += len(p.Summary) + len(joinContent(p.Content))
		}
	case "function_call":
		tc := summary.ToolCall{
			TurnIdx:   turnIdx,
			CallID:    p.CallID,
			ToolName:  p.Name,
			Kind:      classifyCodexFunction(p.Name),
			KeyArg:    extractFunctionKeyArg(p.Name, p.Arguments),
			StartedAt: ts,
		}
		st.s.ToolCalls = append(st.s.ToolCalls, tc)
		st.toolCallByID[p.CallID] = len(st.s.ToolCalls) - 1
		collectFilesFromFunctionCall(st.s, p.Name, p.Arguments)
	case "function_call_output":
		st.applyToolOutput(p.CallID, p.Output, ts)
	case "custom_tool_call":
		tc := summary.ToolCall{
			TurnIdx:   turnIdx,
			CallID:    p.CallID,
			ToolName:  p.Name,
			Kind:      summary.KindCustom,
			KeyArg:    truncate(p.Input, 200),
			StartedAt: ts,
		}
		st.s.ToolCalls = append(st.s.ToolCalls, tc)
		st.toolCallByID[p.CallID] = len(st.s.ToolCalls) - 1
	case "custom_tool_call_output":
		st.applyToolOutput(p.CallID, p.Output, ts)
	case "web_search_call":
		tc := summary.ToolCall{
			TurnIdx:   turnIdx,
			CallID:    "",
			ToolName:  "web_search",
			Kind:      summary.KindWebSearch,
			StartedAt: ts,
		}
		st.s.ToolCalls = append(st.s.ToolCalls, tc)
	default:
		st.bumpUnknown("response_item", p.Type, ts)
	}
	return nil
}

func (st *state) handleEventMsg(env envelope, ts time.Time) error {
	var p eventMsgPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		return err
	}
	turnIdx := st.currentTurnIdx
	if p.TurnID != "" {
		if idx, ok := st.turnByID[p.TurnID]; ok {
			turnIdx = idx
		}
	}

	switch p.Type {
	case "user_message":
		if turnIdx < 0 {
			turnIdx = st.openImplicitTurn(ts)
		}
		t := &st.s.Turns[turnIdx]
		if t.UserMessage == "" {
			t.UserMessage = p.Message
		}
	case "agent_message":
		if turnIdx >= 0 {
			t := &st.s.Turns[turnIdx]
			if t.AssistantText == "" {
				t.AssistantText = p.Message
			}
			t.EndedAt = ts
		}
	case "task_started":
		// Tracked implicitly via turn_context; nothing extra to record.
	case "task_complete":
		if turnIdx >= 0 {
			t := &st.s.Turns[turnIdx]
			t.CompletionStatus = summary.CompletionTaskComplete
			t.EndedAt = ts
			if t.AssistantText == "" {
				t.AssistantText = p.LastAgentMessage
			}
		}
	case "turn_aborted":
		if turnIdx >= 0 {
			t := &st.s.Turns[turnIdx]
			t.CompletionStatus = summary.CompletionAborted
			t.EndedAt = ts
		}
		st.s.Errors = append(st.s.Errors, summary.ErrorEvent{
			TurnIdx: turnIdx,
			Source:  "turn_aborted",
			Message: p.Reason,
			Time:    ts,
		})
	case "exec_command_end":
		st.applyExecCommandEnd(p, ts, turnIdx)
	case "patch_apply_end":
		st.applyPatchApplyEnd(p, ts, turnIdx)
	case "mcp_tool_call_end":
		st.applyMCPToolEnd(p, ts, turnIdx)
	case "web_search_end":
		// Web search start was already recorded via response_item;
		// nothing to update for now.
	case "context_compacted":
		st.s.Compacted = true
		st.s.Compactions = append(st.s.Compactions, summary.Compaction{
			Time: ts, Anchor: "context_compacted",
		})
	case "token_count":
		st.applyTokenCount(p, ts, turnIdx)
	case "agent_reasoning":
		if turnIdx >= 0 {
			t := &st.s.Turns[turnIdx]
			t.ReasoningPresent = true
			t.ReasoningChars += len(p.Message)
		}
	default:
		st.bumpUnknown("event_msg", p.Type, ts)
	}
	return nil
}

func (st *state) applyExecCommandEnd(p eventMsgPayload, ts time.Time,
	turnIdx int) {
	idx, ok := st.toolCallByID[p.CallID]
	if !ok {
		// Some exec_command_end events arrive without a prior function_call
		// (rare — emit as a synthesized tool call so the data isn't lost).
		st.s.ToolCalls = append(st.s.ToolCalls, summary.ToolCall{
			TurnIdx:  turnIdx,
			CallID:   p.CallID,
			Kind:     summary.KindBash,
			ToolName: "shell",
			KeyArg:   commandKeyArg(p.Command),
		})
		idx = len(st.s.ToolCalls) - 1
		st.toolCallByID[p.CallID] = idx
	}
	tc := &st.s.ToolCalls[idx]
	if tc.Kind == "" || tc.Kind == summary.KindFunction {
		tc.Kind = summary.KindBash
	}
	tc.ExitCode = p.ExitCode
	tc.IsError = p.ExitCode != nil && *p.ExitCode != 0
	tc.DurationMs = parseDurationMs(p.Duration)
	tc.ResultSummary = truncate(execResultSummary(p), 800)
	if tc.IsError {
		st.s.Errors = append(st.s.Errors, summary.ErrorEvent{
			TurnIdx: tc.TurnIdx,
			Source:  "exec_error",
			Message: tc.ResultSummary,
			Time:    ts,
		})
	}
}

func (st *state) applyPatchApplyEnd(p eventMsgPayload, ts time.Time,
	turnIdx int) {
	idx, ok := st.toolCallByID[p.CallID]
	if !ok {
		st.s.ToolCalls = append(st.s.ToolCalls, summary.ToolCall{
			TurnIdx:  turnIdx,
			CallID:   p.CallID,
			Kind:     summary.KindPatchApply,
			ToolName: "apply_patch",
		})
		idx = len(st.s.ToolCalls) - 1
		st.toolCallByID[p.CallID] = idx
	}
	tc := &st.s.ToolCalls[idx]
	tc.Kind = summary.KindPatchApply
	tc.IsError = !p.Success
	if p.Success {
		tc.ResultSummary = truncate(p.Stdout, 400)
	} else {
		tc.ResultSummary = truncate(p.Stderr, 400)
		st.s.Errors = append(st.s.Errors, summary.ErrorEvent{
			TurnIdx: tc.TurnIdx,
			Source:  "patch_error",
			Message: tc.ResultSummary,
			Time:    ts,
		})
	}
	for path, change := range p.Changes {
		op := changeOpKind(change)
		recordFile(st.s, path, op)
	}
}

func (st *state) applyMCPToolEnd(p eventMsgPayload, _ time.Time,
	turnIdx int) {
	idx, ok := st.toolCallByID[p.CallID]
	if !ok {
		st.s.ToolCalls = append(st.s.ToolCalls, summary.ToolCall{
			TurnIdx:  turnIdx,
			CallID:   p.CallID,
			Kind:     summary.KindMCP,
			ToolName: "mcp",
		})
		idx = len(st.s.ToolCalls) - 1
		st.toolCallByID[p.CallID] = idx
	}
	tc := &st.s.ToolCalls[idx]
	tc.Kind = summary.KindMCP
	tc.DurationMs = parseDurationMs(p.Duration)
	tc.ResultSummary = truncate(string(p.Result), 400)
}

func (st *state) applyTokenCount(p eventMsgPayload, ts time.Time,
	turnIdx int) {
	if p.Info == nil {
		return
	}
	last := p.Info.LastTokenUsage
	tc := summary.TokenCount{
		TurnIdx:   turnIdx,
		Time:      ts,
		Input:     last.InputTokens,
		Output:    last.OutputTokens,
		Cached:    last.CachedInputTokens,
		Reasoning: last.ReasoningOutputTokens,
	}
	if p.RateLimits != nil {
		tc.LimitID = p.RateLimits.LimitID
		if p.RateLimits.Primary != nil {
			tc.LimitUsedPercent = p.RateLimits.Primary.UsedPercent
		}
	}
	st.s.TokenCounts = append(st.s.TokenCounts, tc)
	// Roll up into per-turn and per-session aggregates.
	if turnIdx >= 0 {
		t := &st.s.Turns[turnIdx]
		t.InputTokens = last.InputTokens
		t.OutputTokens = last.OutputTokens
		t.CacheReadTokens = last.CachedInputTokens
	}
	st.s.InputTokens = p.Info.TotalTokenUsage.InputTokens
	st.s.OutputTokens = p.Info.TotalTokenUsage.OutputTokens
	st.s.CacheReadTokens = p.Info.TotalTokenUsage.CachedInputTokens
}

func (st *state) applyToolOutput(callID string, output json.RawMessage,
	ts time.Time) {
	idx, ok := st.toolCallByID[callID]
	if !ok {
		return
	}
	tc := &st.s.ToolCalls[idx]
	if tc.ResultSummary == "" {
		tc.ResultSummary = truncate(decodeFunctionOutput(output), 800)
	}
	if tc.DurationMs == 0 && !tc.StartedAt.IsZero() && !ts.IsZero() {
		tc.DurationMs = ts.Sub(tc.StartedAt).Milliseconds()
	}
}

func (st *state) openImplicitTurn(ts time.Time) int {
	idx := len(st.s.Turns)
	st.s.Turns = append(st.s.Turns, summary.Turn{
		Idx:       idx,
		StartedAt: ts,
	})
	st.currentTurnIdx = idx
	return idx
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

// bumpUnknown records a Codex record whose discriminator is not in the catalog.
// Counts accumulate in the map; finalize() materializes the slice exactly once
// so re-summarizing is reproducible. FirstSeen comes from the record's own
// timestamp (zero is acceptable: malformed lines have no timestamp to honor).
func (st *state) bumpUnknown(typ, sub string, ts time.Time) {
	key := typ + "::" + sub
	u := st.unknown[key]
	if u == nil {
		u = &summary.UnknownRecord{
			Agent:   summary.AgentCodex,
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

// finalize materializes the unknown-record map into st.s.Unknown in stable
// (Type, Subtype) order so two runs over the same input produce equal output.
func (st *state) finalize() {
	keys := make([]string, 0, len(st.unknown))
	for k := range st.unknown {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		st.s.Unknown = append(st.s.Unknown, *st.unknown[k])
	}
}

// classifyCodexFunction maps a function_call's name to a normalized ToolKind.
func classifyCodexFunction(name string) summary.ToolKind {
	switch name {
	case "shell", "exec", "run":
		return summary.KindBash
	case "apply_patch":
		return summary.KindPatchApply
	case "update_plan":
		return summary.KindPlan
	}
	if strings.HasPrefix(name, "mcp__") {
		return summary.KindMCP
	}
	return summary.KindFunction
}

// extractFunctionKeyArg pulls a representative arg from the JSON-string
// `arguments` blob. The function name is unused today but kept on the
// signature so per-tool prioritization can grow without a churn of callers.
func extractFunctionKeyArg(_, args string) string {
	if args == "" {
		return ""
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(args), &m); err != nil {
		return truncate(args, 200)
	}
	priorities := []string{"command", "file_path", "path", "query",
		"prompt", "url", "description", "explanation"}
	for _, k := range priorities {
		if v, ok := m[k]; ok {
			var s string
			if err := json.Unmarshal(v, &s); err == nil {
				return truncate(s, 200)
			}
			// arrays (e.g. `command: ["bash","-c","..."]`)
			var arr []string
			if err := json.Unmarshal(v, &arr); err == nil {
				return truncate(strings.Join(arr, " "), 200)
			}
		}
	}
	return ""
}

func collectFilesFromFunctionCall(s *summary.SessionSummary, name,
	args string) {
	if args == "" {
		return
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(args), &m); err != nil {
		return
	}
	for _, k := range []string{"file_path", "path"} {
		if v, ok := m[k]; ok {
			var p string
			if err := json.Unmarshal(v, &p); err == nil && p != "" {
				op := "edit"
				if name == "shell" {
					op = "exec"
				}
				recordFile(s, p, op)
				return
			}
		}
	}
}

func recordFile(s *summary.SessionSummary, path, op string) {
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

// changeOpKind classifies a patch change entry's `type` (add/update/delete).
func changeOpKind(raw json.RawMessage) string {
	var m struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return "patch"
	}
	switch m.Type {
	case "add":
		return "write"
	case "update":
		return "edit"
	case "delete":
		return "delete"
	}
	return "patch"
}

// commandKeyArg flattens the exec_command_end `command` field (which is
// either a string or a string array) into a single line.
func commandKeyArg(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return truncate(s, 200)
	}
	var arr []string
	if err := json.Unmarshal(raw, &arr); err == nil {
		return truncate(strings.Join(arr, " "), 200)
	}
	return ""
}

// parseDurationMs handles Codex's duration encoding, which is either a
// {secs, nanos} object or a numeric seconds value depending on subsystem.
func parseDurationMs(raw json.RawMessage) int64 {
	if len(raw) == 0 {
		return 0
	}
	var obj struct {
		Secs  int64 `json:"secs"`
		Nanos int64 `json:"nanos"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil &&
		(obj.Secs != 0 || obj.Nanos != 0) {
		return obj.Secs*1000 + obj.Nanos/1_000_000
	}
	var sec float64
	if err := json.Unmarshal(raw, &sec); err == nil {
		return int64(sec * 1000)
	}
	return 0
}

func execResultSummary(p eventMsgPayload) string {
	var parts []string
	if p.ExitCode != nil {
		parts = append(parts, fmt.Sprintf("exit=%d", *p.ExitCode))
	}
	if p.Stdout != "" {
		parts = append(parts, "stdout: "+truncate(p.Stdout, 400))
	}
	if p.Stderr != "" {
		parts = append(parts, "stderr: "+truncate(p.Stderr, 400))
	}
	return strings.Join(parts, "\n")
}

func decodeFunctionOutput(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return string(raw)
}

func joinContent(items []responseContentItem) string {
	var parts []string
	for _, c := range items {
		if c.Text != "" {
			parts = append(parts, c.Text)
		}
	}
	return strings.Join(parts, "\n")
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
