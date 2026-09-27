package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loom/internal/parse/summary"
	"loom/internal/summaries"
	"loom/internal/synthesis"
)

// fakeTK puts a tk on PATH that answers `query --all-projects` with jsonl and
// `--project=<ns> show --metadata <ids>` with one document per id, read from
// docs/<bare id>.md and separated as tk separates them. Every call is appended
// to calls.log so a test can see what was asked.
func fakeTK(t *testing.T, jsonl string, docs map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "query.jsonl"), []byte(jsonl), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	for bare, doc := range docs {
		if err := os.WriteFile(filepath.Join(dir, "docs", bare+".md"), []byte(doc), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	script := `#!/bin/sh
dir="` + dir + `"
echo "$*" >> "$dir/calls.log"
case "$1" in
query) cat "$dir/query.jsonl" ;;
--project=*)
	shift 3
	first=1
	for id in "$@"; do
		[ $first = 1 ] || echo
		first=0
		cat "$dir/docs/${id#*/}.md" || exit 1
	done ;;
*) echo "unexpected: $*" >&2; exit 2 ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "tk"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir
}

// tkStoreFor binds repoDir to namespace ns in a throwaway tk config, so a
// session whose cwd is repoDir resolves to ns and nothing reads the machine's
// own store.
func tkStoreFor(t *testing.T, ns, repoDir string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	t.Setenv("TK_STORE_ROOT", root)
	local := filepath.Join(root, ".ticket", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(local, []byte("central_root: "+root+"\nprojects:\n  "+ns+":\n    path: "+repoDir+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "config.yaml"), []byte("projects:\n  "+ns+":\n    store: central\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func runSynthesisInput(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	cmd := newSynthesisInputCmd()
	cmd.SetArgs(args)
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	err := cmd.Execute()
	return out.String(), errOut.String(), err
}

func ticketDoc(bare, title, body string) string {
	return "---\nid: " + bare + "\nstatus: done\ntype: feature\n---\n# " + title + "\n\n" + body + "\n"
}

func TestSynthesisInputClassifiesTicketsAndJoinsSessions(t *testing.T) {
	now := time.Now().UTC()
	ts := func(ago time.Duration) string { return now.Add(-ago).Format(time.RFC3339) }
	day := 24 * time.Hour

	repo := t.TempDir()
	other := t.TempDir()
	tkStoreFor(t, "loom", repo)

	lines := []string{
		// Closed in the window, created before it.
		`{"id":"loom/done-0001","status":"done","type":"feature","parent":"loom/epic-0100","title":"Done","created":"` + ts(40*day) + `","updated":"` + ts(2*day) + `","closed":"` + ts(2*day) + `"}`,
		// Created in the window, never closed, and no session behind it.
		`{"id":"loom/new-0002","status":"ready","type":"bug","title":"New","created":"` + ts(3*day) + `","updated":"` + ts(3*day) + `"}`,
		// Created and closed in the window: both signals.
		`{"id":"loom/both-0003","status":"closed","type":"feature","title":"Both","created":"` + ts(5*day) + `","updated":"` + ts(4*day) + `","closed":"` + ts(4*day) + `"}`,
		// Written in the window, neither created nor closed in it.
		`{"id":"loom/edited-0004","status":"open","type":"feature","title":"Edited","created":"` + ts(60*day) + `","updated":"` + ts(1*day) + `"}`,
		// Written before tk stored updated and closed: the keys are absent, and
		// absent is not in the window.
		`{"id":"loom/legacy-0005","status":"done","type":"feature","title":"Legacy","created":"` + ts(90*day) + `"}`,
		// tk's zero created date for a file that records none.
		`{"id":"loom/nodate-0006","status":"backlog","type":"feature","title":"No date","created":"0001-01-01T00:00:00Z"}`,
		// Another namespace's ticket inside the window.
		`{"id":"warp/done-0007","status":"done","type":"feature","title":"Warp","created":"` + ts(2*day) + `","closed":"` + ts(1*day) + `"}`,
		// Everything before the window.
		`{"id":"loom/old-0009","status":"done","type":"feature","title":"Old","created":"` + ts(50*day) + `","updated":"` + ts(45*day) + `","closed":"` + ts(45*day) + `"}`,
	}
	docs := map[string]string{
		"done-0001": ticketDoc("done-0001", "Done", "Why it mattered.\n\n---\nid: not-a-boundary\n\n## Design\n\nThe confirmed scope.\n\n## Acceptance Criteria\n\n- It ships\n  verify: go test ./...\n\n## Test Results\n\nverify: 1 pass"),
		"new-0002":  ticketDoc("new-0002", "New", "Still needed."),
		// A body reproducing the frontmatter of the ticket requested after it: read
		// in one batched show, it would become done-0001's description and criteria.
		"both-0003": ticketDoc("both-0003", "Both", "Rejected.\n\n## Acceptance Criteria\n\n- Never mind\n\n"+
			"---\nid: done-0001\nstatus: done\n---\n# Done\n\nInjected description.\n\n## Acceptance Criteria\n\n- Injected criterion"),
		// A heading inside a fence is text, not a section.
		"edited-0004": ticketDoc("edited-0004", "Edited", "Moved scope.\n\n```\n## not a heading\n```"),
	}
	tkDir := fakeTK(t, strings.Join(lines, "\n")+"\n", docs)

	home := t.TempDir()
	t.Setenv("LOOM_HOME", home)
	st, err := summaries.Open(filepath.Join(home, "summaries.db"))
	if err != nil {
		t.Fatal(err)
	}
	commit := func(at time.Time, subject string) summary.ToolCall {
		return summary.ToolCall{Kind: summary.KindBash, StartedAt: at, ResultSummary: "[main abc1234] " + subject}
	}
	session := func(id, cwd string, start time.Time, calls ...summary.ToolCall) {
		sum := &summary.SessionSummary{SessionID: id, Agent: summary.AgentClaude,
			StartTime: start, EndTime: start.Add(time.Hour), ToolCalls: calls}
		if err := st.WriteSummary(context.Background(), sum, summaries.SourceInfo{Path: "/tmp/" + id + ".jsonl", CwdRaw: cwd}); err != nil {
			t.Fatalf("WriteSummary %s: %v", id, err)
		}
	}
	// The session that closed done-0001, with an unmarked commit beside it.
	session("closer", repo, now.Add(-2*day),
		commit(now.Add(-2*day), "Tidy up"),
		commit(now.Add(-2*day+time.Minute), "[loom/done-0001] Ship it"))
	// A session in another checkout still joins by its marker.
	session("elsewhere", other, now.Add(-4*day), commit(now.Add(-4*day), "[loom/both-0003] Drop it"))
	// Project sessions in the window with nothing for a listed ticket: one
	// marked for a ticket outside the window, one with no commits at all.
	session("stray", repo, now.Add(-6*day), commit(now.Add(-6*day), "[loom/old-0009] Late follow-up"))
	session("idle", repo, now.Add(-1*day))
	// Out of the window, and in the window but another project's.
	session("stale", repo, now.Add(-45*day))
	session("foreign", other, now.Add(-1*day))
	st.Close()

	out, stderr, err := runSynthesisInput(t, "--project", "loom", "--since", "30d")
	if err != nil {
		t.Fatalf("synthesis-input: %v\nstderr: %s", err, stderr)
	}
	var in synthesis.Input
	if err := json.Unmarshal([]byte(out), &in); err != nil {
		t.Fatalf("output is not one JSON object: %v\n%s", err, out)
	}
	if in.Project != "loom" || in.Until.Sub(in.Since) != 30*day {
		t.Fatalf("project/window = %q %v..%v, want loom over 30 days", in.Project, in.Since, in.Until)
	}

	got := map[string]synthesis.Ticket{}
	var ids []string
	for _, tk := range in.Tickets {
		got[tk.ID] = tk
		ids = append(ids, tk.ID+"="+strings.Join(tk.Signals, ","))
	}
	want := "loom/both-0003=new,done loom/done-0001=done loom/edited-0004=edited loom/new-0002=new"
	if strings.Join(ids, " ") != want {
		t.Fatalf("tickets = %v, want %s", ids, want)
	}

	done := got["loom/done-0001"]
	if done.Parent != "loom/epic-0100" || done.Closed == nil || done.Created == nil || done.Updated == nil {
		t.Fatalf("done-0001 fields = %+v", done)
	}
	if !strings.HasPrefix(done.Description, "Why it mattered.") || !strings.Contains(done.Description, "id: not-a-boundary") {
		t.Fatalf("description = %q, want the body before the first section, rule included", done.Description)
	}
	if done.Design != "The confirmed scope." || done.AcceptanceCriteria != "- It ships\n  verify: go test ./..." {
		t.Fatalf("design/acceptance = %q / %q", done.Design, done.AcceptanceCriteria)
	}
	if both := got["loom/both-0003"]; !strings.HasPrefix(both.AcceptanceCriteria, "- Never mind") || both.Description != "Rejected." {
		t.Fatalf("both-0003 description/acceptance = %q / %q, want its own", both.Description, both.AcceptanceCriteria)
	}
	if d := got["loom/edited-0004"].Description; d != "Moved scope.\n\n```\n## not a heading\n```" {
		t.Fatalf("edited-0004 description = %q, want the fenced heading kept as text", d)
	}
	if len(done.Sessions) != 1 || done.Sessions[0].SessionID != "closer" ||
		len(done.Sessions[0].Commits) != 1 || done.Sessions[0].Commits[0].Subject != "[loom/done-0001] Ship it" {
		t.Fatalf("done-0001 sessions = %+v, want closer with its marked commit alone", done.Sessions)
	}
	if s := got["loom/both-0003"].Sessions; len(s) != 1 || s[0].SessionID != "elsewhere" {
		t.Fatalf("both-0003 sessions = %+v, want elsewhere", s)
	}
	if s := got["loom/new-0002"].Sessions; s == nil || len(s) != 0 {
		t.Fatalf("new-0002 sessions = %+v, want kept with an empty list", s)
	}

	var unticketed []string
	for _, s := range in.UnticketedSessions {
		unticketed = append(unticketed, s.SessionID)
	}
	if strings.Join(unticketed, ",") != "stray,idle" {
		t.Fatalf("unticketed = %v, want stray,idle (by start)", unticketed)
	}
	if c := in.UnticketedSessions[0].Commits; len(c) != 1 || c[0].Subject != "[loom/old-0009] Late follow-up" {
		t.Fatalf("stray commits = %+v", c)
	}
	if !strings.Contains(stderr, "4 with a signal in the window") {
		t.Fatalf("stderr carries no counts: %s", stderr)
	}

	// One show for the signalled tickets, never one for a ticket outside it.
	calls, err := os.ReadFile(filepath.Join(tkDir, "calls.log"))
	if err != nil {
		t.Fatal(err)
	}
	wantCalls := "query --all-projects\n" +
		"--project=loom show --metadata loom/both-0003\n" +
		"--project=loom show --metadata loom/done-0001\n" +
		"--project=loom show --metadata loom/edited-0004\n" +
		"--project=loom show --metadata loom/new-0002\n"
	if string(calls) != wantCalls {
		t.Fatalf("tk calls = %q, want %q", calls, wantCalls)
	}
}

// A tk that is not there, and a summaries.db that is not there, are errors: an
// empty answer from either would read as a project that did nothing.
func TestSynthesisInputRefusesMissingSources(t *testing.T) {
	t.Setenv("LOOM_HOME", t.TempDir())

	path := os.Getenv("PATH")
	t.Setenv("PATH", t.TempDir())
	if _, _, err := runSynthesisInput(t, "--project", "loom", "--since", "30d"); err == nil || !strings.Contains(err.Error(), "tk") {
		t.Fatalf("missing tk: err = %v, want a tk error", err)
	}

	t.Setenv("PATH", path)
	fakeTK(t, `{"id":"loom/a-0001","status":"ready","type":"bug","title":"A","created":"`+time.Now().UTC().Format(time.RFC3339)+`"}`+"\n",
		map[string]string{"a-0001": ticketDoc("a-0001", "A", "Body.")})
	if _, _, err := runSynthesisInput(t, "--project", "loom", "--since", "30d"); err == nil || !strings.Contains(err.Error(), "summaries.db") {
		t.Fatalf("missing summaries.db: err = %v, want a summaries.db error", err)
	}
}

// A namespace with no ticket is far likelier a typo than a project, and an
// empty --since has no window to read.
func TestSynthesisInputRefusesUnknownProjectAndEmptyWindow(t *testing.T) {
	t.Setenv("LOOM_HOME", t.TempDir())
	fakeTK(t, `{"id":"loom/a-0001","status":"ready","type":"bug","title":"A","created":"`+time.Now().UTC().Format(time.RFC3339)+`"}`+"\n", nil)
	if _, _, err := runSynthesisInput(t, "--project", "lomo", "--since", "30d"); err == nil || !strings.Contains(err.Error(), `"lomo"`) {
		t.Fatalf("unknown project: err = %v, want it named", err)
	}
	if _, _, err := runSynthesisInput(t, "--project", "loom"); err == nil || err.Error() != "--since is required" {
		t.Fatalf("empty --since: err = %v", err)
	}
}

func TestSynthesisInputWindowFlag(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want time.Duration
	}{{"30d", 30 * 24 * time.Hour}, {"72h", 72 * time.Hour}, {"90m", 90 * time.Minute}} {
		got, err := parseWindow(tc.in)
		if err != nil || got != tc.want {
			t.Fatalf("parseWindow(%q) = %v, %v; want %v", tc.in, got, err, tc.want)
		}
	}
	for _, bad := range []string{"", "0d", "-3d", "d", "30", "-1h"} {
		if _, err := parseWindow(bad); err == nil {
			t.Fatalf("parseWindow(%q) accepted", bad)
		}
	}
}
