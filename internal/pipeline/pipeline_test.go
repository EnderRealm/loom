// Package pipeline holds the controlled end-to-end test: producers on disk,
// the shipper, a receiver, the summarizer and the run report, all in one
// process under a temporary HOME and LOOM_HOME.
package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loom/internal/runreport"
	"loom/internal/summarize"
	"loom/transport/receiver"
	"loom/transport/shipper"
)

const (
	runID        = "7d2e9a4c-1b3f-4e6d-8a5c-2f1e0d9c8b7a"
	ticket       = "loom/pipeline-0001"
	rootSession  = "3f8c1a2b-4d5e-4f60-8a9b-0c1d2e3f4a5b"
	agentSession = "agent-0c9d8e7f6a5b4c3d2"
	weftSession  = "5a6b7c8d-9e0f-4a1b-8c2d-3e4f5a6b7c8d"
	codexSession = "01a0a1b2-c3d4-7e5f-8a6b-7c8d9e0f1a2b"
	producer     = "warp/work@1.4.0"
)

// rfc formats t for a record or transcript line.
func rfc(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func claudeLines(session, cwd string, at time.Time, input, output, cacheRead, cacheWrite int) string {
	return fmt.Sprintf(`{"type":"user","sessionId":%q,"uuid":"u1","promptId":"p1","timestamp":%q,"cwd":%q,"version":"2.1.267","message":{"role":"user","content":"start"}}`+"\n"+
		`{"type":"assistant","sessionId":%q,"uuid":"u2","promptId":"p1","timestamp":%q,"cwd":%q,"version":"2.1.267","message":{"id":"m1","role":"assistant","model":"claude-opus-5","content":[{"type":"text","text":"done"}],"stop_reason":"end_turn","usage":{"input_tokens":%d,"output_tokens":%d,"cache_read_input_tokens":%d,"cache_creation_input_tokens":%d}}}`+"\n",
		session, rfc(at), cwd, session, rfc(at.Add(2*time.Second)), cwd, input, output, cacheRead, cacheWrite)
}

func codexLines(session, cwd string, at time.Time) string {
	return fmt.Sprintf(`{"timestamp":%q,"type":"session_meta","payload":{"id":%q,"timestamp":%q,"cwd":%q,"cli_version":"0.160.0","source":"cli","model_provider":"openai"}}`+"\n"+
		`{"timestamp":%q,"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"review"}]}}`+"\n",
		rfc(at), session, rfc(at), cwd, rfc(at.Add(time.Second)))
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func appendTo(t *testing.T, path, body string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(body); err != nil {
		t.Fatal(err)
	}
}

func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	return addr
}

// startReceiver runs a receiver on addr until stop is called; stop waits
// for Run to return so the port is free for the next one.
func startReceiver(t *testing.T, addr, storage string) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- receiver.Run(ctx, receiver.Options{Addr: addr, Storage: storage, Token: "tok"})
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err := http.Get("http://" + addr + "/healthz")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("receiver did not answer /healthz")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("receiver: %v", err)
		}
	}
}

func loadDetail(t *testing.T, dbPath string) *runreport.Detail {
	t.Helper()
	d, err := runreport.LoadDetail(dbPath, runID)
	if err != nil {
		t.Fatalf("LoadDetail: %v", err)
	}
	return d
}

func sweep(t *testing.T, received, dbPath string) {
	t.Helper()
	if err := summarize.Run(summarize.Options{ReceivedDir: received, DBPath: dbPath}); err != nil {
		t.Fatalf("summarize: %v", err)
	}
}

// executionIDs is every execution id in the report, in order, so a
// duplicate is visible as a repeated id.
func executionIDs(rep *runreport.Report) []string {
	var ids []string
	for _, e := range rep.Executions {
		ids = append(ids, e.ExecutionID)
	}
	return ids
}

func sizeOf(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Size()
}

// Complete parent, child, Weft and routed-lens records travel from the
// producers' files through capture, ingest, summary and the run view inside
// the visibility window, each execution accounted once; records that arrive
// late, or after a receiver outage, land exactly once; and the report's
// token counts move only when recorded usage does.
func TestControlledPipeline(t *testing.T) {
	home := t.TempDir()
	loomHome := filepath.Join(home, "loom")
	t.Setenv("HOME", home)
	t.Setenv("LOOM_HOME", loomHome)
	t.Setenv("LOOM_RECEIVER_TOKEN", "tok")
	received := filepath.Join(loomHome, "received")
	dbPath := filepath.Join(loomHome, "summaries.db")
	registry := filepath.Join(loomHome, "executions.jsonl")
	cwd := filepath.Join(home, "proj")

	started := time.Now()
	t0 := started.Add(-time.Minute)

	// Producers: the root Claude session with a dispatched subagent, the
	// routed lens's Codex rollout, and the registry every producer appends to.
	slug := strings.ReplaceAll(cwd, "/", "-")
	write(t, filepath.Join(home, ".claude", "projects", slug, rootSession+".jsonl"), claudeLines(rootSession, cwd, t0, 100, 50, 300, 40))
	write(t, filepath.Join(home, ".claude", "projects", slug, rootSession, "subagents", agentSession+".jsonl"), claudeLines(agentSession, cwd, t0.Add(5*time.Second), 10, 5, 0, 0))
	write(t, filepath.Join(home, ".codex", "sessions", "2026", "09", "12", "rollout-2026-09-12T10-00-00-"+codexSession+".jsonl"), codexLines(codexSession, cwd, t0.Add(20*time.Second)))
	write(t, registry, strings.Join([]string{
		fmt.Sprintf(`{"v":1,"kind":"run","run_id":%q,"ticket":%q,"runtime":"claude-code","agent":"claude-code","session_id":%q,"producer":%q,"started_at":%q,"recorded_at":%q}`, runID, ticket, rootSession, producer, rfc(t0), rfc(t0)),
		fmt.Sprintf(`{"v":1,"kind":"execution","execution_id":"root-1","run_id":%q,"execution_kind":"root","agent":"claude-code","session_id":%q,"producer":%q,"started_at":%q,"recorded_at":%q}`, runID, rootSession, producer, rfc(t0), rfc(t0)),
		fmt.Sprintf(`{"v":1,"kind":"execution","execution_id":"sub-1","run_id":%q,"parent_execution_id":"root-1","execution_kind":"subagent","agent":"claude-code","session_id":%q,"dispatch_id":"toolu_01Sub","producer":%q,"started_at":%q,"ended_at":%q,"outcome":"completed","recorded_at":%q}`, runID, agentSession, producer, rfc(t0.Add(5*time.Second)), rfc(t0.Add(10*time.Second)), rfc(t0.Add(10*time.Second))),
		fmt.Sprintf(`{"v":1,"kind":"execution","execution_id":"stage-work-1-1","run_id":%q,"parent_execution_id":"root-1","execution_kind":"stage","stage":"work","stage_occurrence":1,"attempt":1,"agent":"claude-code","session_id":%q,"producer":"weft@0.3.0","started_at":%q,"ended_at":%q,"outcome":"completed","recorded_at":%q}`, runID, weftSession, rfc(t0.Add(11*time.Second)), rfc(t0.Add(18*time.Second)), rfc(t0.Add(18*time.Second))),
		fmt.Sprintf(`{"v":1,"kind":"execution","execution_id":"lens-security-r1-a1","run_id":%q,"parent_execution_id":"root-1","execution_kind":"lens","agent":"codex-cli","session_id":%q,"dispatch_id":"call_lens","lens":"security","round":1,"attempt":1,"producer":"warp/codex-lens.sh@1.4.0","started_at":%q,"recorded_at":%q}`, runID, codexSession, rfc(t0.Add(20*time.Second)), rfc(t0.Add(20*time.Second))),
	}, "\n")+"\n")

	addr := freePort(t)
	stop := startReceiver(t, addr, received)
	write(t, filepath.Join(loomHome, "config.json"), fmt.Sprintf(`{"server_url":"http://%s","auth_token":"tok","interval_seconds":1,"notify_on_failure":false}`, addr))

	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	shipper.Once()
	sweep(t, received, dbPath)
	first := loadDetail(t, dbPath)
	if elapsed := time.Since(started); elapsed >= 30*time.Second {
		t.Fatalf("first view took %s, want under 30s", elapsed)
	}
	rep := first.Report
	if rep.Run.Ticket != ticket || rep.Run.Outcome != runreport.OutcomeRunning {
		t.Errorf("run = %+v", rep.Run)
	}
	if rep.Tree == nil || rep.Tree.ExecutionID != "root-1" || len(rep.Tree.Children) != 3 {
		t.Fatalf("tree = %+v, want root-1 with three children", rep.Tree)
	}
	kinds := map[string]string{}
	for _, c := range rep.Tree.Children {
		kinds[c.ExecutionID] = c.Kind
	}
	if kinds["sub-1"] != "subagent" || kinds["stage-work-1-1"] != "stage" || kinds["lens-security-r1-a1"] != "lens" {
		t.Errorf("children = %v", kinds)
	}
	// The root has no terminal record yet either: it is open until the run
	// ends, as the lens is until its own record closes it.
	if got := strings.Join(rep.Telemetry.ExecutionsPending, " "); got != "root-1 lens-security-r1-a1" {
		t.Errorf("pending = %q, want the open root and the running lens", got)
	}
	if rep.Metrics.Total.Executions != 4 || len(rep.Executions) != 4 {
		t.Errorf("executions: total %d, listed %d, want 4 and 4", rep.Metrics.Total.Executions, len(rep.Executions))
	}
	ids := executionIDs(rep)
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			t.Errorf("execution %s listed twice: %v", id, ids)
		}
		seen[id] = true
	}
	claude := rep.Metrics.Parent.TokensByRuntime["claude-code"]
	if claude == nil || claude.Input != 100 || claude.Output != 50 || claude.CacheRead != 300 || claude.CacheWrite != 40 {
		t.Fatalf("parent claude tokens = %+v, want the recorded usage", claude)
	}
	if rep.Metrics.Total.TotalTokens != claude.Total || rep.Metrics.Descendants.TotalTokens != 0 {
		t.Errorf("total tokens %d, descendants %d; want the root's %d and 0: the pending lens has no usage", rep.Metrics.Total.TotalTokens, rep.Metrics.Descendants.TotalTokens, claude.Total)
	}
	// The routed lens's rollout was folded: the lens is metered from its
	// own transcript rather than named as a gap.
	for _, g := range rep.Telemetry.Gaps {
		if strings.Contains(g, codexSession) {
			t.Errorf("lens transcript not folded: %q", g)
		}
	}
	if first.SweptAt.IsZero() {
		t.Error("first detail carries no sweep time")
	}
	if _, ok := shipper.LastSync(); !ok {
		t.Fatal("no last sync after a healthy tick")
	}

	// Delayed records: the lens ends and the run ends. Both land, the
	// execution count holds, and no usage was recorded so no token moves.
	appendTo(t, registry, strings.Join([]string{
		fmt.Sprintf(`{"v":1,"kind":"execution","execution_id":"lens-security-r1-a1","run_id":%q,"ended_at":%q,"outcome":"completed","recorded_at":%q}`, runID, rfc(t0.Add(40*time.Second)), rfc(t0.Add(40*time.Second))),
	}, "\n")+"\n")
	shipper.Once()
	sweep(t, received, dbPath)
	second := loadDetail(t, dbPath)
	rep = second.Report
	if got := strings.Join(rep.Telemetry.ExecutionsPending, " "); got != "root-1" {
		t.Errorf("pending after the lens ended = %q, want the open root alone", got)
	}
	if rep.Metrics.Total.Executions != 4 || len(rep.Executions) != 4 {
		t.Errorf("executions after the delayed record: total %d, listed %d", rep.Metrics.Total.Executions, len(rep.Executions))
	}
	if rep.Metrics.Total.TotalTokens != first.Report.Metrics.Total.TotalTokens {
		t.Errorf("tokens moved from %d to %d with no new usage recorded", first.Report.Metrics.Total.TotalTokens, rep.Metrics.Total.TotalTokens)
	}
	if !second.SweptAt.After(first.SweptAt) {
		t.Errorf("sweep time did not advance: %s then %s", first.SweptAt, second.SweptAt)
	}
	// A sweep with nothing new changes nothing in the report.
	sweep(t, received, dbPath)
	third := loadDetail(t, dbPath)
	want, _ := json.Marshal(second.Report)
	got, _ := json.Marshal(third.Report)
	if !bytes.Equal(want, got) {
		t.Errorf("report changed across an idle sweep:\n%s\n%s", want, got)
	}

	// Receiver down: the records stay staged, the last sync stands, and the
	// tick says it skipped shipping. On recovery they land exactly once.
	lastSync, ok := shipper.LastSync()
	if !ok {
		t.Fatal("no last sync before the outage")
	}
	stop()
	landed := filepath.Join(received, "loom-executions")
	before := registrySize(t, landed)
	end := rfc(t0.Add(50 * time.Second))
	appendTo(t, registry, strings.Join([]string{
		fmt.Sprintf(`{"v":1,"kind":"execution","execution_id":"root-1","run_id":%q,"ended_at":%q,"outcome":"completed","recorded_at":%q}`, runID, end, end),
		fmt.Sprintf(`{"v":1,"kind":"run","run_id":%q,"ended_at":%q,"outcome":"completed","reporting_cutoff":%q,"producer":%q,"recorded_at":%q}`, runID, end, end, producer, end),
	}, "\n")+"\n")
	logs.Reset()
	shipper.Once()
	if !strings.Contains(logs.String(), "ship-skipped") {
		t.Errorf("tick with the receiver down did not report ship-skipped:\n%s", logs.String())
	}
	if after := registrySize(t, landed); after != before {
		t.Errorf("landed registry grew from %d to %d with the receiver down", before, after)
	}
	if sync, ok := shipper.LastSync(); !ok || !sync.Equal(lastSync) {
		t.Errorf("last sync = %s %v after a failed tick, want %s kept", sync, ok, lastSync)
	}
	sweep(t, received, dbPath)
	if d := loadDetail(t, dbPath); d.Report.Run.Outcome != runreport.OutcomeRunning {
		t.Errorf("outcome %s while the end record is still staged", d.Report.Run.Outcome)
	}

	stop = startReceiver(t, addr, received)
	defer stop()
	shipper.Once()
	sweep(t, received, dbPath)
	recovered := loadDetail(t, dbPath)
	rep = recovered.Report
	if rep.Run.Outcome != "completed" || len(rep.Telemetry.ExecutionsPending) != 0 {
		t.Errorf("after recovery: outcome %s, pending %v", rep.Run.Outcome, rep.Telemetry.ExecutionsPending)
	}
	if rep.Metrics.Total.Executions != 4 || len(rep.Executions) != 4 {
		t.Errorf("executions after recovery: total %d, listed %d", rep.Metrics.Total.Executions, len(rep.Executions))
	}
	if rep.Metrics.Total.TotalTokens != first.Report.Metrics.Total.TotalTokens {
		t.Errorf("tokens moved to %d across the outage with no new usage", rep.Metrics.Total.TotalTokens)
	}
	if sync, ok := shipper.LastSync(); !ok || !sync.After(lastSync) {
		t.Errorf("last sync did not advance on recovery: %s %v", sync, ok)
	}
	if len(rep.Diagnostics) != 0 {
		t.Errorf("diagnostics = %+v", rep.Diagnostics)
	}
	if elapsed := time.Since(started); elapsed >= 30*time.Second {
		t.Errorf("whole pipeline took %s", elapsed)
	}
}

// registrySize is the size of the one landed registry under root, or 0
// before anything landed.
func registrySize(t *testing.T, root string) int64 {
	t.Helper()
	var size int64
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(path, ".jsonl") {
			size += sizeOf(t, path)
		}
		return nil
	})
	return size
}
