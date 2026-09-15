// Package cursorparse folds the append-only cursor-store-v1 journal emitted by
// Loom's Cursor CLI transport into the shared session summary.
package cursorparse

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"loom/internal/parse/lens"
	"loom/internal/parse/summary"
)

const (
	journalFormat = "cursor-store-v1"
	textLimit     = 800
)

type journalRecord struct {
	Format    string `json:"format"`
	Kind      string `json:"kind"`
	Key       string `json:"key"`
	ValueType string `json:"value_type"`
	Value     string `json:"value"`
	Deleted   bool   `json:"deleted"`
}

type entry struct {
	data []byte
	line int
}

type cursorMeta struct {
	AgentID        string `json:"agentId"`
	LatestRootBlob string `json:"latestRootBlobId"`
	Name           string `json:"name"`
	Mode           string `json:"mode"`
	CreatedAt      int64  `json:"createdAt"`
	LastUsedModel  string `json:"lastUsedModel"`
	SubagentInfo   *struct {
		ParentAgentID     string `json:"parentAgentId"`
		RootParentAgentID string `json:"rootParentAgentId"`
		ToolCallID        string `json:"toolCallId"`
		TypeName          string `json:"typeName"`
	} `json:"subagentInfo"`
}

type fileMeta struct {
	SchemaVersion int    `json:"schemaVersion"`
	CreatedAt     int64  `json:"createdAtMs"`
	UpdatedAt     int64  `json:"updatedAtMs"`
	Cwd           string `json:"cwd"`
}

type jsonMessage struct {
	Role            string          `json:"role"`
	Content         json.RawMessage `json:"content"`
	ProviderOptions json.RawMessage `json:"providerOptions"`
}

type jsonPart struct {
	Type            string          `json:"type"`
	Text            string          `json:"text"`
	ToolCallID      string          `json:"toolCallId"`
	ToolName        string          `json:"toolName"`
	Args            json.RawMessage `json:"args"`
	Result          json.RawMessage `json:"result"`
	ProviderOptions json.RawMessage `json:"providerOptions"`
}

type callEvidence struct {
	name, keyArg, result          string
	isError                       bool
	line                          int
	hasArgs, hasResult, ambiguous bool
}

type state struct {
	s         *summary.SessionSummary
	blobs     map[string]entry
	metaRaw   []byte
	fileRaw   []byte
	unknown   map[string]*summary.UnknownRecord
	calls     map[string]callEvidence
	models    map[string][]string
	seenTurns map[string]bool
	seenSteps map[string]bool
}

// Parse consumes a cursor-store-v1 journal. The journal is replayed before
// references are followed, so repeated folding is idempotent and sees the
// same latest SQLite state the transport captured.
func Parse(r io.Reader) (*summary.SessionSummary, error) {
	st := &state{
		s:         &summary.SessionSummary{Agent: summary.AgentCursor, ModelProvider: "cursor"},
		blobs:     map[string]entry{},
		unknown:   map[string]*summary.UnknownRecord{},
		calls:     map[string]callEvidence{},
		models:    map[string][]string{},
		seenTurns: map[string]bool{},
		seenSteps: map[string]bool{},
	}
	if err := st.replay(r); err != nil {
		return nil, err
	}
	st.fold()
	st.finish()
	return st.s, nil
}

func (st *state) replay(r io.Reader) error {
	br := bufio.NewReader(r)
	for lineNo := 1; ; lineNo++ {
		line, err := br.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			st.feed(line, lineNo)
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func (st *state) feed(line []byte, lineNo int) {
	var rec journalRecord
	if err := json.Unmarshal(line, &rec); err != nil {
		st.addUnknown("journal", "malformed_json", time.Time{})
		return
	}
	if rec.Format != journalFormat {
		st.addUnknown("journal", "format:"+rec.Format, time.Time{})
		return
	}
	if rec.Kind != "blobs" && rec.Kind != "meta" && rec.Kind != "file" {
		st.addUnknown("journal", "kind:"+rec.Kind, time.Time{})
		return
	}
	if rec.Deleted || rec.ValueType == "null" {
		switch rec.Kind {
		case "blobs":
			delete(st.blobs, rec.Key)
		case "meta":
			if rec.Key == "0" {
				st.metaRaw = nil
			}
		case "file":
			if rec.Key == "meta.json" {
				st.fileRaw = nil
			}
		}
		return
	}
	if rec.ValueType != "blob" && rec.ValueType != "text" {
		st.addUnknown("journal", "value_type:"+rec.ValueType, time.Time{})
		return
	}
	data, err := base64.StdEncoding.DecodeString(rec.Value)
	if err != nil {
		st.addUnknown("journal", "invalid_base64", time.Time{})
		return
	}
	switch rec.Kind {
	case "blobs":
		if old, ok := st.blobs[rec.Key]; !ok || !bytes.Equal(old.data, data) {
			st.blobs[rec.Key] = entry{data: data, line: lineNo}
		}
	case "meta":
		if rec.Key == "0" {
			st.metaRaw = data
		} else {
			st.addUnknown("meta", "unmodeled_key", time.Time{})
		}
	case "file":
		if rec.Key == "meta.json" {
			st.fileRaw = data
		} else {
			st.addUnknown("file", "unmodeled_file", time.Time{})
		}
	}
}

func (st *state) fold() {
	var fm fileMeta
	if len(st.fileRaw) > 0 && json.Unmarshal(st.fileRaw, &fm) != nil {
		st.addUnknown("file", "meta.json", time.Time{})
	}
	if len(st.fileRaw) > 0 && fm.SchemaVersion != 1 {
		st.addUnknown("file", "meta_schema_version", time.Time{})
	}
	st.s.Cwd = fm.Cwd
	st.s.StartTime = millis(fm.CreatedAt)
	st.s.EndTime = millis(fm.UpdatedAt)

	var meta cursorMeta
	raw, err := hex.DecodeString(string(st.metaRaw))
	if err != nil || json.Unmarshal(raw, &meta) != nil {
		st.addUnknown("meta", "key_0", st.s.StartTime)
	} else {
		st.s.SessionID = meta.AgentID
		st.s.CustomTitle = meta.Name
		st.s.Personality = meta.Mode
		st.s.Model = meta.LastUsedModel
		if st.s.StartTime.IsZero() {
			st.s.StartTime = millis(meta.CreatedAt)
		}
		if meta.SubagentInfo != nil {
			st.s.ParentSessionID = meta.SubagentInfo.ParentAgentID
			st.s.ParentToolCallID = meta.SubagentInfo.ToolCallID
			if st.s.ParentSessionID != "" && st.s.ParentSessionID == meta.SubagentInfo.RootParentAgentID {
				st.s.SpawnDepth = 1
			}
			st.s.AgentName = meta.SubagentInfo.TypeName
		}
	}

	root, ok := st.blobs[meta.LatestRootBlob]
	if !ok {
		st.addUnknown("conversation", "missing_root_blob", st.s.StartTime)
		return
	}
	fields, ok := decodeWire(root.data)
	if !ok {
		st.addUnknown("protobuf", "conversation_state", st.s.StartTime)
		return
	}
	st.readJSONEvidence(fields)
	st.checkFields("conversation_state", fields, stateFields)
	if v := firstVarint(fields, 26); v != 0 {
		st.s.StartTime = millis(int64(v))
	}
	if v := firstString(fields, 19); v != "" {
		st.s.GitBranch = v
	}
	if v := firstString(fields, 22); v != "" && st.s.AgentName == "" {
		st.s.AgentName = v
	}
	if counts := fieldBytes(fields, 5); len(counts) > 0 {
		usage, valid := decodeWire(counts[0])
		if valid {
			st.addUnknown("usage", fmt.Sprintf("context_window_only:used=%d:max=%d", firstVarint(usage, 1), firstVarint(usage, 2)), st.s.StartTime)
		} else {
			st.addUnknown("protobuf", "token_details", st.s.StartTime)
		}
	}
	archives := map[string]bool{}
	for _, f := range fields {
		if (f.num == 11 || f.num == 13) && f.wire == 2 && len(f.data) > 0 {
			id := blobID(f.data)
			if !archives[id] {
				st.s.Compactions = append(st.s.Compactions, summary.Compaction{Anchor: id, UsageUnavailable: true})
				archives[id] = true
			}
		}
	}
	if n := firstVarint(fields, 37); n > 0 && len(st.s.Compactions) == 0 {
		st.s.Compactions = append(st.s.Compactions, summary.Compaction{UsageUnavailable: true, Anchor: fmt.Sprintf("message_count:%d", n)})
	}
	st.s.Compacted = len(st.s.Compactions) > 0

	for _, turnRef := range fieldBytes(fields, 8) {
		st.foldTurn(blobID(turnRef))
	}
}

// JSON model messages supply tool arguments and results. Only messages linked
// from the current root or a user-message snapshot are read; abandoned roots
// and compacted/requoted text must not create extra turns or lens responses.
func (st *state) readJSONEvidence(root []wireField) {
	seen := map[string]bool{}
	var visit func([]wireField)
	visit = func(fields []wireField) {
		request := ""
		for _, ref := range fieldBytes(fields, 1) {
			id := blobID(ref)
			e, ok := st.blobs[id]
			if !ok {
				st.addUnknown("reference", "model_message:"+id, st.s.StartTime)
				continue
			}
			var msg jsonMessage
			if json.Unmarshal(e.data, &msg) != nil {
				st.addUnknown("message", "invalid_json", st.s.StartTime)
				continue
			}
			var provider struct {
				Cursor struct {
					RequestID string `json:"requestId"`
				} `json:"cursor"`
			}
			if json.Unmarshal(msg.ProviderOptions, &provider) == nil && msg.Role == "user" && provider.Cursor.RequestID != "" {
				request = provider.Cursor.RequestID
			}
			var parts []jsonPart
			if len(msg.Content) > 0 && msg.Content[0] == '[' {
				if json.Unmarshal(msg.Content, &parts) != nil {
					st.addUnknown("message", "content", st.s.StartTime)
					continue
				}
			}
			if msg.Role == "assistant" {
				for _, part := range parts {
					st.readModel(request, part.ProviderOptions)
				}
				st.readModel(request, msg.ProviderOptions)
			}
			if seen[id] {
				continue
			}
			seen[id] = true
			switch msg.Role {
			case "system", "user":
				continue
			case "assistant", "tool":
			default:
				st.addUnknown("message", "role:"+msg.Role, st.s.StartTime)
				continue
			}
			for _, part := range parts {
				switch part.Type {
				case "reasoning", "text", "image":
				case "tool-call":
					if msg.Role != "assistant" || part.ToolCallID == "" {
						st.addUnknown("message", "unattributed_tool_call", st.s.StartTime)
						continue
					}
					ce := st.calls[part.ToolCallID]
					arg := keyArg(part.ToolName, part.Args)
					if ce.hasArgs && (ce.name != part.ToolName || ce.keyArg != arg) {
						ce.ambiguous = true
					}
					ce.name, ce.keyArg, ce.hasArgs = part.ToolName, arg, true
					st.calls[part.ToolCallID] = ce
				case "tool-result":
					if msg.Role != "tool" || part.ToolCallID == "" {
						st.addUnknown("message", "unattributed_tool_result", st.s.StartTime)
						continue
					}
					ce := st.calls[part.ToolCallID]
					result := rawText(part.Result)
					isError := resultIsError(msg.ProviderOptions) || resultIsError(part.ProviderOptions)
					if ce.hasResult && (ce.result != result || ce.isError != isError) {
						ce.ambiguous = true
					}
					if ce.name == "" {
						ce.name = part.ToolName
					}
					ce.result, ce.line, ce.hasResult, ce.isError = result, e.line, true, isError
					st.calls[part.ToolCallID] = ce
				default:
					st.addUnknown("message_content", part.Type, st.s.StartTime)
				}
			}
		}
	}
	visit(root)
	for _, field := range root {
		if field.num != 11 && field.num != 13 {
			continue
		}
		archive, ok := st.blobs[blobID(field.data)]
		if !ok {
			st.addUnknown("reference", "summary_archive:"+blobID(field.data), st.s.StartTime)
			continue
		}
		f, ok := decodeWire(archive.data)
		if !ok {
			st.addUnknown("protobuf", "summary_archive", st.s.StartTime)
			continue
		}
		visit(f)
	}
	// UserMessage field 10 is the conversation snapshot before that user
	// turn. These snapshots keep model messages reachable after compaction.
	for _, turnRef := range fieldBytes(root, 8) {
		turn, ok := st.blobs[blobID(turnRef)]
		if !ok {
			continue
		}
		tf, ok := decodeWire(turn.data)
		if !ok {
			continue
		}
		agent := fieldBytes(tf, 1)
		if len(agent) != 1 {
			continue
		}
		af, ok := decodeWire(agent[0])
		if !ok {
			continue
		}
		for _, userRef := range fieldBytes(af, 1) {
			user, ok := st.blobs[blobID(userRef)]
			if !ok {
				continue
			}
			uf, ok := decodeWire(user.data)
			if !ok {
				continue
			}
			for _, snapshotRef := range fieldBytes(uf, 10) {
				id := blobID(snapshotRef)
				snapshot, ok := st.blobs[id]
				if !ok {
					st.addUnknown("reference", "user_snapshot:"+id, st.s.StartTime)
					continue
				}
				sf, ok := decodeWire(snapshot.data)
				if ok {
					visit(sf)
				} else {
					st.addUnknown("protobuf", "user_snapshot:"+id, st.s.StartTime)
				}
			}
		}
	}
}

func (st *state) readModel(request string, raw json.RawMessage) {
	if request == "" || len(raw) == 0 {
		return
	}
	var x struct {
		Cursor struct {
			ModelName string `json:"modelName"`
		} `json:"cursor"`
	}
	if json.Unmarshal(raw, &x) != nil || x.Cursor.ModelName == "" {
		return
	}
	for _, have := range st.models[request] {
		if have == x.Cursor.ModelName {
			return
		}
	}
	st.models[request] = append(st.models[request], x.Cursor.ModelName)
}

func (st *state) foldTurn(id string) {
	if st.seenTurns[id] {
		st.addUnknown("reference", "duplicate_turn:"+id, st.s.StartTime)
		return
	}
	st.seenTurns[id] = true
	e, ok := st.blobs[id]
	if !ok {
		st.addUnknown("reference", "turn:"+id, st.s.StartTime)
		return
	}
	wrapper, ok := decodeWire(e.data)
	if !ok {
		st.addUnknown("protobuf", "conversation_turn", st.s.StartTime)
		return
	}
	st.checkFields("conversation_turn", wrapper, map[int]int{1: 2, 2: 2})
	payloads := fieldBytes(wrapper, 1)
	if len(payloads) == 0 {
		st.addUnknown("conversation_turn", "non_agent", st.s.StartTime)
		return
	}
	tf, ok := decodeWire(payloads[0])
	if !ok {
		st.addUnknown("protobuf", "agent_turn", st.s.StartTime)
		return
	}
	st.checkFields("agent_turn", tf, map[int]int{1: 2, 2: 2, 3: 2, 4: 2, 5: 0, 6: -1, 7: 2})
	t := summary.Turn{Idx: len(st.s.Turns), TurnID: id, Model: firstString(tf, 7)}
	st.seenSteps = map[string]bool{}
	if userRefs := fieldBytes(tf, 1); len(userRefs) > 0 {
		st.foldUser(&t, blobID(userRefs[0]))
	}
	for _, stepRef := range fieldBytes(tf, 2) {
		st.foldStep(&t, blobID(stepRef))
	}
	models := st.models[firstString(tf, 3)]
	if len(models) == 1 && t.Model == "" {
		t.Model = models[0]
	}
	if len(models) > 1 {
		t.Mixed = true
		st.addUnknown("model", "multiple_models_in_turn", t.StartedAt)
	}
	if st.s.Model == "" {
		st.s.Model = t.Model
	}
	st.s.Turns = append(st.s.Turns, t)
}

func (st *state) foldUser(t *summary.Turn, id string) {
	e, ok := st.blobs[id]
	if !ok {
		st.addUnknown("reference", "user_message:"+id, st.s.StartTime)
		return
	}
	f, ok := decodeWire(e.data)
	if !ok {
		st.addUnknown("protobuf", "user_message", st.s.StartTime)
		return
	}
	st.checkFields("user_message", f, userFields)
	t.UserMessage = firstString(f, 1)
	if t.UserMessage == "" {
		if refs := fieldBytes(f, 18); len(refs) > 0 {
			if text, ok := st.blobs[blobID(refs[0])]; ok {
				t.UserMessage = string(text.data)
			} else {
				st.addUnknown("reference", "user_text:"+blobID(refs[0]), st.s.StartTime)
			}
		}
	}
	if mid := firstString(f, 2); mid != "" {
		t.TurnID = mid
	}
	t.StartedAt = millis(int64(firstVarint(f, 25)))
	if t.StartedAt.IsZero() {
		t.StartedAt = millis(int64(firstVarint(f, 26)))
	}
	st.recordLenses(t.UserMessage, summary.OriginUser, "", e.line, t.Idx, t.StartedAt)
}

func (st *state) foldStep(t *summary.Turn, id string) {
	if st.seenSteps[id] {
		st.addUnknown("reference", "repeated_step:"+id, t.StartedAt)
		return
	}
	st.seenSteps[id] = true
	e, ok := st.blobs[id]
	if !ok {
		st.addUnknown("reference", "step:"+id, st.s.StartTime)
		return
	}
	f, ok := decodeWire(e.data)
	if !ok {
		st.addUnknown("protobuf", "conversation_step", st.s.StartTime)
		return
	}
	st.checkFields("conversation_step", f, map[int]int{1: 2, 2: 2, 3: 2})
	switch {
	case len(fieldBytes(f, 1)) > 0:
		m, ok := decodeWire(fieldBytes(f, 1)[0])
		if !ok {
			st.addUnknown("protobuf", "assistant_message", t.StartedAt)
			return
		}
		st.checkFields("assistant_message", m, map[int]int{1: 2, 2: 0, 3: 0})
		appendText(&t.AssistantText, firstString(m, 1))
		end := millis(int64(firstVarint(m, 3)))
		if end.IsZero() {
			end = millis(int64(firstVarint(m, 2)))
		}
		t.EndedAt = later(t.EndedAt, end)
		st.recordLenses(firstString(m, 1), summary.OriginAssistant, "", e.line, t.Idx, end)
	case len(fieldBytes(f, 3)) > 0:
		m, ok := decodeWire(fieldBytes(f, 3)[0])
		if !ok {
			st.addUnknown("protobuf", "thinking_message", t.StartedAt)
			return
		}
		st.checkFields("thinking_message", m, map[int]int{1: 2, 2: 0, 3: 0, 4: 0})
		text := firstString(m, 1)
		t.ReasoningPresent = true
		t.ReasoningChars += utf8.RuneCountInString(text)
		t.EndedAt = later(t.EndedAt, millis(int64(firstVarint(m, 4))))
	case len(fieldBytes(f, 2)) > 0:
		st.foldTool(t, fieldBytes(f, 2)[0])
	default:
		st.addUnknown("conversation_step", "oneof", t.StartedAt)
	}
}

func (st *state) foldTool(t *summary.Turn, raw []byte) {
	f, ok := decodeWire(raw)
	if !ok {
		st.addUnknown("protobuf", "tool_call", t.StartedAt)
		return
	}
	st.checkFields("tool_call", f, toolFields)
	if truncated := fieldBytes(f, 34); len(truncated) > 0 {
		tf, ok := decodeWire(truncated[0])
		if !ok {
			st.addUnknown("protobuf", "truncated_tool_call", t.StartedAt)
			return
		}
		refs := fieldBytes(tf, 1)
		if len(refs) == 0 {
			st.addUnknown("reference", "truncated_tool_call", t.StartedAt)
			return
		}
		st.foldStep(t, blobID(refs[0]))
		return
	}
	id := firstString(f, 57)
	ce := st.calls[id]
	if ce.ambiguous {
		st.addUnknown("tool_call", "ambiguous_json_evidence:"+id, t.StartedAt)
		ce = callEvidence{}
	}
	if !ce.hasArgs {
		st.addUnknown("tool_call", "arguments_unavailable:"+id, t.StartedAt)
	}
	if !ce.hasResult {
		st.addUnknown("tool_call", "result_unavailable:"+id, t.StartedAt)
	}
	name := ce.name
	var toolField int
	for _, field := range f {
		if field.num != 54 && field.num != 57 && field.num != 59 && field.num != 60 && len(field.data) > 0 {
			toolField = field.num
			break
		}
	}
	if name == "" {
		name = toolNames[toolField]
	}
	if name == "" {
		name = fmt.Sprintf("field_%d", toolField)
		st.addUnknown("tool_call", name, t.StartedAt)
	}
	start, end := millis(int64(firstVarint(f, 59))), millis(int64(firstVarint(f, 60)))
	duration := int64(0)
	durationUnavailable := start.IsZero() || end.IsZero() || end.Before(start)
	if !durationUnavailable {
		duration = end.Sub(start).Milliseconds()
	} else {
		st.addUnknown("tool_call", "duration_unavailable:"+id, t.StartedAt)
	}
	call := summary.ToolCall{TurnIdx: t.Idx, CallID: id, ToolName: name, Kind: classify(name), KeyArg: ce.keyArg, StartedAt: start, DurationMs: duration, DurationUnavailable: durationUnavailable, IsError: ce.isError, ResultSummary: truncate(ce.result)}
	if task := fieldBytes(f, 19); len(task) > 0 {
		st.foldTaskResult(&call, task[0])
	}
	st.s.ToolCalls = append(st.s.ToolCalls, call)
	if ce.isError {
		st.s.Errors = append(st.s.Errors, summary.ErrorEvent{TurnIdx: t.Idx, Source: "tool_error", Message: truncate(ce.result), Time: end})
	}
	if call.Kind == summary.KindRead || call.Kind == summary.KindEdit || call.Kind == summary.KindWrite || call.Kind == summary.KindPatchApply {
		op := string(call.Kind)
		if op == string(summary.KindPatchApply) {
			op = "patch"
		}
		st.touchFile(call.KeyArg, op)
	}
	t.EndedAt = later(t.EndedAt, end)
	if ce.hasResult {
		st.recordLenses(ce.result, summary.OriginToolResult, id, ce.line, t.Idx, end)
	}
}

func (st *state) foldTaskResult(call *summary.ToolCall, raw []byte) {
	// TaskToolCall.result -> TaskResult.success -> TaskSuccess. A background
	// acknowledgement may name the child without carrying a final duration.
	for _, step := range []struct {
		name   string
		fields map[int]int
		next   int
	}{
		{"task_tool_call", map[int]int{1: 2, 2: 2, 3: 2}, 2},
		{"task_result", map[int]int{1: 2, 2: 2}, 1},
		{"task_success", map[int]int{1: 2, 2: 2, 3: 0, 4: 0, 5: 2, 6: 0, 7: 2}, 0},
	} {
		f, ok := decodeWire(raw)
		if !ok {
			st.addUnknown("protobuf", step.name, call.StartedAt)
			return
		}
		st.checkFields(step.name, f, step.fields)
		if step.name == "task_tool_call" {
			if args := fieldBytes(f, 1); len(args) > 0 {
				a, ok := decodeWire(args[0])
				if !ok {
					st.addUnknown("protobuf", "task_args", call.StartedAt)
				} else {
					st.checkFields("task_args", a, map[int]int{1: 2, 2: 2, 3: 2, 4: 2, 5: 2, 6: 2, 7: 2, 8: 0, 9: 2, 10: 0, 11: 2})
					call.ChildResumeID = firstString(a, 5)
				}
			}
		}
		if step.next == 0 {
			call.ChildSessionID = firstString(f, 2)
			if call.ChildResumeID != "" && call.ChildSessionID != "" && call.ChildResumeID != call.ChildSessionID {
				st.addUnknown("task_result", "resume_identity_conflict:"+call.CallID, call.StartedAt)
			}
			for _, field := range f {
				if field.num == 4 && field.wire == 0 && field.value <= uint64(1<<63-1) {
					duration := int64(field.value)
					call.ChildDurationMs = &duration
				}
			}
			if call.ChildSessionID == "" {
				st.addUnknown("task_result", "child_identity_unavailable:"+call.CallID, call.StartedAt)
			}
			return
		}
		values := fieldBytes(f, step.next)
		if len(values) == 0 {
			return
		}
		raw = values[0]
	}
}

// The field catalog is pinned to Cursor CLI 2026.09.10-fd3934a's
// agent.v1 protobuf definitions. Unmodeled fields remain visible by number;
// payload bytes never become instructions or diagnostic text.
var stateFields = map[int]int{
	1: 2, 3: 2, 4: 2, 5: 2, 6: 2, 7: 2, 8: 2, 9: 2, 10: 0, 11: 2, 12: 2,
	13: 2, 14: 2, 15: 2, 16: 2, 17: 0, 18: 2, 19: 2, 20: 2, 21: 2, 22: 2,
	23: 2, 24: 2, 25: 2, 26: 0, 27: 2, 28: 2, 29: 2, 30: 2, 31: 2, 32: 2,
	33: 0, 34: 2, 35: 2, 36: 2, 37: 0,
}

var userFields = map[int]int{
	1: 2, 2: 2, 3: 2, 4: 0, 5: 0, 6: 2, 7: 0, 8: 2, 9: 0, 10: 2, 11: 2,
	13: 2, 14: 2, 15: 2, 16: 2, 17: 2, 18: 2, 19: 2, 21: 2, 22: 2, 23: 2,
	24: 0, 25: 0, 26: 0,
}

var toolFields = map[int]int{
	1: 2, 3: 2, 4: 2, 5: 2, 8: 2, 9: 2, 10: 2, 12: 2, 13: 2, 14: 2, 15: 2,
	16: 2, 17: 2, 18: 2, 19: 2, 20: 2, 21: 2, 22: 2, 23: 2, 24: 2, 25: 2,
	28: 2, 29: 2, 30: 2, 31: 2, 32: 2, 33: 2, 34: 2, 35: 2, 36: 2, 37: 2,
	38: 2, 39: 2, 40: 2, 41: 2, 42: 2, 43: 2, 44: 2, 45: 2, 46: 2, 48: 2,
	49: 2, 50: 2, 51: 2, 52: 2, 53: 2, 54: 2, 55: 2, 56: 2, 57: 2, 58: 2,
	59: 0, 60: 0, 61: 2, 62: 2, 63: 2, 64: 2, 65: 2, 66: 2, 67: 2, 68: 2,
	69: 2, 70: 2, 71: 2, 72: 2, 73: 2, 74: 2, 75: 2, 76: 2, 77: 2, 78: 2,
}

func (st *state) checkFields(kind string, fields []wireField, known map[int]int) {
	for _, field := range fields {
		wire, ok := known[field.num]
		if !ok {
			st.addUnknown(kind, fmt.Sprintf("field_%d", field.num), st.s.StartTime)
		} else if wire >= 0 && wire != field.wire {
			st.addUnknown(kind, fmt.Sprintf("field_%d_wire_%d", field.num, field.wire), st.s.StartTime)
		}
	}
}

var toolNames = map[int]string{
	1: "Shell", 3: "Delete", 4: "Glob", 5: "Grep", 8: "Read", 9: "UpdateTodos", 10: "ReadTodos",
	12: "Edit", 13: "LS", 14: "ReadLints", 15: "MCP", 16: "SemanticSearch", 17: "CreatePlan",
	18: "WebSearch", 19: "Task", 20: "ListMCPResources", 21: "ReadMCPResource", 22: "ApplyDiff",
	23: "AskQuestion", 24: "WebFetch", 25: "SwitchMode", 28: "Image", 30: "Computer", 31: "WriteStdin",
	37: "WebFetch", 42: "Await", 44: "GetMCPTools", 55: "SendMessage",
}

func classify(name string) summary.ToolKind {
	switch strings.ToLower(name) {
	case "shell", "bash":
		return summary.KindBash
	case "edit":
		return summary.KindEdit
	case "write", "delete":
		return summary.KindWrite
	case "read", "ls", "readlints", "readtodos":
		return summary.KindRead
	case "grep", "semanticsearch":
		return summary.KindGrep
	case "glob":
		return summary.KindGlob
	case "task", "sendmessage", "await":
		return summary.KindTask
	case "webfetch":
		return summary.KindWebFetch
	case "websearch":
		return summary.KindWebSearch
	case "mcp", "listmcpresources", "readmcpresource", "getmcptools":
		return summary.KindMCP
	case "applydiff":
		return summary.KindPatchApply
	case "createplan", "updatetodos":
		return summary.KindPlan
	default:
		return summary.KindOther
	}
}

func (st *state) touchFile(path, op string) {
	if path == "" {
		return
	}
	for i := range st.s.FilesTouched {
		if st.s.FilesTouched[i].Path == path && st.s.FilesTouched[i].Op == op {
			st.s.FilesTouched[i].Count++
			return
		}
	}
	st.s.FilesTouched = append(st.s.FilesTouched, summary.FileTouch{Path: path, Op: op, Count: 1})
}

func (st *state) recordLenses(text, origin, dispatch string, line, turn int, at time.Time) {
	for _, block := range lens.Extract(text) {
		st.s.LensResponses = append(st.s.LensResponses, summary.LensResponse{TurnIdx: turn, Origin: origin, DispatchID: dispatch, SourceLine: line, At: at, Block: block})
	}
}

func (st *state) addUnknown(typ, subtype string, at time.Time) {
	key := typ + "\x00" + subtype
	u := st.unknown[key]
	if u == nil {
		u = &summary.UnknownRecord{Agent: summary.AgentCursor, Type: typ, Subtype: subtype, FirstSeen: at}
		st.unknown[key] = u
	}
	u.Count++
}

func (st *state) finish() {
	for _, t := range st.s.Turns {
		st.s.StartTime = earlierNonzero(st.s.StartTime, t.StartedAt)
		st.s.EndTime = later(st.s.EndTime, t.EndedAt)
	}
	if st.s.EndTime.IsZero() {
		st.addUnknown("timing", "end_unavailable", st.s.StartTime)
	}
	keys := make([]string, 0, len(st.unknown))
	for key := range st.unknown {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		u := *st.unknown[key]
		if u.FirstSeen.IsZero() {
			u.FirstSeen = st.s.StartTime
		}
		st.s.Unknown = append(st.s.Unknown, u)
	}
	st.s.UsageUnavailable = true
}

type wireField struct {
	num, wire int
	value     uint64
	data      []byte
}

func decodeWire(b []byte) ([]wireField, bool) {
	var out []wireField
	for len(b) > 0 {
		key, n := readVarint(b)
		if n == 0 {
			return nil, false
		}
		b = b[n:]
		f := wireField{num: int(key >> 3), wire: int(key & 7)}
		if f.num == 0 || key>>3 > (1<<29)-1 {
			return nil, false
		}
		switch f.wire {
		case 0:
			f.value, n = readVarint(b)
			if n == 0 {
				return nil, false
			}
			b = b[n:]
		case 1:
			if len(b) < 8 {
				return nil, false
			}
			b = b[8:]
		case 2:
			var size uint64
			size, n = readVarint(b)
			if n == 0 || size > uint64(len(b)-n) {
				return nil, false
			}
			b = b[n:]
			f.data = append([]byte(nil), b[:size]...)
			b = b[size:]
		case 5:
			if len(b) < 4 {
				return nil, false
			}
			b = b[4:]
		default:
			return nil, false
		}
		out = append(out, f)
	}
	return out, true
}

func readVarint(b []byte) (uint64, int) {
	var x uint64
	for i, c := range b {
		if i == 10 || (i == 9 && c > 1) {
			return 0, 0
		}
		x |= uint64(c&0x7f) << (7 * i)
		if c < 0x80 {
			return x, i + 1
		}
	}
	return 0, 0
}

func fieldBytes(fs []wireField, n int) [][]byte {
	var out [][]byte
	for _, f := range fs {
		if f.num == n && f.wire == 2 {
			out = append(out, f.data)
		}
	}
	return out
}
func firstString(fs []wireField, n int) string {
	xs := fieldBytes(fs, n)
	if len(xs) > 0 {
		return string(xs[0])
	}
	return ""
}
func firstVarint(fs []wireField, n int) uint64 {
	for _, f := range fs {
		if f.num == n && f.wire == 0 {
			return f.value
		}
	}
	return 0
}
func blobID(b []byte) string {
	return hex.EncodeToString(b)
}
func millis(v int64) time.Time {
	if v <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(v).UTC()
}
func later(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}
func earlierNonzero(a, b time.Time) time.Time {
	if a.IsZero() || (!b.IsZero() && b.Before(a)) {
		return b
	}
	return a
}
func appendText(dst *string, text string) {
	if text == "" {
		return
	}
	if *dst != "" {
		*dst += "\n"
	}
	*dst += text
}
func truncate(s string) string {
	if utf8.RuneCountInString(s) <= textLimit {
		return s
	}
	r := []rune(s)
	return string(r[:textLimit]) + "…"
}

func rawText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return string(raw)
}

func keyArg(name string, raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) == nil {
		for _, key := range []string{"path", "file_path", "target_file", "command", "query", "description", "prompt", "url"} {
			if v, ok := m[key]; ok {
				var s string
				if json.Unmarshal(v, &s) == nil {
					return s
				}
			}
		}
	}
	return truncate(string(raw))
}

func resultIsError(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var x struct {
		Cursor struct {
			High struct {
				IsError bool `json:"isError"`
			} `json:"highLevelToolCallResult"`
		} `json:"cursor"`
	}
	return json.Unmarshal(raw, &x) == nil && x.Cursor.High.IsError
}
