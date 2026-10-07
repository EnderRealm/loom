package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loom/internal/parse/summary"
	"loom/internal/summaries"
	"loom/internal/summarize"
)

func runProjectState(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newProjectStateCmd()
	cmd.SetArgs(args)
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	err := cmd.Execute()
	return out.String(), err
}

// captureLog routes the standard logger — the summarizer's only channel —
// into a buffer for the rest of the test.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })
	return &buf
}

// foldOnce runs one summarize sweep over an empty received tree, so the only
// thing it does to db is the project_state rebuild.
func foldOnce(t *testing.T, db string, window time.Duration) {
	t.Helper()
	if err := summarize.Run(summarize.Options{ReceivedDir: t.TempDir(), DBPath: db, Verbose: true, StateWindow: window}); err != nil {
		t.Fatalf("summarize: %v", err)
	}
}

func TestProjectStateRebuiltByTheFold(t *testing.T) {
	now := time.Now().UTC()
	ts := func(ago time.Duration) string { return now.Add(-ago).Format(time.RFC3339) }
	day := 24 * time.Hour

	alphaRepo, forgeRepo, elsewhere := t.TempDir(), t.TempDir(), t.TempDir()
	tkStoreFor(t, "alpha", alphaRepo, "forge", forgeRepo)

	// The project set is the knowledge store's scopes: quiet has neither tickets
	// nor sessions, other has tickets but no scope, and neither an
	// underscore-prefixed directory nor a file under truths/ is a scope.
	knowledge := t.TempDir()
	t.Setenv("LOOM_KNOWLEDGE_ROOT", knowledge)
	for _, d := range []string{"alpha", "forge", "quiet", "_archive"} {
		if err := os.MkdirAll(filepath.Join(knowledge, "truths", d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(knowledge, "truths", "_schema.md"), []byte("schema"), 0o644); err != nil {
		t.Fatal(err)
	}

	fakeTK(t, strings.Join([]string{
		`{"id":"alpha/open-0001","status":"open","type":"feature","title":"Open","created":"` + ts(5*day) + `"}`,
		`{"id":"alpha/done-0002","status":"done","type":"feature","title":"Done","created":"` + ts(9*day) + `","closed":"` + ts(2*day) + `"}`,
		`{"id":"alpha/old-0003","status":"closed","type":"bug","title":"Old","created":"` + ts(90*day) + `","closed":"` + ts(60*day) + `"}`,
		`{"id":"forge/backlog-0004","status":"backlog","type":"feature","title":"Backlog","created":"` + ts(120*day) + `"}`,
		// Done before tk stored a closed date: terminal, but never closed in a window.
		`{"id":"forge/legacy-0005","status":"done","type":"feature","title":"Legacy","created":"` + ts(200*day) + `"}`,
		`{"id":"other/x-0006","status":"done","type":"feature","title":"Other","created":"` + ts(3*day) + `","closed":"` + ts(1*day) + `"}`,
	}, "\n")+"\n", nil)

	home := t.TempDir()
	t.Setenv("LOOM_HOME", home)
	db := filepath.Join(home, "summaries.db")
	st, err := summaries.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	commit := func(at time.Time, hash, subject string) summary.ToolCall {
		return summary.ToolCall{Kind: summary.KindBash, StartedAt: at, ResultSummary: "[main " + hash + "] " + subject}
	}
	session := func(id, cwd string, start time.Time, calls ...summary.ToolCall) {
		sum := &summary.SessionSummary{SessionID: id, Agent: summary.AgentClaude,
			StartTime: start, EndTime: start.Add(time.Hour), ToolCalls: calls}
		if err := st.WriteSummary(context.Background(), sum, summaries.SourceInfo{Path: "/tmp/" + id + ".jsonl", CwdRaw: cwd}); err != nil {
			t.Fatalf("WriteSummary %s: %v", id, err)
		}
	}
	session("alpha-work", alphaRepo, now.Add(-2*day), commit(now.Add(-2*day), "a1a1a1a", "[alpha/done-0002] Ship it"))
	// A checkout that resolves to no project: its marked commit still counts for
	// alpha, the session counts for nobody.
	session("cross", elsewhere, now.Add(-1*day), commit(now.Add(-1*day), "a2a2a2a", "[alpha/open-0001] From elsewhere"))
	// The same commit read under a second session counts once.
	session("dup", alphaRepo, now.Add(-1*day), commit(now.Add(-1*day), "a2a2a2a", "[alpha/open-0001] From elsewhere"))
	// forge's last activity, an unmarked commit, well before the window.
	session("forge-work", forgeRepo, now.Add(-100*day), commit(now.Add(-100*day), "f1f1f1f", "Tidy"))
	st.Close()

	logs := captureLog(t)
	foldOnce(t, db, 30*day)
	if !strings.Contains(logs.String(), "project state alpha: commits=2 sessions=2 open=1 closed=1 dormant=false") ||
		!strings.Contains(logs.String(), "project state forge: commits=0 sessions=0 open=1 closed=0 dormant=true") {
		t.Fatalf("verbose fold names no per-scope counts:\n%s", logs)
	}

	out, err := runProjectState(t, "--json")
	if err != nil {
		t.Fatalf("project-state: %v", err)
	}
	var rows []summaries.ProjectState
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("output is not a JSON array: %v\n%s", err, out)
	}
	var names []string
	for _, r := range rows {
		names = append(names, r.Project)
	}
	if strings.Join(names, ",") != "alpha,forge,quiet" {
		t.Fatalf("projects = %v, want the three scopes", names)
	}

	alpha, forge, quiet := rows[0], rows[1], rows[2]
	if alpha.CommitsInWindow != 2 || alpha.SessionsInWindow != 2 || alpha.OpenTickets != 1 ||
		alpha.TicketsClosedInWindow != 1 || alpha.Dormant || alpha.WindowSeconds != int64(30*day/time.Second) {
		t.Fatalf("alpha = %+v", alpha)
	}
	if alpha.LastTicketClosedAt == nil || alpha.LastTicketClosedAt.Format(time.RFC3339) != ts(2*day) ||
		alpha.LastCommitAt == nil || !alpha.LastCommitAt.Equal(now.Add(-1*day)) {
		t.Fatalf("alpha last times = %v / %v", alpha.LastTicketClosedAt, alpha.LastCommitAt)
	}
	if !forge.Dormant || forge.OpenTickets != 1 || forge.CommitsInWindow != 0 || forge.SessionsInWindow != 0 ||
		forge.LastTicketClosedAt != nil || forge.LastCommitAt == nil || !forge.LastCommitAt.Equal(now.Add(-100*day)) ||
		forge.LastSessionAt == nil || !forge.LastSessionAt.Equal(now.Add(-100*day+time.Hour)) {
		t.Fatalf("forge = %+v", forge)
	}
	if !quiet.Dormant || quiet.LastCommitAt != nil || quiet.LastSessionAt != nil || quiet.OpenTickets != 0 {
		t.Fatalf("quiet = %+v", quiet)
	}

	// --project prints the one object /work reads.
	out, err = runProjectState(t, "--project", "forge", "--json")
	if err != nil {
		t.Fatalf("project-state --project forge: %v", err)
	}
	var one summaries.ProjectState
	if err := json.Unmarshal([]byte(out), &one); err != nil || one.Project != "forge" || !one.Dormant {
		t.Fatalf("--project forge = %v %+v\n%s", err, one, out)
	}
	if out, err := runProjectState(t); err != nil || !strings.Contains(out, "window 30d") ||
		!strings.Contains(out, "forge          dormant") {
		t.Fatalf("text output = %v\n%s", err, out)
	}
	if _, err := runProjectState(t, "--project", "other"); err == nil || !strings.Contains(err.Error(), `"other"`) {
		t.Fatalf("untracked project: err = %v, want it named", err)
	}

	// A narrower window is a flag, not a rebuild of the code: alpha's commit two
	// days back falls out of a 36-hour window.
	foldOnce(t, db, 36*time.Hour)
	out, err = runProjectState(t, "--project", "alpha", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(out), &one); err != nil || one.CommitsInWindow != 1 || one.TicketsClosedInWindow != 0 {
		t.Fatalf("alpha over 36h = %+v", one)
	}

	// A rebuild that cannot read tk, a knowledge store with no truths/, or one
	// whose truths/ holds no scope leaves the last rows standing and says so;
	// the fold itself still succeeds.
	logs.Reset()
	t.Setenv("PATH", t.TempDir())
	foldOnce(t, db, 30*day)
	// A tk that runs and fails: its stderr reaches the log without -v, the
	// mode the launchd summarizer runs in.
	failing := t.TempDir()
	if err := os.WriteFile(filepath.Join(failing, "tk"), []byte("#!/bin/sh\necho 'store is locked' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", failing)
	if err := summarize.Run(summarize.Options{ReceivedDir: t.TempDir(), DBPath: db, StateWindow: 30 * day}); err != nil {
		t.Fatalf("summarize: %v", err)
	}
	if !strings.Contains(logs.String(), "store is locked — previous rows kept") {
		t.Fatalf("non-verbose tk failure dropped tk's stderr:\n%s", logs)
	}
	t.Setenv("LOOM_KNOWLEDGE_ROOT", t.TempDir())
	foldOnce(t, db, 30*day)
	noScopes := t.TempDir()
	if err := os.MkdirAll(filepath.Join(noScopes, "truths", "_archive"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LOOM_KNOWLEDGE_ROOT", noScopes)
	foldOnce(t, db, 30*day)
	if !strings.Contains(logs.String(), "knowledge store has no scopes — previous rows kept") {
		t.Fatalf("scope-less knowledge store not reported:\n%s", logs)
	}
	if n := strings.Count(logs.String(), "previous rows kept"); n != 4 {
		t.Fatalf("failed rebuilds logged %d time(s), want 4:\n%s", n, logs)
	}
	out, err = runProjectState(t, "--project", "alpha", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(out), &one); err != nil || one.CommitsInWindow != 1 || one.WindowSeconds != int64(36*time.Hour/time.Second) {
		t.Fatalf("alpha after failed rebuilds = %+v, want the 36h rows kept", one)
	}
}

// No database, no table and an empty table are errors: each would otherwise
// read as loom tracking no project.
func TestProjectStateRefusesMissingState(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LOOM_HOME", home)
	if _, err := runProjectState(t); err == nil || !strings.Contains(err.Error(), "summaries.db") {
		t.Fatalf("missing db: err = %v", err)
	}
	st, err := summaries.Open(filepath.Join(home, "summaries.db"))
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	if _, err := runProjectState(t); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("empty table: err = %v", err)
	}
}
