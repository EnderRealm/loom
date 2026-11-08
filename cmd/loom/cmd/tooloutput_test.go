package cmd

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"loom/internal/summaries"
	"loom/internal/summarize"
)

// testOutputMatch is what each fixture's long output holds past the 800
// characters summaries.db keeps; the Cursor fixture's long lens response ends
// with `response "}`.
const testOutputMatch = `teardown:|kern\.procargs2|response "\}`

// The diagnostics sit past the head the summary DB keeps.
var (
	claudeGoTest = strings.Repeat("ok  \tshuttle/internal/pkg\t0.01s\n", 40) +
		"--- FAIL: TestReap (0.20s)\n    teardown: TempDir RemoveAll cleanup: directory not empty\n"
	subagentGoTest = strings.Repeat("=== RUN   TestReapLoop\n", 50) +
		"reap: sysctl kern.procargs2: invalid argument\n"
	codexGoTest = strings.Repeat("ok  \tticket/internal/sync\t0.02s\n", 40)
	codexStderr = "--- FAIL: TestSyncParentRepo\n    teardown: rm -rf: permission denied\n"
)

func jsonLine(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b) + "\n"
}

func claudeUser(sessionID, ts, promptID, text string, sidechain bool) map[string]any {
	return map[string]any{"type": "user", "uuid": "u-" + ts, "sessionId": sessionID, "promptId": promptID,
		"isSidechain": sidechain, "timestamp": ts, "message": map[string]any{"role": "user", "content": text}}
}

func claudeToolUse(sessionID, ts, id, name string, input map[string]any, sidechain bool) map[string]any {
	return map[string]any{"type": "assistant", "uuid": "a-" + ts, "sessionId": sessionID, "isSidechain": sidechain,
		"timestamp": ts, "message": map[string]any{"role": "assistant", "stop_reason": "tool_use",
			"content": []any{map[string]any{"type": "tool_use", "id": id, "name": name, "input": input}}}}
}

func claudeToolResult(sessionID, ts, id, content string, sidechain bool) map[string]any {
	return map[string]any{"type": "user", "uuid": "r-" + ts, "sessionId": sessionID, "isSidechain": sidechain, "toolUseResult": map[string]any{"stdout": content},
		"timestamp": ts, "message": map[string]any{"role": "user",
			"content": []any{map[string]any{"type": "tool_result", "tool_use_id": id, "content": content}}}}
}

func writeFixture(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// seedToolOutputHome lays out a received tree for all three agents under a
// fresh LOOM_HOME: a Claude /work session with a dispatched subagent, an
// older Claude session outside the test window, a Codex /work rollout and the
// Cursor parent journal fixture.
func seedToolOutputHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("LOOM_HOME", home)
	received := filepath.Join(home, "received")

	parent := filepath.Join(received, "claude-code", "proj", "claude-parent.jsonl")
	writeFixture(t, parent,
		jsonLine(t, claudeUser("claude-parent", "2026-09-20T10:00:00.000Z", "p1",
			"<command-message>work</command-message>\n<command-name>/work</command-name>\n<command-args>shuttle/reap-flake-1234</command-args>", false))+
			jsonLine(t, claudeToolUse("claude-parent", "2026-09-20T10:00:05.000Z", "toolu_1", "Bash", map[string]any{"command": "go test ./..."}, false))+
			jsonLine(t, claudeToolResult("claude-parent", "2026-09-20T10:00:30.000Z", "toolu_1", claudeGoTest, false))+
			jsonLine(t, claudeToolUse("claude-parent", "2026-09-20T10:01:00.000Z", "toolu_2", "Bash", map[string]any{"command": "cat notes.txt"}, false))+
			jsonLine(t, claudeToolResult("claude-parent", "2026-09-20T10:01:01.000Z", "toolu_2", "teardown: quoted in a note", false))+
			jsonLine(t, claudeToolUse("claude-parent", "2026-09-20T10:02:00.000Z", "toolu_3", "Task",
				map[string]any{"description": "Implement the fix", "subagent_type": "coder", "prompt": "fix it"}, false))+
			jsonLine(t, claudeToolResult("claude-parent", "2026-09-20T10:09:00.000Z", "toolu_3", "done", false)))
	sub := filepath.Join(received, "claude-code", "proj", "claude-parent", "subagents", "agent-sub1.jsonl")
	writeFixture(t, sub,
		jsonLine(t, claudeUser("claude-parent", "2026-09-20T10:02:01.000Z", "sp1", "fix it", true))+
			jsonLine(t, claudeToolUse("claude-parent", "2026-09-20T10:03:00.000Z", "toolu_s1", "Bash", map[string]any{"command": "go test ./internal/reap/"}, true))+
			jsonLine(t, claudeToolResult("claude-parent", "2026-09-20T10:03:40.000Z", "toolu_s1", subagentGoTest, true)))
	writeFixture(t, strings.TrimSuffix(sub, ".jsonl")+".subagent.json", `{"agent_type":"coder","tool_use_id":"toolu_3"}`)

	writeFixture(t, filepath.Join(received, "claude-code", "proj", "claude-old.jsonl"),
		jsonLine(t, claudeUser("claude-old", "2026-09-01T08:00:00.000Z", "o1", "run the suite", false))+
			jsonLine(t, claudeToolUse("claude-old", "2026-09-01T08:00:05.000Z", "toolu_o", "Bash", map[string]any{"command": "go test ./..."}, false))+
			jsonLine(t, claudeToolResult("claude-old", "2026-09-01T08:00:30.000Z", "toolu_o", claudeGoTest, false)))

	writeFixture(t, filepath.Join(received, "codex-cli", "proj", "codex-sess.jsonl"),
		jsonLine(t, map[string]any{"timestamp": "2026-09-21T09:00:00.000Z", "type": "session_meta",
			"payload": map[string]any{"id": "codex-sess", "timestamp": "2026-09-21T09:00:00.000Z", "cwd": "/tmp/c", "cli_version": "0.160.0", "source": "cli", "model_provider": "openai"}})+
			jsonLine(t, map[string]any{"timestamp": "2026-09-21T09:00:01.000Z", "type": "turn_context",
				"payload": map[string]any{"turn_id": "t1", "cwd": "/tmp/c", "model": "gpt-5.4"}})+
			jsonLine(t, map[string]any{"timestamp": "2026-09-21T09:00:02.000Z", "type": "response_item",
				"payload": map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "#work ticket/sync-flake-5678"}}}})+
			jsonLine(t, map[string]any{"timestamp": "2026-09-21T09:00:03.000Z", "type": "response_item",
				"payload": map[string]any{"type": "function_call", "name": "shell", "call_id": "call_1", "arguments": `{"command": ["bash", "-lc", "go test ./..."]}`}})+
			jsonLine(t, map[string]any{"timestamp": "2026-09-21T09:00:40.000Z", "type": "event_msg",
				"payload": map[string]any{"type": "exec_command_end", "call_id": "call_1", "command": []string{"bash", "-lc", "go test ./..."},
					"stdout": codexGoTest, "stderr": codexStderr, "exit_code": 1, "duration": map[string]any{"secs": 37, "nanos": 0}}}))

	cursor, err := os.ReadFile(filepath.Join("..", "..", "..", "internal", "parse", "cursorparse", "testdata", "parent.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(received, "cursor-cli", "proj", "963ac97f-c33f-46de-9624-3e5946b61a0a.jsonl"), string(cursor))
	return home
}

// seedToolOutputDB folds the fixture tree into summaries.db with the real
// summarizer, so tickets are attributed from the runs it recognizes.
func seedToolOutputDB(t *testing.T, home string) {
	t.Helper()
	if err := summarize.Run(summarize.Options{
		ReceivedDir: filepath.Join(home, "received"),
		DBPath:      filepath.Join(home, "summaries.db"),
	}); err != nil {
		t.Fatal(err)
	}
}

type fileState struct {
	size  int64
	mode  fs.FileMode
	mtime time.Time
}

// statTree records every path under root with its size, mode and mtime.
func statTree(t *testing.T, root string) map[string]fileState {
	t.Helper()
	out := map[string]fileState{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		out[path] = fileState{size: info.Size(), mode: info.Mode(), mtime: info.ModTime()}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func runToolOutput(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	cmd := newToolOutputCmd()
	cmd.SetArgs(args)
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	err := cmd.Execute()
	return out.String(), errOut.String(), err
}

func decodeMatches(t *testing.T, out string) []summarize.ToolOutputMatch {
	t.Helper()
	var got []summarize.ToolOutputMatch
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("stdout is not a JSON array: %v\n%s", err, out)
	}
	return got
}

func ticketOf(m summarize.ToolOutputMatch) string {
	if m.Ticket == nil {
		return "<nil>"
	}
	return *m.Ticket
}

// failuresOf decodes stderr as Execute leaves it — the command's own lines
// plus whatever printError adds for err — requiring every line to be a JSON
// failure record.
func failuresOf(t *testing.T, stderr string, err error) []summarize.ToolOutputFailure {
	t.Helper()
	var tail bytes.Buffer
	printError(&tail, err)
	var out []summarize.ToolOutputFailure
	for _, line := range strings.Split(strings.TrimSpace(stderr+tail.String()), "\n") {
		var f summarize.ToolOutputFailure
		if err := json.Unmarshal([]byte(line), &f); err != nil {
			t.Fatalf("stderr line is not a JSON failure: %q", line)
		}
		if f.Path == "" || f.Error == "" {
			t.Errorf("failure record %+v is missing its path or error", f)
		}
		out = append(out, f)
	}
	return out
}

func TestToolOutputReturnsFullOutputAcrossAgents(t *testing.T) {
	home := seedToolOutputHome(t)
	seedToolOutputDB(t, home)
	before := statTree(t, home)

	out, stderr, err := runToolOutput(t, "--since", "2026-09-10T00:00:00Z", "--until", "2026-09-30T00:00:00Z",
		"--match", testOutputMatch, "--json")
	if err != nil {
		t.Fatalf("tool-output: %v\nstderr: %s", err, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want nothing", stderr)
	}

	got := decodeMatches(t, out)
	type key struct{ agent, session, keyArg string }
	byKey := map[key]summarize.ToolOutputMatch{}
	for _, m := range got {
		byKey[key{m.Agent, m.SessionID, m.KeyArg}] = m
	}
	want := []struct {
		key    key
		ticket string
		output string
		at     string
	}{
		{key{"claude-code", "claude-parent", "go test ./..."}, "shuttle/reap-flake-1234", claudeGoTest, "2026-09-20T10:00:05Z"},
		{key{"claude-code", "claude-parent", "cat notes.txt"}, "shuttle/reap-flake-1234", "teardown: quoted in a note", "2026-09-20T10:01:00Z"},
		{key{"claude-code", "agent-sub1", "go test ./internal/reap/"}, "shuttle/reap-flake-1234", subagentGoTest, "2026-09-20T10:03:00Z"},
		{key{"codex-cli", "codex-sess", "bash -lc go test ./..."}, "ticket/sync-flake-5678", codexGoTest + "\n" + codexStderr, "2026-09-21T09:00:03Z"},
	}
	for _, w := range want {
		m, ok := byKey[w.key]
		if !ok {
			t.Errorf("no match for %+v", w.key)
			continue
		}
		if m.Output != w.output {
			t.Errorf("%+v output = %d chars, want the full %d", w.key, len(m.Output), len(w.output))
		}
		if ticketOf(m) != w.ticket {
			t.Errorf("%+v ticket = %s, want %s", w.key, ticketOf(m), w.ticket)
		}
		if at, _ := time.Parse(time.RFC3339, w.at); !m.StartedAt.Equal(at) {
			t.Errorf("%+v started_at = %s, want %s", w.key, m.StartedAt, w.at)
		}
	}
	var cursor *summarize.ToolOutputMatch
	for i := range got {
		if got[i].Agent == "cursor-cli" {
			cursor = &got[i]
		}
	}
	if cursor == nil || len(cursor.Output) <= 800 || !strings.HasSuffix(cursor.Output, "response \"}\n```") {
		t.Errorf("cursor match = %+v, want the full lens response", cursor)
	} else if cursor.SessionID != "963ac97f-c33f-46de-9624-3e5946b61a0a" || cursor.Ticket == nil {
		t.Errorf("cursor match session = %s ticket = %s", cursor.SessionID, ticketOf(*cursor))
	}
	if len(got) != len(want)+1 {
		t.Errorf("got %d matches, want %d: %+v", len(got), len(want)+1, got)
	}
	for i := 1; i < len(got); i++ {
		if got[i].StartedAt.Before(got[i-1].StartedAt) {
			t.Errorf("matches out of start order at %d", i)
		}
	}
	for _, m := range got {
		if m.SessionID == "claude-old" {
			t.Error("a call before --since was returned")
		}
	}

	if after := statTree(t, home); !reflect.DeepEqual(before, after) {
		t.Errorf("tool-output changed the loom home:\nbefore %v\nafter  %v", before, after)
	}
}

// A running summarizer holds summaries.db open in WAL mode; the query reads
// the main file alone and leaves the writer's -wal and -shm as they are.
func TestToolOutputReadsALiveSummaryDB(t *testing.T) {
	home := seedToolOutputHome(t)
	seedToolOutputDB(t, home)
	st, err := summaries.Open(filepath.Join(home, "summaries.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SetLastSweep(time.Now()); err != nil {
		t.Fatal(err)
	}
	before := statTree(t, home)

	out, stderr, err := runToolOutput(t, "--since", "2026-09-10", "--until", "2026-09-30", "--match", testOutputMatch, "--json")
	if err != nil {
		t.Fatalf("tool-output: %v\nstderr: %s", err, stderr)
	}
	for _, m := range decodeMatches(t, out) {
		if m.Ticket == nil {
			t.Errorf("%s %s %q has no ticket", m.Agent, m.SessionID, m.KeyArg)
		}
	}
	if after := statTree(t, home); !reflect.DeepEqual(before, after) {
		t.Errorf("tool-output changed the loom home:\nbefore %v\nafter  %v", before, after)
	}
}

func TestToolOutputFiltersByCommandAndWindow(t *testing.T) {
	home := seedToolOutputHome(t)
	before := statTree(t, home)

	out, stderr, err := runToolOutput(t, "--since", "2026-09-20T00:00:00Z", "--until", "2026-09-21T00:00:00Z",
		"--match", testOutputMatch, "--command", "go test", "--json")
	if err != nil {
		t.Fatalf("tool-output: %v\nstderr: %s", err, stderr)
	}
	got := decodeMatches(t, out)
	var keys []string
	for _, m := range got {
		keys = append(keys, m.Agent+" "+m.SessionID+" "+m.KeyArg)
		// No summaries.db in this home: every ticket is unknown.
		if m.Ticket != nil {
			t.Errorf("%s %s ticket = %s, want null with no summaries.db", m.Agent, m.SessionID, *m.Ticket)
		}
	}
	want := []string{"claude-code claude-parent go test ./...", "claude-code agent-sub1 go test ./internal/reap/"}
	if !reflect.DeepEqual(keys, want) {
		t.Errorf("matches = %q, want %q", keys, want)
	}
	if !strings.Contains(out, `"ticket": null`) {
		t.Errorf("unknown ticket not rendered as null:\n%s", out)
	}

	if after := statTree(t, home); !reflect.DeepEqual(before, after) {
		t.Errorf("tool-output changed the loom home:\nbefore %v\nafter  %v", before, after)
	}
}

func TestToolOutputEmptyWindowIsAnEmptyArray(t *testing.T) {
	seedToolOutputHome(t)

	out, stderr, err := runToolOutput(t, "--since", "2026-10-01", "--until", "2026-10-02", "--match", testOutputMatch, "--json")
	if err != nil {
		t.Fatalf("tool-output: %v\nstderr: %s", err, stderr)
	}
	if strings.TrimSpace(out) != "[]" || stderr != "" {
		t.Fatalf("stdout = %q stderr = %q, want [] and nothing", out, stderr)
	}
}

func TestToolOutputReportsUnreadableTranscripts(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a mode-000 file")
	}
	home := seedToolOutputHome(t)
	locked := filepath.Join(home, "received", "codex-cli", "proj", "locked.jsonl")
	writeFixture(t, locked, "{}\n")
	lockedSub := filepath.Join(home, "received", "claude-code", "proj", "claude-parent", "subagents", "agent-locked.jsonl")
	writeFixture(t, lockedSub, "{}\n")
	for _, p := range []string{locked, lockedSub} {
		if err := os.Chmod(p, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(p, 0o644) })
	}
	before := statTree(t, home)

	out, stderr, err := runToolOutput(t, "--since", "2026-09-10", "--until", "2026-09-30", "--match", testOutputMatch, "--json")
	if err == nil {
		t.Fatal("tool-output exited 0 with unreadable transcripts")
	}

	var failed []string
	for _, f := range failuresOf(t, stderr, err) {
		failed = append(failed, f.Path)
	}
	if want := []string{lockedSub, locked}; !reflect.DeepEqual(failed, want) {
		t.Errorf("failures = %q, want %q", failed, want)
	}

	// Every readable transcript, the locked one's siblings included, still answers.
	got := decodeMatches(t, out)
	if len(got) != 5 {
		t.Errorf("got %d matches beside the unreadable files, want 5", len(got))
	}

	if after := statTree(t, home); !reflect.DeepEqual(before, after) {
		t.Errorf("tool-output changed the loom home:\nbefore %v\nafter  %v", before, after)
	}
}

// requireWALMode fails unless the database file's header records WAL mode
// (read and write format versions 2), the state in which a non-immutable
// reader creates -wal and -shm.
func requireWALMode(t *testing.T, db string) {
	t.Helper()
	b, err := os.ReadFile(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) < 20 || b[18] != 2 || b[19] != 2 {
		t.Fatalf("%s is not in WAL mode", db)
	}
}

// runImmutableQuery runs a query that must succeed with every match
// ticketed and the home tree left exactly as it was.
func runImmutableQuery(t *testing.T, home string) {
	t.Helper()
	before := statTree(t, home)
	out, stderr, err := runToolOutput(t, "--since", "2026-09-10", "--until", "2026-09-30", "--match", testOutputMatch, "--json")
	if err != nil || stderr != "" {
		t.Fatalf("tool-output: %v\nstderr: %s", err, stderr)
	}
	got := decodeMatches(t, out)
	if len(got) != 5 {
		t.Errorf("got %d matches, want 5", len(got))
	}
	for _, m := range got {
		if m.Ticket == nil {
			t.Errorf("%s %s %q has no ticket", m.Agent, m.SessionID, m.KeyArg)
		}
	}
	if after := statTree(t, home); !reflect.DeepEqual(before, after) {
		t.Errorf("tool-output changed the loom home:\nbefore %v\nafter  %v", before, after)
	}
}

// The state a summarizer leaves on shutdown: a WAL-mode database whose last
// connection removed both sidecars. Reading it creates neither.
func TestToolOutputReadsAClosedWALDB(t *testing.T) {
	home := seedToolOutputHome(t)
	seedToolOutputDB(t, home)
	db := filepath.Join(home, "summaries.db")
	requireWALMode(t, db)
	for _, side := range []string{db + "-wal", db + "-shm"} {
		if _, err := os.Stat(side); !os.IsNotExist(err) {
			t.Fatalf("%s present after the summarizer closed: %v", side, err)
		}
	}
	runImmutableQuery(t, home)
}

// A -wal with no -shm, as a restore leaves them, is read without creating
// the -shm.
func TestToolOutputReadsAWALWithoutSHM(t *testing.T) {
	home := seedToolOutputHome(t)
	seedToolOutputDB(t, home)
	db := filepath.Join(home, "summaries.db")
	requireWALMode(t, db)
	writeFixture(t, db+"-wal", "")
	runImmutableQuery(t, home)
}

// A transcript last written before --since is never opened, unless one of
// its subagent transcripts was written since.
func TestToolOutputSkipsTranscriptsWrittenBeforeTheWindow(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a mode-000 file")
	}
	home := seedToolOutputHome(t)
	old := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	stale := filepath.Join(home, "received", "codex-cli", "proj", "stale.jsonl")
	writeFixture(t, stale, "{}\n")
	parent := filepath.Join(home, "received", "claude-code", "proj", "claude-parent.jsonl")
	for _, p := range []string{stale, parent} {
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}
	// Unreadable: had it been opened, it would be reported.
	if err := os.Chmod(stale, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(stale, 0o644) })

	out, stderr, err := runToolOutput(t, "--since", "2026-09-10T00:00:00Z", "--until", "2026-09-30T00:00:00Z", "--match", testOutputMatch, "--json")
	if err != nil || stderr != "" {
		t.Fatalf("tool-output: %v\nstderr: %s", err, stderr)
	}
	found := false
	for _, m := range decodeMatches(t, out) {
		if m.SessionID == "agent-sub1" && m.Output == subagentGoTest {
			found = true
		}
	}
	if !found {
		t.Error("the fresh subagent's match was dropped with its stale parent")
	}
}

func TestToolOutputRequiresItsFlags(t *testing.T) {
	seedToolOutputHome(t)
	for _, args := range [][]string{
		{"--until", "2026-10-02", "--match", "x"},
		{"--since", "2026-10-01", "--match", "x"},
		{"--since", "2026-10-01", "--until", "2026-10-02"},
		{"--since", "2026-10-02", "--until", "2026-10-01", "--match", "x"},
		{"--since", "2026-10-01", "--until", "2026-10-02", "--match", "("},
	} {
		if _, _, err := runToolOutput(t, args...); err == nil {
			t.Errorf("tool-output %q succeeded", args)
		}
	}
}
