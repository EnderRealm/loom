package cursorparse

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"loom/internal/parse/summary"
	"loom/internal/runreport"
	"loom/internal/summaries"
)

func mutateBlob(t *testing.T, raw []byte, change func([]byte) ([]byte, bool)) []byte {
	t.Helper()
	var out bytes.Buffer
	changed := false
	for _, line := range bytes.Split(raw, []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var row journalRecord
		if err := json.Unmarshal(line, &row); err != nil {
			t.Fatal(err)
		}
		if row.Kind == "blobs" && !changed {
			data, err := base64.StdEncoding.DecodeString(row.Value)
			if err != nil {
				t.Fatal(err)
			}
			if data, ok := change(data); ok {
				row.Value = base64.StdEncoding.EncodeToString(data)
				line, err = json.Marshal(row)
				if err != nil {
					t.Fatal(err)
				}
				changed = true
			}
		}
		out.Write(line)
		out.WriteByte('\n')
	}
	if !changed {
		t.Fatal("fixture mutation matched no blob")
	}
	return out.Bytes()
}

func TestTaskResumeIdentityIsPersisted(t *testing.T) {
	raw, err := os.ReadFile("testdata/parent.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	const child = "5aabb54a-05c7-403c-9721-1bf0448847c1"
	encode := func(fields []wireField) []byte {
		var out []byte
		for _, field := range fields {
			out = binary.AppendUvarint(out, uint64(field.num<<3|field.wire))
			switch field.wire {
			case 0:
				out = binary.AppendUvarint(out, field.value)
			case 2:
				out = binary.AppendUvarint(out, uint64(len(field.data)))
				out = append(out, field.data...)
			default:
				t.Fatalf("unexpected fixture wire %d", field.wire)
			}
		}
		return out
	}
	raw = mutateBlob(t, raw, func(data []byte) ([]byte, bool) {
		step, ok := decodeWire(data)
		if !ok || len(fieldBytes(step, 2)) != 1 {
			return nil, false
		}
		tool, ok := decodeWire(fieldBytes(step, 2)[0])
		if !ok || len(fieldBytes(tool, 19)) != 1 {
			return nil, false
		}
		task, _ := decodeWire(fieldBytes(tool, 19)[0])
		for i := range task {
			if task[i].num == 1 {
				args, _ := decodeWire(task[i].data)
				args = append(args, wireField{num: 5, wire: 2, data: []byte(child)})
				task[i].data = encode(args)
			}
		}
		for i := range tool {
			if tool[i].num == 19 {
				tool[i].data = encode(task)
			}
		}
		for i := range step {
			if step[i].num == 2 {
				step[i].data = encode(tool)
			}
		}
		return encode(step), true
	})
	s, err := Parse(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if s.ToolCalls[1].ChildResumeID != child || s.ToolCalls[1].ChildSessionID != child {
		t.Fatalf("Task resume = %+v", s.ToolCalls[1])
	}
	st, err := summaries.Open(filepath.Join(t.TempDir(), "summaries.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.WriteSummary(context.Background(), s, summaries.SourceInfo{}); err != nil {
		t.Fatal(err)
	}
	var resume string
	if err := st.DB().QueryRow(`SELECT child_resume_id FROM tool_calls WHERE agent = 'cursor-cli' AND session_id = ? AND call_id = ?`, s.SessionID, s.ToolCalls[1].CallID).Scan(&resume); err != nil || resume != child {
		t.Fatalf("stored resume = %q, %v", resume, err)
	}
}

func TestUnknownToolShapesWithJSONEvidence(t *testing.T) {
	raw, err := os.ReadFile("testdata/parent.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	for _, shape := range []string{"variant", "additional_field", "wrong_wire"} {
		t.Run(shape, func(t *testing.T) {
			mutated := mutateBlob(t, raw, func(data []byte) ([]byte, bool) {
				step, ok := decodeWire(data)
				if !ok || len(fieldBytes(step, 2)) != 1 {
					return nil, false
				}
				tool, ok := decodeWire(fieldBytes(step, 2)[0])
				if !ok || len(fieldBytes(tool, 8)) == 0 || firstString(tool, 57) == "" {
					return nil, false
				}
				var inner []byte
				for _, field := range tool {
					if field.num == 8 && shape == "variant" {
						field.num = 99
					}
					inner = binary.AppendUvarint(inner, uint64(field.num<<3|field.wire))
					if field.wire == 0 {
						inner = binary.AppendUvarint(inner, field.value)
					} else if field.wire == 2 {
						inner = binary.AppendUvarint(inner, uint64(len(field.data)))
						inner = append(inner, field.data...)
					} else {
						t.Fatalf("unexpected fixture wire %d", field.wire)
					}
				}
				if shape == "additional_field" {
					inner = binary.AppendUvarint(inner, 99<<3)
					inner = append(inner, 1)
				} else if shape == "wrong_wire" {
					inner = binary.AppendUvarint(inner, 59<<3|2)
					inner = append(inner, 1, 'x')
				}
				out := binary.AppendUvarint([]byte{2<<3 | 2}, uint64(len(inner)))
				return append(out, inner...), true
			})
			s, err := Parse(bytes.NewReader(mutated))
			if err != nil {
				t.Fatal(err)
			}
			if s.ToolCalls[0].ToolName != "Read" || s.ToolCalls[0].ResultSummary == "" {
				t.Fatal("usable JSON evidence lost")
			}
			found := false
			for _, u := range s.Unknown {
				found = found || (u.Type == "tool_call" && strings.HasPrefix(u.Subtype, "field_"))
			}
			if !found {
				t.Fatalf("%s shape was not diagnosed: %+v", shape, s.Unknown)
			}
		})
	}
}

func TestInitialChildHasNoObservedEnd(t *testing.T) {
	raw, err := os.ReadFile("testdata/child.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	initial := mutateBlob(t, raw, func(data []byte) ([]byte, bool) {
		wrapper, ok := decodeWire(data)
		if !ok || len(fieldBytes(wrapper, 1)) != 1 {
			return nil, false
		}
		turn, ok := decodeWire(fieldBytes(wrapper, 1)[0])
		refs := fieldBytes(turn, 1)
		if !ok || len(refs) != 1 || len(refs[0]) != 32 || firstString(turn, 3) == "" || len(fieldBytes(turn, 2)) == 0 {
			return nil, false
		}
		// Retain the initial prompt, before any response steps are recorded.
		payload := append([]byte{1<<3 | 2, 32}, refs[0]...)
		out := binary.AppendUvarint([]byte{1<<3 | 2}, uint64(len(payload)))
		return append(out, payload...), true
	})
	deleted, _ := json.Marshal(journalRecord{Format: journalFormat, Kind: "file", Key: "meta.json", Deleted: true})
	initial = append(append(initial, deleted...), '\n')
	child, err := Parse(bytes.NewReader(initial))
	if err != nil {
		t.Fatal(err)
	}
	if child.StartTime.IsZero() || !child.EndTime.IsZero() || len(child.Turns) != 1 || child.Turns[0].UserMessage == "" || len(child.ToolCalls) != 0 {
		t.Errorf("initial child fabricated end or lost prompt: start=%v end=%v turns=%d tools=%d", child.StartTime, child.EndTime, len(child.Turns), len(child.ToolCalls))
	}
	parentRaw, err := os.ReadFile("testdata/parent.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	parent, err := Parse(bytes.NewReader(parentRaw))
	if err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(t.TempDir(), "summaries.db")
	st, err := summaries.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, s := range []*summary.SessionSummary{parent, child} {
		if err := st.WriteSummary(context.Background(), s, summaries.SourceInfo{}); err != nil {
			t.Fatal(err)
		}
	}
	report, err := runreport.Load(dbPath, "transcript:cursor-cli:"+parent.SessionID+":0")
	if err != nil {
		t.Fatal(err)
	}
	c := report.Executions[1]
	if c.EndedAt != "" || c.DurationMs == nil || *c.DurationMs != 5725 || !strings.Contains(strings.Join(report.Telemetry.ExecutionsPending, "\n"), c.ExecutionID) {
		t.Errorf("initial child lost observed dispatch duration or pending state: %+v", c)
	}
	if !strings.Contains(strings.Join(report.Telemetry.Gaps, "\n"), "timing:end_unavailable") {
		t.Error("unavailable child end was not diagnosed")
	}
}

func TestUntimedInvocationKeepsDisjointParentMetrics(t *testing.T) {
	raw, err := os.ReadFile("testdata/parent.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := Parse(bytes.NewReader(raw))
	if err != nil || len(baseline.Turns) != 2 {
		t.Fatalf("fixture turns: %v", err)
	}
	target := baseline.Turns[1].UserMessage
	raw = mutateBlob(t, raw, func(data []byte) ([]byte, bool) {
		fields, ok := decodeWire(data)
		if !ok || firstString(fields, 1) != target {
			return nil, false
		}
		var out []byte
		for _, field := range fields {
			if field.num == 25 || field.num == 26 {
				continue
			}
			out = binary.AppendUvarint(out, uint64(field.num<<3|field.wire))
			switch field.wire {
			case 0:
				out = binary.AppendUvarint(out, field.value)
			case 2:
				out = binary.AppendUvarint(out, uint64(len(field.data)))
				out = append(out, field.data...)
			default:
				t.Fatalf("unexpected fixture wire %d", field.wire)
			}
		}
		return out, true
	})
	s, err := Parse(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if !s.Turns[1].StartedAt.IsZero() {
		t.Fatal("timestamp removal did not leave an untimed invocation")
	}
	dbPath := filepath.Join(t.TempDir(), "summaries.db")
	st, err := summaries.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.WriteSummary(context.Background(), s, summaries.SourceInfo{}); err != nil {
		t.Fatal(err)
	}
	for _, declared := range []bool{true, false} {
		runID := fmt.Sprintf("untimed-boundary-%t", declared)
		start := baseline.Turns[1].StartedAt.Format(time.RFC3339Nano)
		record := map[string]any{
			"v": 1, "kind": "run", "run_id": runID, "runtime": "cursor-cli",
			"ticket": "loom/cursor-first-0001", "started_at": start,
		}
		if declared {
			record["agent"], record["session_id"] = "cursor-cli", s.SessionID
		}
		var registry bytes.Buffer
		enc := json.NewEncoder(&registry)
		if err := enc.Encode(record); err != nil {
			t.Fatal(err)
		}
		if err := enc.Encode(map[string]any{
			"v": 1, "kind": "execution", "run_id": runID,
			"execution_id": runID + "-root", "execution_kind": "root",
			"agent": "cursor-cli", "session_id": s.SessionID, "started_at": start,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := st.ImportExecutions(context.Background(), runID, bytes.NewReader(registry.Bytes()), int64(registry.Len()), time.Now()); err != nil {
			t.Fatal(err)
		}
		report, err := runreport.Load(dbPath, runID)
		if err != nil {
			t.Fatal(err)
		}
		if report.Telemetry.RootSpan != runreport.SpanUnresolved || report.Metrics.Total.Turns != 0 || report.Metrics.Total.ToolCalls != 0 {
			t.Errorf("declared=%t: ambiguous explicit run owns span=%s turns=%d calls=%d", declared, report.Telemetry.RootSpan, report.Metrics.Total.Turns, report.Metrics.Total.ToolCalls)
		}
		if len(report.Lenses) != 0 || !strings.Contains(strings.Join(report.Telemetry.Gaps, "\n"), "invocation attribution unresolved") {
			t.Errorf("declared=%t: ambiguous explicit run lenses=%v gaps=%v", declared, report.Lenses, report.Telemetry.Gaps)
		}
	}
	for idx, wantCalls := range []int{2, 1} {
		runID := fmt.Sprintf("transcript:cursor-cli:%s:%d", s.SessionID, idx)
		report, err := runreport.Load(dbPath, runID)
		if err != nil {
			t.Fatal(err)
		}
		if report.Telemetry.RootSpan != runreport.SpanInvocation || report.Metrics.Parent.Turns != 1 || report.Metrics.Parent.ToolCalls != wantCalls {
			t.Errorf("invocation %d: span=%s turns=%d calls=%d, want invocation/1/%d", idx, report.Telemetry.RootSpan, report.Metrics.Parent.Turns, report.Metrics.Parent.ToolCalls, wantCalls)
		}
	}
}

func TestUserShapeAndSnapshotDiagnostics(t *testing.T) {
	raw, err := os.ReadFile("testdata/parent.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	for _, malformed := range []bool{false, true} {
		mutated := mutateBlob(t, raw, func(data []byte) ([]byte, bool) {
			f, ok := decodeWire(data)
			if !ok || !strings.HasPrefix(firstString(f, 1), "$work ") {
				return nil, false
			}
			data = binary.AppendUvarint(data, 99<<3)
			data = binary.AppendUvarint(data, 1)
			data = binary.AppendUvarint(data, 25<<3|2)
			data = append(data, 1, 'x')
			data = append(data, 10<<3|2, 32)
			return append(data, bytes.Repeat([]byte{0xff}, 32)...), true
		})
		if malformed {
			row, _ := json.Marshal(journalRecord{Format: journalFormat, Kind: "blobs", Key: strings.Repeat("ff", 32), ValueType: "blob", Value: base64.StdEncoding.EncodeToString([]byte{0xff})})
			mutated = append(append(mutated, row...), '\n')
		}
		s, err := Parse(bytes.NewReader(mutated))
		if err != nil {
			t.Fatal(err)
		}
		var diagnostics []string
		for _, u := range s.Unknown {
			diagnostics = append(diagnostics, u.Type+":"+u.Subtype)
		}
		prefix := "reference:"
		if malformed {
			prefix = "protobuf:"
		}
		for _, want := range []string{"user_message:field_99", "user_message:field_25_wire_2", prefix + "user_snapshot:" + strings.Repeat("ff", 32)} {
			if !strings.Contains(strings.Join(diagnostics, "\n"), want) {
				t.Errorf("malformed=%v missing diagnostic %s", malformed, want)
			}
		}
	}
}

func TestNestedMessageShapeDiagnostics(t *testing.T) {
	raw, err := os.ReadFile("testdata/parent.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []struct {
		field int
		name  string
	}{{1, "assistant_message"}, {3, "thinking_message"}} {
		mutated := mutateBlob(t, raw, func(data []byte) ([]byte, bool) {
			f, ok := decodeWire(data)
			if !ok || len(fieldBytes(f, 1)) != 1 {
				return nil, false
			}
			inner, ok := decodeWire(fieldBytes(f, 1)[0])
			if !ok || !strings.Contains(firstString(inner, 1), "Second run ended.") {
				return nil, false
			}
			// Valid protobuf, unsupported message schema: text is a varint
			// and field 99 is not in the installed producer's catalog.
			payload := binary.AppendUvarint([]byte{1 << 3, 1}, 99<<3)
			payload = append(payload, 1)
			out := binary.AppendUvarint([]byte{byte(kind.field<<3 | 2)}, uint64(len(payload)))
			return append(out, payload...), true
		})
		s, err := Parse(bytes.NewReader(mutated))
		if err != nil {
			t.Fatal(err)
		}
		var diagnostics []string
		for _, u := range s.Unknown {
			diagnostics = append(diagnostics, u.Type+":"+u.Subtype)
		}
		for _, suffix := range []string{":field_1_wire_0", ":field_99"} {
			if !strings.Contains(strings.Join(diagnostics, "\n"), kind.name+suffix) {
				t.Errorf("missing nested diagnostic %s%s", kind.name, suffix)
			}
		}
	}
}

func TestUnobservedToolDurationReachesReport(t *testing.T) {
	raw, err := os.ReadFile("testdata/parent.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	mutated := mutateBlob(t, raw, func(data []byte) ([]byte, bool) {
		f, ok := decodeWire(data)
		if !ok || len(fieldBytes(f, 2)) != 1 {
			return nil, false
		}
		tool, ok := decodeWire(fieldBytes(f, 2)[0])
		if !ok || firstString(tool, 57) == "" {
			return nil, false
		}
		var inner []byte
		for _, field := range tool {
			if field.num == 60 {
				continue
			}
			inner = binary.AppendUvarint(inner, uint64(field.num<<3|field.wire))
			if field.wire == 0 {
				inner = binary.AppendUvarint(inner, field.value)
			} else if field.wire == 2 {
				inner = binary.AppendUvarint(inner, uint64(len(field.data)))
				inner = append(inner, field.data...)
			} else {
				t.Fatalf("unexpected fixture wire %d", field.wire)
			}
		}
		out := binary.AppendUvarint([]byte{2<<3 | 2}, uint64(len(inner)))
		return append(out, inner...), true
	})
	s, err := Parse(bytes.NewReader(mutated))
	if err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(t.TempDir(), "summaries.db")
	st, err := summaries.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.WriteSummary(context.Background(), s, summaries.SourceInfo{}); err != nil {
		t.Fatal(err)
	}
	var missing int
	if err := st.DB().QueryRow(`SELECT count(*) FROM tool_calls WHERE duration_ms IS NULL`).Scan(&missing); err != nil {
		t.Fatal(err)
	}
	if missing != 1 {
		t.Errorf("missing duration rows = %d, want 1", missing)
	}
	report, err := runreport.Load(dbPath, "transcript:cursor-cli:"+s.SessionID+":0")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(report.Telemetry.Gaps, "\n"), "duration_unavailable") {
		t.Fatal("report omitted missing tool duration")
	}
	if report.Metrics.Total.ToolTimeCoverage.Untimed != 1 {
		t.Fatal("missing duration was counted as timed")
	}
	encoded, err := json.Marshal(report)
	if err != nil || !strings.Contains(string(encoded), `"tool_time_ms":null`) {
		t.Fatalf("unavailable tool time encoded as measured: %v", err)
	}
}

func TestJournalReplayAndMissingEvidence(t *testing.T) {
	raw, err := os.ReadFile("testdata/parent.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	first, err := Parse(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	replay, err := Parse(bytes.NewReader(append(append([]byte{}, raw...), raw...)))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, replay) {
		t.Fatal("replayed journal changed normalized records or lens identity")
	}
	if len(first.Turns) != 2 || first.Turns[0].Model != "cursor-grok-4.5-high" || first.Turns[1].Model != "cursor-test-model-2" || len(first.Compactions) != 1 {
		t.Fatalf("turns/compaction = %+v / %+v", first.Turns, first.Compactions)
	}
	if len(first.LensResponses) != 3 {
		t.Fatalf("lens responses = %d", len(first.LensResponses))
	}
	for _, response := range first.LensResponses {
		if response.TurnIdx != 1 || len(response.Raw) < 1000 || response.SourceLine <= 0 || response.At.IsZero() {
			t.Fatalf("lens provenance/truncation: %+v", response)
		}
	}
	// A future top-level shape and a deleted referenced step remain visible;
	// no lookup in orphan blobs may silently reconstruct a deleted record.
	var stepKey string
	for _, line := range bytes.Split(raw, []byte("\n")) {
		var row journalRecord
		if json.Unmarshal(line, &row) != nil || row.Kind != "blobs" {
			continue
		}
		b, err := base64.StdEncoding.DecodeString(row.Value)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "Second run ended.") {
			stepKey = row.Key
			break
		}
	}
	if stepKey == "" {
		t.Fatal("fixture final step absent")
	}
	deleted, _ := json.Marshal(journalRecord{Format: journalFormat, Kind: "blobs", Key: stepKey, Deleted: true})
	mutated := append(append(append([]byte{}, raw...), deleted...), '\n')
	mutated = append(mutated, []byte("{\"format\":\"cursor-store-v2\",\"kind\":\"new\"}\nnot-json\n")...)
	parsed, err := Parse(bytes.NewReader(mutated))
	if err != nil {
		t.Fatal(err)
	}
	var types []string
	for _, u := range parsed.Unknown {
		types = append(types, u.Type+":"+u.Subtype)
	}
	joined := strings.Join(types, "\n")
	for _, want := range []string{"reference:step:" + stepKey, "journal:format:cursor-store-v2", "journal:malformed_json"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %s diagnostic: %v", want, types)
		}
	}
	if len(parsed.LensResponses) != 1 {
		t.Fatalf("deleted assistant step still supplied lenses: %+v", parsed.LensResponses)
	}
	nullRow, _ := json.Marshal(journalRecord{Format: journalFormat, Kind: "blobs", Key: stepKey, ValueType: "null"})
	parsed, err = Parse(bytes.NewReader(append(append(append([]byte{}, raw...), nullRow...), '\n')))
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.LensResponses) != 1 {
		t.Fatalf("NULL assistant step retained stale lenses: %d", len(parsed.LensResponses))
	}
}

func TestLargeJournalRecord(t *testing.T) {
	raw, err := os.ReadFile("testdata/parent.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	large, err := json.Marshal(journalRecord{Format: journalFormat, Kind: "blobs", Key: "unreferenced", ValueType: "blob", Value: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("x"), 12*1024*1024))})
	if err != nil {
		t.Fatal(err)
	}
	s, err := Parse(bytes.NewReader(append(append(large, '\n'), raw...)))
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Turns) != 2 || len(s.LensResponses) != 3 {
		t.Fatalf("large unreferenced blob lost session records: %d turns, %d lenses", len(s.Turns), len(s.LensResponses))
	}
}

// This capture was shipped through Loom by capture-ship-complete-60ab. It is
// optional on other machines, but locks the parser to the source format on the
// development host where that evidence is retained.
func TestParseShippedCursorSession(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home")
	}
	path := filepath.Join(home, ".loom", "received", "cursor-cli",
		"-private-tmp-loom-cursor-work_gzTD7K-probe",
		"963ac97f-c33f-46de-9624-3e5946b61a0a.jsonl")
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		t.Skip("shipped Cursor evidence not retained")
	}
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	s, err := Parse(f)
	if err != nil {
		t.Fatal(err)
	}
	if s.Agent != summary.AgentCursor || s.SessionID != "963ac97f-c33f-46de-9624-3e5946b61a0a" {
		t.Fatalf("identity = %s/%s", s.Agent, s.SessionID)
	}
	if len(s.Turns) != 1 || !strings.Contains(s.Turns[0].UserMessage, "real-session capture test") {
		t.Fatalf("turns = %+v", s.Turns)
	}
	if got := len(s.ToolCalls); got != 2 {
		t.Fatalf("tool calls = %d, want 2", got)
	}
	if s.ToolCalls[0].ToolName != "Read" || s.ToolCalls[0].ResultSummary == "" || s.ToolCalls[0].DurationMs <= 0 {
		t.Fatalf("read call = %+v", s.ToolCalls[0])
	}
	if s.ToolCalls[1].ToolName != "Task" || !strings.Contains(s.ToolCalls[1].ResultSummary, "5aabb54a") {
		t.Fatalf("task call = %+v", s.ToolCalls[1])
	}
	if s.Model != "cursor-grok-4.5-high" || !s.UsageUnavailable {
		t.Fatalf("model/usage unavailable = %q/%v", s.Model, s.UsageUnavailable)
	}
	if len(s.FilesTouched) != 1 || s.FilesTouched[0].Path != "/tmp/loom-cursor-work.gzTD7K/probe/evidence.txt" {
		t.Fatalf("files = %+v", s.FilesTouched)
	}
	if s.StartTime.IsZero() || s.EndTime.IsZero() || !s.EndTime.After(s.StartTime) {
		t.Fatalf("span = %s..%s", s.StartTime, s.EndTime)
	}
}

func TestParseShippedCursorChildRelationship(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home")
	}
	path := filepath.Join(home, ".loom", "received", "cursor-cli",
		"-private-tmp-loom-cursor-work_gzTD7K-probe",
		"963ac97f-c33f-46de-9624-3e5946b61a0a", "subagents",
		"5aabb54a-05c7-403c-9721-1bf0448847c1.jsonl")
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		t.Skip("shipped Cursor child evidence not retained")
	}
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	s, err := Parse(f)
	if err != nil {
		t.Fatal(err)
	}
	if s.ParentSessionID != "963ac97f-c33f-46de-9624-3e5946b61a0a" || s.SpawnDepth != 1 {
		t.Fatalf("parent = %q depth %d", s.ParentSessionID, s.SpawnDepth)
	}
	if len(s.Turns) != 1 || len(s.ToolCalls) != 1 || s.ToolCalls[0].ToolName != "Read" {
		t.Fatalf("child shape: turns=%d tools=%+v", len(s.Turns), s.ToolCalls)
	}
}
