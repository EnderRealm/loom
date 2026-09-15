package summarize

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"loom/internal/parse/cursorparse"
	"loom/internal/parse/summary"
	"loom/internal/runreport"
	"loom/internal/runs"
	"loom/internal/summaries"
	"loom/internal/workreport"
)

const cursorParent = "963ac97f-c33f-46de-9624-3e5946b61a0a"
const cursorChild = "5aabb54a-05c7-403c-9721-1bf0448847c1"

func TestCursorRootIdentityDisambiguatesOverlappingRuns(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "summaries.db")
	st, err := summaries.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	raw, err := os.ReadFile(filepath.Join("..", "parse", "cursorparse", "testdata", "parent.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{cursorParent, "overlapping-session"} {
		s, err := cursorparse.Parse(strings.NewReader(string(raw)))
		if err != nil {
			t.Fatal(err)
		}
		s.SessionID = id
		if err := st.WriteSummary(context.Background(), s, summaries.SourceInfo{}); err != nil {
			t.Fatal(err)
		}
	}
	registry := `{"v":1,"kind":"run","run_id":"root-identified-run","ticket":"loom/cursor-second-0002","runtime":"cursor-cli","started_at":"2026-09-14T06:12:15Z"}` + "\n" +
		`{"v":1,"kind":"execution","execution_id":"identified-root","run_id":"root-identified-run","execution_kind":"root","agent":"cursor-cli","session_id":"` + cursorParent + `","started_at":"2026-09-14T06:12:15Z"}` + "\n"
	if _, err := st.ImportExecutions(context.Background(), "root-identity-registry", strings.NewReader(registry), int64(len(registry)), time.Now()); err != nil {
		t.Fatal(err)
	}
	list, err := runs.List(st.DB(), time.Time{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 4 {
		t.Errorf("got %d runs, want one recorded and three unclaimed historical invocations", len(list))
	}
	for _, r := range list {
		if r.Origin == runs.OriginTranscript && r.Transcript.SessionID == cursorParent && r.Ticket == "loom/cursor-second-0002" {
			t.Error("root-identified invocation also appears as a historical run")
		}
	}
	recorded, err := runs.Load(st.DB(), "root-identified-run")
	if err != nil {
		t.Fatal(err)
	}
	if recorded.Transcript == nil || recorded.Transcript.SessionID != cursorParent || recorded.TranscriptBasis != runs.BasisDeclared || len(recorded.Lenses) != 3 {
		t.Errorf("root identity or lenses lost: transcript=%+v basis=%s lenses=%d", recorded.Transcript, recorded.TranscriptBasis, len(recorded.Lenses))
	}
	responses, err := runreport.LensResponses(st.DB(), recorded)
	if err != nil {
		t.Fatal(err)
	}
	if len(responses) != 3 {
		t.Errorf("recorded lens responses = %d, want 3", len(responses))
	}
	report, err := runreport.Load(dbPath, recorded.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if report.Metrics.Parent.Turns != 1 || report.Metrics.Parent.ToolCalls != 1 {
		t.Errorf("recorded parent metrics = %+v", report.Metrics.Parent)
	}
}

func TestCursorChildResumesInOneInvocation(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "summaries.db")
	st, err := summaries.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var parent, childSummary *summary.SessionSummary
	for _, name := range []string{"parent", "child"} {
		raw, err := os.ReadFile(filepath.Join("..", "parse", "cursorparse", "testdata", name+".jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		s, err := cursorparse.Parse(strings.NewReader(string(raw)))
		if err != nil {
			t.Fatal(err)
		}
		if name == "parent" {
			parent = s
			resume := s.ToolCalls[1]
			resume.CallID = "resume-child"
			resume.ChildResumeID = cursorChild
			s.ToolCalls = append(s.ToolCalls, resume)
		} else {
			childSummary = s
		}
		if err := st.WriteSummary(context.Background(), s, summaries.SourceInfo{}); err != nil {
			t.Fatal(err)
		}
	}
	check := func(resolved bool) {
		t.Helper()
		list, err := runs.List(st.DB(), time.Time{}, time.Time{})
		if err != nil {
			t.Fatal(err)
		}
		if len(list) != 2 {
			t.Fatalf("runs = %d", len(list))
		}
		report, err := runreport.Load(dbPath, list[0].RunID)
		if err != nil {
			t.Fatal(err)
		}
		cost, err := workreport.LoadCost(dbPath, time.Time{}, time.Time{})
		if err != nil {
			t.Fatal(err)
		}
		if resolved {
			if len(list[0].Root.Children) != 1 || len(list[0].Unresolved) != 0 || len(list[1].Root.Children) != 0 || report.Metrics.Descendants.Transcripts != 1 || report.Metrics.Descendants.ToolCalls != 1 || cost.Runs[0].Subagents != 1 {
				t.Fatalf("same-invocation resume: children=%d unresolved=%d metrics=%+v cost=%+v", len(list[0].Root.Children), len(list[0].Unresolved), report.Metrics.Descendants, cost.Runs[0])
			}
			child := list[0].Root.Children[0]
			if child.DispatchID != parent.ToolCalls[1].CallID || len(child.DispatchIDs) != 2 {
				t.Errorf("creation/resume associations = %+v", child)
			}
		} else if len(list[0].Root.Children) != 0 || len(list[1].Root.Children) != 0 || len(list[0].Unresolved) != 1 || len(list[1].Unresolved) != 1 || len(list[0].Diagnostics) == 0 || report.Metrics.Descendants.Transcripts != 0 || cost.Runs[0].Subagents != 0 || cost.Runs[1].Subagents != 0 {
			t.Fatal("ambiguous child ownership did not remain unresolved")
		}
	}
	check(true)
	setMetadataDispatch := func(id string) {
		t.Helper()
		childSummary.ParentToolCallID = id
		if err := st.WriteSummary(context.Background(), childSummary, summaries.SourceInfo{}); err != nil {
			t.Fatal(err)
		}
	}
	setMetadataDispatch("resume-child")
	check(true)
	// The parent's unrelated Read call is in the same invocation. Its mere
	// existence cannot reconcile metadata that contradicts the Task result.
	setMetadataDispatch(parent.ToolCalls[0].CallID)
	check(false)
	setMetadataDispatch(parent.ToolCalls[1].CallID)
	check(true)
	parent.ToolCalls[len(parent.ToolCalls)-1].TurnIdx = parent.Turns[len(parent.Turns)-1].Idx
	if err := st.WriteSummary(context.Background(), parent, summaries.SourceInfo{}); err != nil {
		t.Fatal(err)
	}
	check(false)
}

func TestCursorChildArrivesAfterParent(t *testing.T) {
	received := t.TempDir()
	dbPath := filepath.Join(t.TempDir(), "summaries.db")
	st, err := summaries.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var executionID string
	for i, name := range []string{"parent", "child"} {
		data, err := os.ReadFile(filepath.Join("..", "parse", "cursorparse", "testdata", name+".jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(received, "cursor-cli", "loom", cursorParent+".jsonl")
		if i == 1 {
			path = filepath.Join(received, "cursor-cli", "loom", cursorParent, "subagents", cursorChild+".jsonl")
		}
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0644); err != nil {
			t.Fatal(err)
		}
		if res := sweep(context.Background(), st, received, false, false); res.parsed < 1 || res.errored != 0 {
			t.Fatalf("%s sweep = %+v", name, res)
		}
		list, err := runs.List(st.DB(), time.Time{}, time.Time{})
		if err != nil {
			t.Fatal(err)
		}
		if len(list) != 2 || len(list[0].Root.Children) != 1 || len(list[1].Root.Children) != 0 {
			t.Fatalf("%s child attribution = %+v", name, list)
		}
		child := list[0].Root.Children[0]
		if child.Transcript.SessionID != cursorChild || child.DispatchID == "" || (i == 1 && child.ExecutionID != executionID) {
			t.Fatalf("%s child identity = %+v", name, child)
		}
		executionID = child.ExecutionID
		report, err := runreport.Load(dbPath, list[0].RunID)
		if err != nil {
			t.Fatal(err)
		}
		encoded, _ := json.Marshal(report)
		missing := strings.Contains(string(encoded), "cursor-cli/"+cursorChild+" not in summaries.db")
		if missing != (i == 0) || report.Metrics.Total.Transcripts != i+1 || report.Metrics.Descendants.ToolCalls != i {
			t.Errorf("%s telemetry = %s", name, encoded)
		}
		childMetrics := report.Executions[1]
		if i == 0 && (childMetrics.DurationMs == nil || *childMetrics.DurationMs != 5725 || childMetrics.StartedAt != "" || childMetrics.EndedAt != "") {
			t.Errorf("observed child duration lost or timestamps invented: %+v", childMetrics)
		}
		if report.Metrics.Descendants.ExecutionTimeCoverage != (runreport.Coverage{Timed: 1}) {
			t.Errorf("%s child timing coverage = %+v", name, report.Metrics.Descendants.ExecutionTimeCoverage)
		}
		for scope, metrics := range map[string]runreport.Metrics{"child": childMetrics.Metrics, "descendants": report.Metrics.Descendants, "total": report.Metrics.Total} {
			wire, _ := json.Marshal(metrics)
			if !metrics.TokenUsageUnavailable || !strings.Contains(string(wire), `"total_tokens":null`) {
				t.Errorf("%s %s unavailable tokens encoded as measured: %s", name, scope, wire)
			}
			if strings.Contains(string(wire), `"tool_time_ms":null`) != (i == 0) {
				t.Errorf("%s %s tool timing availability = %s", name, scope, wire)
			}
		}
		wireTime, _ := json.Marshal(report.Time)
		if strings.Contains(string(wireTime), `"tool_time_ms":null`) != (i == 0) || runreport.SummaryOf(report).ToolTimeUnavailable != (i == 0) {
			t.Errorf("%s report/summary tool timing availability = %s", name, wireTime)
		}
		cost, err := workreport.LoadCost(dbPath, time.Time{}, time.Time{})
		if err != nil {
			t.Fatal(err)
		}
		c := cost.Runs[0]
		if c.Subagents != 1 || c.SubagentCostUSD != nil || c.SubagentDurationMs == nil || *c.SubagentDurationMs <= 0 || cost.Runs[1].Subagents != 0 {
			t.Errorf("%s child cost = %+v", name, c)
		}
		if i == 0 && !strings.Contains(strings.Join(c.PricingWarnings, "\n"), "no transcript") {
			t.Errorf("missing transcript not diagnosed: %+v", c.PricingWarnings)
		}
		if i == 1 {
			// Contradict the Task result with a second child naming its dispatch.
			conflict, err := cursorparse.Parse(strings.NewReader(string(data)))
			if err != nil {
				t.Fatal(err)
			}
			conflict.SessionID = "contradictory-child"
			if err := st.WriteSummary(context.Background(), conflict, summaries.SourceInfo{}); err != nil {
				t.Fatal(err)
			}
			list, err = runs.List(st.DB(), time.Time{}, time.Time{})
			if err != nil {
				t.Fatal(err)
			}
			if len(list[0].Root.Children) != 0 || len(list[0].Unresolved) != 2 || len(list[0].Diagnostics) == 0 {
				t.Errorf("conflicting child identities were resolved: children=%d unresolved=%d diagnostics=%v", len(list[0].Root.Children), len(list[0].Unresolved), list[0].Diagnostics)
			}
			report, err = runreport.Load(dbPath, list[0].RunID)
			if err != nil {
				t.Fatal(err)
			}
			cost, err = workreport.LoadCost(dbPath, time.Time{}, time.Time{})
			if err != nil {
				t.Fatal(err)
			}
			if report.Metrics.Descendants.Executions != 0 || report.Metrics.Descendants.Transcripts != 0 || cost.Runs[0].Subagents != 0 || cost.Runs[0].SubagentCostUSD != nil {
				t.Error("conflicting children contributed run or cost totals")
			}
		}
	}
}

func TestCursorHistoricalDescendantsAndMissingDispatch(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "summaries.db")
	st, err := summaries.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	readFixture := func(name string) *summary.SessionSummary {
		t.Helper()
		f, err := os.Open(filepath.Join("..", "parse", "cursorparse", "testdata", name+".jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		s, err := cursorparse.Parse(f)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	write := func(s *summary.SessionSummary) {
		t.Helper()
		if err := st.WriteSummary(context.Background(), s, summaries.SourceInfo{}); err != nil {
			t.Fatal(err)
		}
	}
	parent := readFixture("parent")
	child := readFixture("child")
	// Extend the source-derived fixture with one observed nested dispatch.
	child.ToolCalls = append(child.ToolCalls, summary.ToolCall{TurnIdx: 0, CallID: "nested-dispatch", ToolName: "Task", Kind: summary.KindTask, StartedAt: child.StartTime})
	grandchild := readFixture("child")
	grandchild.SessionID = "cursor-grandchild"
	grandchild.ParentSessionID = child.SessionID
	grandchild.ParentToolCallID = "nested-dispatch"
	write(parent)
	write(child)
	write(grandchild)
	// Parent session identity alone cannot place a child, even with one run.
	originalPrompt, originalDispatch := parent.Turns[1].UserMessage, child.ParentToolCallID
	originalChildID := parent.ToolCalls[1].ChildSessionID
	parent.ToolCalls[1].ChildSessionID = ""
	parent.Turns[1].UserMessage, child.ParentToolCallID = "ordinary conversation", ""
	write(parent)
	write(child)
	missing, err := runs.List(st.DB(), time.Time{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 1 || len(missing[0].Root.Children) != 0 || len(missing[0].Unresolved) == 0 || len(missing[0].Diagnostics) == 0 {
		t.Error("child with missing dispatch was attributed to the parent's only run")
	}
	parent.Turns[1].UserMessage, child.ParentToolCallID = originalPrompt, originalDispatch
	parent.ToolCalls[1].ChildSessionID = originalChildID
	write(parent)
	write(child)
	list, err := runs.List(st.DB(), time.Time{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || len(list[0].Root.Children) != 1 || len(list[0].Root.Children[0].Children) != 1 || len(list[1].Root.Children) != 0 {
		t.Fatalf("nested attribution = %+v", list)
	}
	nested := list[0].Root.Children[0].Children[0]
	if nested.Transcript.SessionID != grandchild.SessionID || nested.ParentExecutionID != list[0].Root.Children[0].ExecutionID {
		t.Fatalf("nested identity = %+v", nested)
	}
	report, err := runreport.Load(dbPath, list[0].RunID)
	if err != nil {
		t.Fatal(err)
	}
	if report.Metrics.Total.Transcripts != 3 || report.Metrics.Descendants.ToolCalls != 3 || report.Metrics.Total.ToolCalls != 5 {
		t.Fatalf("descendant totals = %+v", report.Metrics)
	}
	// A cycle back to the already attached root must not duplicate it.
	parent.ParentSessionID = grandchild.SessionID
	parent.ParentToolCallID = grandchild.ToolCalls[0].CallID
	write(parent)
	list, err = runs.List(st.DB(), time.Time{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	foundCycle := false
	for _, d := range list[0].Diagnostics {
		foundCycle = foundCycle || d.Code == runs.DiagCyclicParent
	}
	if !foundCycle {
		t.Fatal("cycle was not diagnosed")
	}
	if _, err := json.Marshal(list); err != nil {
		t.Fatal(err)
	}
}

// Source-derived journals exercise the same sweep, store, invocation and
// reporting path as a shipped session. The first invocation is compacted;
// the second carries long lens responses and an errored tool result.
func TestCursorSweepReportsDistinctRunsAndRebuilds(t *testing.T) {
	received := t.TempDir()
	for _, x := range []struct{ fixture, rel string }{
		{"parent", cursorParent + ".jsonl"},
		{"child", filepath.Join(cursorParent, "subagents", cursorChild+".jsonl")},
	} {
		data, err := os.ReadFile(filepath.Join("..", "parse", "cursorparse", "testdata", x.fixture+".jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(received, "cursor-cli", "loom", x.rel)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0644); err != nil {
			t.Fatal(err)
		}
	}
	dbPath := filepath.Join(t.TempDir(), "summaries.db")
	st, err := summaries.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	res := sweep(context.Background(), st, received, false, false)
	if res.parsed != 2 || res.errored != 0 {
		t.Fatalf("sweep = %+v", res)
	}
	if again := sweep(context.Background(), st, received, false, false); again.skipped != 2 {
		t.Fatalf("repeat = %+v", again)
	}
	var project string
	var known, missing bool
	if err := st.DB().QueryRow(`SELECT project, usage_known, input_tokens IS NULL FROM sessions WHERE session_id = ?`, cursorChild).Scan(&project, &known, &missing); err != nil {
		t.Fatal(err)
	}
	if project != "loom" || known || !missing {
		t.Fatalf("child identity/usage: %q %v %v", project, known, missing)
	}
	var beforeMissing, afterMissing bool
	if err := st.DB().QueryRow(`SELECT tokens_before IS NULL, tokens_after IS NULL FROM compactions WHERE session_id = ?`, cursorParent).Scan(&beforeMissing, &afterMissing); err != nil {
		t.Fatal(err)
	}
	if !beforeMissing || !afterMissing {
		t.Fatal("unavailable compaction usage stored as measured zero")
	}
	historical, err := runs.List(st.DB(), time.Time{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(historical) != 2 {
		t.Fatalf("historical runs = %+v", historical)
	}
	if len(historical[0].Root.Children) != 1 || historical[0].Root.Children[0].Transcript.SessionID != cursorChild || len(historical[1].Root.Children) != 0 {
		t.Fatalf("child attribution: first=%+v second=%+v", historical[0], historical[1])
	}
	if historical[1].Runtime != "cursor-cli" || len(historical[1].Lenses) != 3 {
		t.Fatalf("second run = %+v", historical[1])
	}
	for _, run := range historical {
		responses, err := runreport.LensResponses(st.DB(), &run)
		if err != nil {
			t.Fatal(err)
		}
		if run.Ticket == "loom/cursor-second-0002" {
			if len(responses) != 3 {
				t.Fatalf("responses = %v", responses)
			}
			for key, raw := range responses {
				if len(raw) < 1000 {
					t.Errorf("%s response truncated: %d", key, len(raw))
				}
			}
		}
	}
	registryDir := filepath.Join(received, summaries.ExecutionsAgent, "fixture")
	if err := os.MkdirAll(registryDir, 0755); err != nil {
		t.Fatal(err)
	}
	registry := filepath.Join(registryDir, "executions.jsonl")
	first := `{"v":1,"kind":"run","run_id":"cursor-first","runtime":"cursor-cli","ticket":"loom/cursor-first-0001","agent":"cursor-cli","session_id":"` + cursorParent + `","started_at":"2026-09-14T06:11:15.268Z","ended_at":"2026-09-14T06:11:29.321Z","outcome":"completed"}` + "\n" +
		`{"v":1,"kind":"execution","execution_id":"root-first","run_id":"cursor-first","execution_kind":"root","agent":"cursor-cli","session_id":"` + cursorParent + `","started_at":"2026-09-14T06:11:15.268Z","ended_at":"2026-09-14T06:11:29.321Z","outcome":"completed"}` + "\n" +
		`{"v":1,"kind":"execution","execution_id":"child-first","run_id":"cursor-first","parent_execution_id":"root-first","execution_kind":"subagent","agent":"cursor-cli","session_id":"` + cursorChild + `","started_at":"2026-09-14T06:11:20.586Z","ended_at":"2026-09-14T06:11:26.317Z","outcome":"completed"}` + "\n"
	if err := os.WriteFile(registry, []byte(first), 0644); err != nil {
		t.Fatal(err)
	}
	sweep(context.Background(), st, received, false, false)
	list, err := runs.List(st.DB(), time.Time{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("one recorded invocation hid another: %+v", list)
	}
	second := `{"v":1,"kind":"run","run_id":"cursor-second","runtime":"cursor-cli","ticket":"loom/cursor-second-0002","agent":"cursor-cli","session_id":"` + cursorParent + `","started_at":"2026-09-14T06:12:15Z","ended_at":"2026-09-14T06:12:21Z","outcome":"failed"}` + "\n" +
		`{"v":1,"kind":"execution","execution_id":"root-second","run_id":"cursor-second","execution_kind":"root","agent":"cursor-cli","session_id":"` + cursorParent + `","started_at":"2026-09-14T06:12:15Z","ended_at":"2026-09-14T06:12:21Z","outcome":"failed"}` + "\n" +
		`{"v":1,"kind":"execution","execution_id":"stage-second","run_id":"cursor-second","parent_execution_id":"root-second","execution_kind":"stage","stage":"work","stage_occurrence":1,"attempt":1,"agent":"cursor-cli","session_id":"` + cursorParent + `","started_at":"2026-09-14T06:12:15Z","ended_at":"2026-09-14T06:12:21Z","outcome":"stopped"}` + "\n" +
		`{"v":1,"kind":"execution","execution_id":"lens-first-attempt","run_id":"cursor-second","parent_execution_id":"root-second","execution_kind":"lens","lens":"security","round":2,"attempt":1,"started_at":"2026-09-14T06:12:16Z","ended_at":"2026-09-14T06:12:17Z","outcome":"failed"}` + "\n" +
		`{"v":1,"kind":"execution","execution_id":"lens-retry","run_id":"cursor-second","parent_execution_id":"root-second","execution_kind":"lens","lens":"security","round":2,"attempt":2,"agent":"codex-cli","session_id":"not-shipped","started_at":"2026-09-14T06:12:17Z","ended_at":"2026-09-14T06:12:18Z","outcome":"completed"}` + "\n"
	if err := os.WriteFile(registry, []byte(first+second), 0644); err != nil {
		t.Fatal(err)
	}
	sweep(context.Background(), st, received, false, false)
	var previous []byte
	for pass := 0; pass < 3; pass++ {
		if pass > 0 {
			sweep(context.Background(), st, received, true, false)
		}
		report, err := runreport.Load(dbPath, "cursor-first")
		if err != nil {
			t.Fatal(err)
		}
		if report.Metrics.Parent.Turns != 1 || report.Metrics.Parent.ToolCalls != 2 || report.Metrics.Descendants.ToolCalls != 1 || report.Metrics.Total.ToolCalls != 3 {
			t.Fatalf("first metrics = %+v", report.Metrics)
		}
		if report.Metrics.Total.CostUSD != nil || !report.Metrics.Total.TokenUsageUnavailable {
			t.Fatalf("unknown usage priced: %+v", report.Metrics.Total)
		}
		encoded, err := json.Marshal(report)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(encoded), `"total_tokens":null`) || !strings.Contains(string(encoded), `"cache_semantics":"unknown"`) {
			t.Fatalf("unknown usage encoded as measured: %s", encoded)
		}
		if pass > 0 && !reflect.DeepEqual(previous, encoded) {
			t.Fatal("report changed after refold/rebuild")
		}
		previous = encoded
		secondReport, err := runreport.Load(dbPath, "cursor-second")
		if err != nil {
			t.Fatal(err)
		}
		if secondReport.Metrics.Parent.Turns != 1 || secondReport.Metrics.Total.ToolCalls != 1 || secondReport.Metrics.Total.Failures.Tool != 1 || secondReport.Metrics.Total.Transcripts != 1 {
			t.Fatalf("second metrics = %+v", secondReport.Metrics)
		}
		if !strings.Contains(strings.Join(secondReport.Telemetry.Gaps, "\n"), "not-shipped") {
			t.Fatalf("missing transcript gap = %v", secondReport.Telemetry.Gaps)
		}
		if pass == 1 {
			st.Close()
			if err := Run(Options{ReceivedDir: received, DBPath: dbPath, Rebuild: true, Strict: true}); err != nil {
				t.Fatal(err)
			}
			st, err = summaries.Open(dbPath)
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
		}
	}
	cost, err := workreport.LoadCost(dbPath, time.Time{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(cost.Runs) != 2 || cost.Runs[0].Runtime != workreport.RuntimeCursor || cost.Runs[0].CostUSD != nil {
		t.Fatalf("cost report = %+v", cost)
	}
	firstCost := cost.Runs[0]
	if firstCost.Subagents != 1 || firstCost.SubagentDurationMs == nil || *firstCost.SubagentDurationMs <= 0 || firstCost.SubagentCostUSD != nil {
		t.Errorf("Cursor child cost/count/duration = %+v", firstCost)
	}
	if !strings.Contains(strings.Join(firstCost.PricingWarnings, "\n"), "subagent 0: token usage not recorded") || cost.Runs[1].Subagents != 0 {
		t.Error("Cursor child usage gap or invocation attribution missing")
	}
	// A declared session without a run timestamp does not choose either
	// invocation, and must not meter their combined session as a third run.
	dbPath = filepath.Join(t.TempDir(), "summaries.db")
	ambiguousStore, err := summaries.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer ambiguousStore.Close()
	f, err := os.Open(filepath.Join("..", "parse", "cursorparse", "testdata", "parent.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	parent, err := cursorparse.Parse(f)
	if err != nil {
		t.Fatal(err)
	}
	if err := ambiguousStore.WriteSummary(context.Background(), parent, summaries.SourceInfo{}); err != nil {
		t.Fatal(err)
	}
	ambiguous := `{"v":1,"kind":"run","run_id":"cursor-untimed","runtime":"cursor-cli","agent":"cursor-cli","session_id":"` + cursorParent + `"}` + "\n" +
		`{"v":1,"kind":"execution","execution_id":"untimed-root","run_id":"cursor-untimed","execution_kind":"root","agent":"cursor-cli","session_id":"` + cursorParent + `"}` + "\n"
	if _, err := ambiguousStore.ImportExecutions(context.Background(), "untimed-registry", strings.NewReader(ambiguous), int64(len(ambiguous)), time.Now()); err != nil {
		t.Fatal(err)
	}
	retained, err := runs.List(ambiguousStore.DB(), time.Time{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(retained) != 3 {
		t.Fatalf("unattributed record hid historical invocations: %d runs", len(retained))
	}
	untimed, err := runreport.Load(dbPath, "cursor-untimed")
	if err != nil {
		t.Fatal(err)
	}
	if untimed.Metrics.Total.Turns != 0 || untimed.Metrics.Total.ToolCalls != 0 || !strings.Contains(strings.Join(untimed.Telemetry.Gaps, "\n"), "invocation attribution unresolved") {
		t.Errorf("untimed run meters ambiguous invocation: %+v, gaps=%v", untimed.Metrics.Total, untimed.Telemetry.Gaps)
	}
	if untimed.Telemetry.RootSpan != runreport.SpanUnresolved || untimed.Metrics.Total.CostUSD != nil {
		t.Fatal("unresolved invocation reported as measured")
	}
}
