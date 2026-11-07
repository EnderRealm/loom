package source

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"loom/internal/config"
)

// TestReadClaudeCwdSparseFirstLine pins the bug that 882a7aa fixed:
// Claude session JSONLs frequently start with sparse-headered records
// (permission-mode, file-history-snapshot, agent-name, ...) that carry
// no cwd field. readClaudeCwd must keep scanning until a header-bearing
// record answers, otherwise the shipper emits Session{Cwd: ""} and
// capture pass skips writing the identity sidecar.
func TestReadClaudeCwdSparseFirstLine(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "first-line-has-cwd",
			body: `{"type":"user","cwd":"/Users/steve/code/loom","sessionId":"s1","timestamp":"2026-04-27T00:00:00Z"}` + "\n",
			want: "/Users/steve/code/loom",
		},
		{
			name: "permission-mode-then-system",
			body: `{"type":"permission-mode","sessionId":"s1","permissionMode":"acceptEdits"}` + "\n" +
				`{"type":"system","subtype":"info","sessionId":"s1","cwd":"/Users/steve/code/loom","timestamp":"2026-04-27T00:00:00Z"}` + "\n",
			want: "/Users/steve/code/loom",
		},
		{
			name: "many-sparse-headers-then-cwd",
			body: `{"type":"permission-mode","sessionId":"s1","permissionMode":"acceptEdits"}` + "\n" +
				`{"type":"file-history-snapshot","messageId":"m1","snapshot":{}}` + "\n" +
				`{"type":"agent-name","sessionId":"s1","agentName":"main"}` + "\n" +
				`{"type":"custom-title","sessionId":"s1","customTitle":"hi"}` + "\n" +
				`{"type":"user","cwd":"/Users/steve/code/loom","sessionId":"s1","timestamp":"2026-04-27T00:00:00Z"}` + "\n",
			want: "/Users/steve/code/loom",
		},
		{
			name: "malformed-line-then-cwd",
			body: `{not valid json}` + "\n" +
				`{"type":"user","cwd":"/Users/steve/code/loom","sessionId":"s1"}` + "\n",
			want: "/Users/steve/code/loom",
		},
		{
			name: "empty-cwd-fields-then-real-one",
			body: `{"type":"permission-mode","cwd":"","sessionId":"s1"}` + "\n" +
				`{"type":"user","cwd":"/Users/steve/code/loom","sessionId":"s1"}` + "\n",
			want: "/Users/steve/code/loom",
		},
		{
			name: "empty-file",
			body: "",
			want: "",
		},
		{
			name: "no-cwd-anywhere",
			body: `{"type":"permission-mode","sessionId":"s1"}` + "\n" +
				`{"type":"agent-name","sessionId":"s1","agentName":"x"}` + "\n",
			want: "",
		},
		{
			name: "partial-trailing-line",
			body: `{"type":"user","cwd":"/Users/steve/code/loom","sessionId":"s1"}` + "\n" +
				`{"type":"system","cwd":"/other"`, // no closing brace, no newline
			want: "/Users/steve/code/loom",
		},
		{
			name: "only-partial-line",
			body: `{"type":"user","cwd":"/Users/steve/code/loom"`, // never finished
			want: "",
		},
	}

	dir := t.TempDir()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := filepath.Join(dir, c.name+".jsonl")
			if err := os.WriteFile(p, []byte(c.body), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := readClaudeCwd(p)
			if err != nil {
				t.Fatalf("readClaudeCwd: %v", err)
			}
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

// writeClaudeSession lays out one project/session pair under a fake
// ~/.claude/projects tree and returns the project slug.
func writeClaudeSession(t *testing.T, home, project, sessionID, cwd string) {
	t.Helper()
	dir := filepath.Join(home, ".claude", "projects", project)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := `{"type":"user","cwd":"` + cwd + `","sessionId":"` + sessionID + `"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, sessionID+".jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// writeClaudeSubagent writes one subagent transcript, plus its sidecar when
// meta is non-empty.
func writeClaudeSubagent(t *testing.T, home, project, parentSessionID, agentID, cwd, meta string) {
	t.Helper()
	dir := filepath.Join(home, ".claude", "projects", project, parentSessionID, "subagents")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// sessionId on a subagent record is the PARENT's uuid.
	body := `{"type":"user","cwd":"` + cwd + `","sessionId":"` + parentSessionID + `"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, agentID+".jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if meta == "" {
		return
	}
	if err := os.WriteFile(filepath.Join(dir, agentID+".meta.json"), []byte(meta), 0o600); err != nil {
		t.Fatal(err)
	}
}

func findSession(sessions []Session, sessionID string) *Session {
	for i := range sessions {
		if sessions[i].SessionID == sessionID {
			return &sessions[i]
		}
	}
	return nil
}

// The bug this fixes: ListClaudeSessions skipped every directory entry in a
// project dir, so the subagents/ subtree never shipped at all.
func TestListClaudeSessionsEnumeratesSubagents(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("LOOM_HOME", t.TempDir())
	const project = "-Users-steve-code-loom"
	const parent = "195f819e-1e11-4e08-8c16-a340f512f892"
	writeClaudeSession(t, home, project, parent, "/Users/steve/code/loom")
	writeClaudeSubagent(t, home, project, parent, "agent-a0e0c89b977fd6273", "/Users/steve/code/loom",
		`{"agentType":"reviewer","description":"Contract lens round 3","toolUseId":"toolu_015BC3bRz7V19vyVDAMXf5FX","spawnDepth":1}`)
	// The sidecar is the agent's to write: a transcript without one must
	// still ship, never be skipped.
	writeClaudeSubagent(t, home, project, parent, "agent-b11115ce", "/Users/steve/code/warp", "")

	sessions, err := ListClaudeSessions()
	if err != nil {
		t.Fatalf("ListClaudeSessions: %v", err)
	}
	if len(sessions) != 3 {
		t.Fatalf("got %d sessions, want 3 (1 top-level + 2 subagents): %+v", len(sessions), sessions)
	}

	top := findSession(sessions, parent)
	if top == nil {
		t.Fatal("top-level session missing")
	}
	if top.Subagent != nil {
		t.Errorf("top-level session has Subagent %+v, want nil", top.Subagent)
	}
	if got, want := top.Key(), parent; got != want {
		t.Errorf("top-level Key() = %q, want %q", got, want)
	}

	withMeta := findSession(sessions, "agent-a0e0c89b977fd6273")
	if withMeta == nil {
		t.Fatal("subagent with sidecar missing")
	}
	if withMeta.Project != project {
		t.Errorf("Project = %q, want %q", withMeta.Project, project)
	}
	if withMeta.Cwd != "/Users/steve/code/loom" {
		t.Errorf("Cwd = %q, want the subagent's own cwd", withMeta.Cwd)
	}
	want := Subagent{
		ParentSessionID: parent,
		AgentType:       "reviewer",
		Description:     "Contract lens round 3",
		ToolUseID:       "toolu_015BC3bRz7V19vyVDAMXf5FX",
		SpawnDepth:      1,
	}
	if withMeta.Subagent == nil || *withMeta.Subagent != want {
		t.Errorf("Subagent = %+v, want %+v", withMeta.Subagent, want)
	}
	if got, want := withMeta.Key(), parent+".agent-a0e0c89b977fd6273"; got != want {
		t.Errorf("subagent Key() = %q, want %q", got, want)
	}

	noMeta := findSession(sessions, "agent-b11115ce")
	if noMeta == nil {
		t.Fatal("subagent without sidecar missing — it must still ship")
	}
	if noMeta.Subagent == nil {
		t.Fatal("Subagent nil for a sidecar-less subagent")
	}
	if noMeta.Subagent.ParentSessionID != parent {
		t.Errorf("ParentSessionID = %q, want %q", noMeta.Subagent.ParentSessionID, parent)
	}
	if noMeta.Subagent.AgentType != "" || noMeta.Subagent.ToolUseID != "" {
		t.Errorf("sidecar-less subagent carries metadata: %+v", noMeta.Subagent)
	}
	if noMeta.Cwd != "/Users/steve/code/warp" {
		t.Errorf("Cwd = %q, want the subagent's own cwd", noMeta.Cwd)
	}
}

// A session that dispatched nothing must enumerate exactly as it does today.
func TestListClaudeSessionsWithoutSubagentsDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("LOOM_HOME", t.TempDir())
	const project = "-Users-steve-code-loom"
	const sid = "195f819e-1e11-4e08-8c16-a340f512f892"
	writeClaudeSession(t, home, project, sid, "/Users/steve/code/loom")

	sessions, err := ListClaudeSessions()
	if err != nil {
		t.Fatalf("ListClaudeSessions: %v", err)
	}
	want := []Session{{
		Project:   project,
		SessionID: sid,
		Path:      filepath.Join(home, ".claude", "projects", project, sid+".jsonl"),
		Cwd:       "/Users/steve/code/loom",
	}}
	if len(sessions) != 1 || sessions[0] != want[0] {
		t.Fatalf("got %+v, want %+v", sessions, want)
	}
}

// writeClaudeWorkflowSubagent writes one workflow-nested subagent transcript
// at subagents/workflows/<wfID>/<agentID>.jsonl, plus its sidecar when meta
// is non-empty.
func writeClaudeWorkflowSubagent(t *testing.T, home, project, parentSessionID, wfID, agentID, cwd, meta string) {
	t.Helper()
	dir := filepath.Join(home, ".claude", "projects", project, parentSessionID, "subagents", "workflows", wfID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := `{"type":"user","cwd":"` + cwd + `","sessionId":"` + parentSessionID + `"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, agentID+".jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if meta == "" {
		return
	}
	if err := os.WriteFile(filepath.Join(dir, agentID+".meta.json"), []byte(meta), 0o600); err != nil {
		t.Fatal(err)
	}
}

// Claude Code also nests subagent transcripts one level deeper, under
// subagents/workflows/<wf_id>/ — 178 of them across 13 workflow dirs on the
// measured machine. A single-level listing shipped none of those.
func TestListClaudeSessionsEnumeratesWorkflowSubagents(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("LOOM_HOME", t.TempDir())
	const project = "-Users-steve-code-loom"
	const parent = "195f819e-1e11-4e08-8c16-a340f512f892"
	const wf = "wf_5daf2eee-720"
	writeClaudeSession(t, home, project, parent, "/Users/steve/code/loom")
	writeClaudeSubagent(t, home, project, parent, "agent-a0e0", "/Users/steve/code/loom", "")
	writeClaudeWorkflowSubagent(t, home, project, parent, wf, "agent-a0e0", "/Users/steve/code/warp",
		`{"agentType":"workflow-subagent","spawnDepth":1}`)
	// A workflow dir whose name ends in a dot flattens to an id containing
	// "..", which the receiver's identifier guard refuses: enumerating it
	// would 400 forever without advancing a cursor.
	writeClaudeWorkflowSubagent(t, home, project, parent, "wf_5daf2eee-720.", "agent-b1b1", "/Users/steve/code/warp", "")
	// Workflow bookkeeping: no dispatch, no sidecar, must not ship.
	journalDir := filepath.Join(home, ".claude", "projects", project, parent, "subagents", "workflows", wf)
	if err := os.WriteFile(filepath.Join(journalDir, "journal.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	sessions, err := ListClaudeSessions()
	if err != nil {
		t.Fatalf("ListClaudeSessions: %v", err)
	}
	if len(sessions) != 3 {
		t.Fatalf("got %d sessions, want 3 (1 top-level + flat + nested): %+v", len(sessions), sessions)
	}

	nested := findSession(sessions, "workflows."+wf+".agent-a0e0")
	if nested == nil {
		t.Fatal("workflow-nested subagent missing")
	}
	if nested.Path != filepath.Join(journalDir, "agent-a0e0.jsonl") {
		t.Errorf("Path = %q, want the nested transcript", nested.Path)
	}
	if nested.Cwd != "/Users/steve/code/warp" {
		t.Errorf("Cwd = %q, want the nested transcript's own cwd", nested.Cwd)
	}
	wantSub := Subagent{ParentSessionID: parent, AgentType: "workflow-subagent", SpawnDepth: 1}
	if nested.Subagent == nil || *nested.Subagent != wantSub {
		t.Errorf("Subagent = %+v, want %+v", nested.Subagent, wantSub)
	}
	// The namespaced id keeps the nested transcript off the flat one's
	// staging file and cursor.
	if got, want := nested.Key(), parent+".workflows."+wf+".agent-a0e0"; got != want {
		t.Errorf("nested Key() = %q, want %q", got, want)
	}

	flat := findSession(sessions, "agent-a0e0")
	if flat == nil {
		t.Fatal("flat subagent missing — the nested one must not displace it")
	}
	if got, want := flat.Key(), parent+".agent-a0e0"; got != want {
		t.Errorf("flat Key() = %q, want %q", got, want)
	}

	for _, s := range sessions {
		if strings.Contains(s.SessionID, "journal") {
			t.Errorf("journal.jsonl enumerated as a subagent: %+v", s)
		}
		if strings.Contains(s.SessionID, "..") {
			t.Errorf("id the receiver would reject was enumerated: %+v", s)
		}
	}
}

const (
	spoolProject = "-Users-weftagent1-work-loom"
	spoolSession = "7c1f0a52-92ab-4d3c-8f6e-0d4a7b1e2c33"
	spoolCwd     = "/Users/weftagent1/work/loom"
)

// useSpool points HOME and LOOM_HOME at fresh directories and returns HOME and
// the spool root. The root is created, as the worker's first copy would.
func useSpool(t *testing.T) (home, spool string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("LOOM_HOME", t.TempDir())
	spool = config.ClaudeSpoolDir()
	if err := os.MkdirAll(spool, 0o700); err != nil {
		t.Fatal(err)
	}
	return home, spool
}

// writeTranscript writes a one-record transcript at path, creating its parents.
func writeTranscript(t *testing.T, path, cwd string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	body := `{"type":"user","cwd":"` + cwd + `"}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

func fifo(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
}

func captureListLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	orig := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(orig) })
	return &buf
}

// A hosted run's transcripts arrive through the spool, in the same layout as
// the operator's own tree, and list beside it through the same code.
func TestListClaudeSessionsEnumeratesSpool(t *testing.T) {
	home, spool := useSpool(t)
	writeClaudeSession(t, home, "-Users-steve-code-loom", "195f819e-1e11-4e08-8c16-a340f512f892", "/Users/steve/code/loom")
	top := filepath.Join(spool, spoolProject, spoolSession+".jsonl")
	writeTranscript(t, top, spoolCwd)
	sub := filepath.Join(spool, spoolProject, spoolSession, "subagents", "agent-a0e0.jsonl")
	writeTranscript(t, sub, spoolCwd)
	meta := `{"agentType":"reviewer","toolUseId":"toolu_1","spawnDepth":1}`
	if err := os.WriteFile(strings.TrimSuffix(sub, ".jsonl")+".meta.json", []byte(meta), 0o600); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(spool, spoolProject, spoolSession, "subagents", "workflows", "wf_1", "agent-b1b1.jsonl")
	writeTranscript(t, nested, spoolCwd)
	journal := filepath.Join(filepath.Dir(nested), claudeWorkflowJournal)
	if err := os.WriteFile(journal, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	sessions, err := claudeAdapter{}.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(sessions) != 4 {
		t.Fatalf("got %d sessions, want operator + spooled top-level + 2 subagents: %+v", len(sessions), sessions)
	}

	got := findSession(sessions, spoolSession)
	if got == nil {
		t.Fatal("spooled session missing")
	}
	want := Session{Project: spoolProject, SessionID: spoolSession, Path: top, Cwd: spoolCwd}
	if *got != want {
		t.Errorf("spooled session = %+v, want %+v", *got, want)
	}

	flat := findSession(sessions, "agent-a0e0")
	if flat == nil {
		t.Fatal("spooled subagent missing")
	}
	wantSub := Subagent{ParentSessionID: spoolSession, AgentType: "reviewer", ToolUseID: "toolu_1", SpawnDepth: 1}
	if flat.Path != sub || flat.Subagent == nil || *flat.Subagent != wantSub {
		t.Errorf("spooled subagent = %+v (%+v), want path %q and %+v", flat, flat.Subagent, sub, wantSub)
	}
	if findSession(sessions, "workflows.wf_1.agent-b1b1") == nil {
		t.Error("spooled workflow subagent missing")
	}
}

// A machine that has run nothing hosted has no spool, at any depth of its path.
func TestListClaudeSessionsMissingSpool(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("LOOM_HOME", t.TempDir())
	buf := captureListLog(t)

	sessions, err := ListClaudeSessions()
	if err != nil || sessions != nil {
		t.Fatalf("no trees: got %+v, %v", sessions, err)
	}
	writeClaudeSession(t, home, "-Users-steve-code-loom", spoolSession, "/Users/steve/code/loom")
	sessions, err = ListClaudeSessions()
	if err != nil || len(sessions) != 1 {
		t.Fatalf("operator tree only: got %+v, %v", sessions, err)
	}
	if buf.Len() != 0 {
		t.Errorf("a missing spool logged: %s", buf)
	}
}

// A spool that cannot be read is a failure to surface, not a reason to stop
// capturing what the operator's own agents wrote.
func TestListClaudeSessionsUnreadableSpoolKeepsOperatorSessions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("LOOM_HOME", t.TempDir())
	writeClaudeSession(t, home, "-Users-steve-code-loom", spoolSession, "/Users/steve/code/loom")
	spool := config.ClaudeSpoolDir()
	if err := os.MkdirAll(filepath.Dir(spool), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(spool, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	sessions, err := ListClaudeSessions()
	if err == nil {
		t.Fatal("no error for a spool root that is a file")
	}
	if len(sessions) != 1 {
		t.Fatalf("got %+v, want the operator's session alongside the error", sessions)
	}
}

// Each case plants one entry that is not a regular file. outside holds real
// transcripts at the paths the links aim for, so a listing that followed one
// would surface a session the assertion catches rather than an empty directory.
// A FIFO cannot be read at all: reaching one hangs the test.
func TestListClaudeSessionsSkipsNonRegularSpoolEntries(t *testing.T) {
	cases := []struct {
		name  string
		plant func(t *testing.T, spool, outside string)
	}{
		{"project dir symlink", func(t *testing.T, spool, outside string) {
			symlink(t, filepath.Join(outside, "project"), filepath.Join(spool, "-linked-project"))
		}},
		{"session dir symlink", func(t *testing.T, spool, outside string) {
			symlink(t, filepath.Join(outside, "session"), filepath.Join(spool, spoolProject, "linked-session"))
		}},
		{"subagents dir symlink", func(t *testing.T, spool, outside string) {
			symlink(t, filepath.Join(outside, "session", "subagents"), filepath.Join(spool, spoolProject, spoolSession, "subagents"))
		}},
		{"dir symlink below subagents", func(t *testing.T, spool, outside string) {
			symlink(t, filepath.Join(outside, "session", "subagents"), filepath.Join(spool, spoolProject, spoolSession, "subagents", "workflows"))
		}},
		{"dir symlink with a transcript's name", func(t *testing.T, spool, outside string) {
			symlink(t, filepath.Join(outside, "session", "subagents"), filepath.Join(spool, spoolProject, "linked.jsonl"))
		}},
		{"session transcript symlink", func(t *testing.T, spool, outside string) {
			symlink(t, filepath.Join(outside, "project", "victim.jsonl"), filepath.Join(spool, spoolProject, "linked.jsonl"))
		}},
		{"subagent transcript symlink", func(t *testing.T, spool, outside string) {
			symlink(t, filepath.Join(outside, "project", "victim.jsonl"), filepath.Join(spool, spoolProject, spoolSession, "subagents", "agent-linked.jsonl"))
		}},
		{"session transcript fifo", func(t *testing.T, spool, outside string) {
			fifo(t, filepath.Join(spool, spoolProject, "piped.jsonl"))
		}},
		{"subagent transcript fifo", func(t *testing.T, spool, outside string) {
			fifo(t, filepath.Join(spool, spoolProject, spoolSession, "subagents", "agent-piped.jsonl"))
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, spool := useSpool(t)
			outside := t.TempDir()
			writeTranscript(t, filepath.Join(outside, "project", "victim.jsonl"), "/victim")
			writeTranscript(t, filepath.Join(outside, "session", "subagents", "agent-victim.jsonl"), "/victim")
			control := filepath.Join(spool, spoolProject, spoolSession+".jsonl")
			writeTranscript(t, control, spoolCwd)
			c.plant(t, spool, outside)
			buf := captureListLog(t)

			sessions, err := ListClaudeSessions()
			if err != nil {
				t.Fatalf("ListClaudeSessions: %v", err)
			}
			if len(sessions) != 1 || sessions[0].Path != control {
				t.Fatalf("got %+v, want only the control session", sessions)
			}
			if !strings.Contains(buf.String(), "skip stage=list agent="+ClaudeAgent) ||
				!strings.Contains(buf.String(), "not a regular file") {
				t.Errorf("skip not logged: %q", buf)
			}
		})
	}
}

// The sidecar is read by a different path from the transcript, and refusing it
// must cost only the metadata.
func TestListClaudeSessionsSkipsNonRegularSpoolSidecars(t *testing.T) {
	cases := []struct {
		name  string
		plant func(t *testing.T, sidecar, outside string)
	}{
		{"symlink", func(t *testing.T, sidecar, outside string) {
			symlink(t, filepath.Join(outside, "meta.json"), sidecar)
		}},
		{"fifo", func(t *testing.T, sidecar, outside string) {
			fifo(t, sidecar)
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, spool := useSpool(t)
			outside := t.TempDir()
			if err := os.WriteFile(filepath.Join(outside, "meta.json"), []byte(`{"agentType":"from-outside"}`), 0o600); err != nil {
				t.Fatal(err)
			}
			transcript := filepath.Join(spool, spoolProject, spoolSession, "subagents", "agent-a0e0.jsonl")
			writeTranscript(t, transcript, spoolCwd)
			c.plant(t, strings.TrimSuffix(transcript, ".jsonl")+".meta.json", outside)
			buf := captureListLog(t)

			sessions, err := ListClaudeSessions()
			if err != nil {
				t.Fatalf("ListClaudeSessions: %v", err)
			}
			if len(sessions) != 1 || sessions[0].Subagent == nil {
				t.Fatalf("got %+v, want the transcript listed", sessions)
			}
			if got, want := *sessions[0].Subagent, (Subagent{ParentSessionID: spoolSession}); got != want {
				t.Errorf("Subagent = %+v, want %+v", got, want)
			}
			if !strings.Contains(buf.String(), "not a regular file") {
				t.Errorf("skip not logged: %q", buf)
			}
		})
	}
}

// The rule is the spool's alone: a link in the operator's own tree reads as it
// always has.
func TestListClaudeSessionsOperatorTreeKeepsSymlinks(t *testing.T) {
	home, _ := useSpool(t)
	outside := filepath.Join(t.TempDir(), "elsewhere.jsonl")
	writeTranscript(t, outside, "/Users/steve/code/loom")
	symlink(t, outside, filepath.Join(home, ".claude", "projects", "-Users-steve-code-loom", spoolSession+".jsonl"))

	sessions, err := ListClaudeSessions()
	if err != nil {
		t.Fatalf("ListClaudeSessions: %v", err)
	}
	if len(sessions) != 1 || sessions[0].SessionID != spoolSession {
		t.Fatalf("got %+v, want the linked session", sessions)
	}
}

// An agent names the files it writes, so a name the receiver refuses reaches
// the spool as readily as a link does; shipping one fails on every tick.
func TestListClaudeSessionsSkipsUnsafeSpoolNames(t *testing.T) {
	for _, rel := range []string{
		filepath.Join("a..b", spoolSession+".jsonl"),
		filepath.Join("a\nb", spoolSession+".jsonl"),
		filepath.Join(spoolProject, `a\b.jsonl`),
		filepath.Join(spoolProject, "a\nforged line.jsonl"),
		filepath.Join(spoolProject, ".jsonl"),
		filepath.Join(spoolProject, "a..b", "subagents", "agent-a0e0.jsonl"),
		filepath.Join(spoolProject, "a\nb", "subagents", "agent-a0e0.jsonl"),
		filepath.Join(spoolProject, spoolSession, "subagents", ".jsonl"),
		filepath.Join(spoolProject, spoolSession, "subagents", "agent\nforged.jsonl"),
		filepath.Join(spoolProject, spoolSession, "subagents", "workflows", "wf\n1", "agent-a0e0.jsonl"),
	} {
		t.Run(rel, func(t *testing.T) {
			_, spool := useSpool(t)
			writeTranscript(t, filepath.Join(spool, rel), spoolCwd)
			buf := captureListLog(t)

			sessions, err := ListClaudeSessions()
			if err != nil {
				t.Fatalf("ListClaudeSessions: %v", err)
			}
			if len(sessions) != 0 {
				t.Fatalf("got %+v, want nothing listed", sessions)
			}
			if !strings.Contains(buf.String(), "name the receiver rejects") {
				t.Errorf("skip not logged: %q", buf)
			}
			if strings.Contains(buf.String(), "\nforged") {
				t.Errorf("a name forged a log line: %q", buf)
			}
		})
	}
}

// Cursors key on the session alone, so a key listed twice would run one offset
// through two files. Whichever tree a clash spans, the first listed keeps the
// key and the operator's tree is listed first. The clashing project differs on
// purpose: a staging path carries the project and would not reveal the clash.
func TestListClaudeSessionsCollidingKeys(t *testing.T) {
	const parent = "195f819e-1e11-4e08-8c16-a340f512f892"
	home, spool := useSpool(t)
	operator := filepath.Join(home, ".claude", "projects", "-Users-steve-code-loom")
	writeClaudeSession(t, home, "-Users-steve-code-loom", parent, "/Users/steve/code/loom")
	writeClaudeSubagent(t, home, "-Users-steve-code-loom", parent, "agent-a0e0", "/Users/steve/code/loom", "")
	// Same session, same subagent, and a top-level name spelling a subagent key.
	writeTranscript(t, filepath.Join(spool, spoolProject, parent+".jsonl"), spoolCwd)
	writeTranscript(t, filepath.Join(spool, spoolProject, parent, "subagents", "agent-a0e0.jsonl"), spoolCwd)
	writeTranscript(t, filepath.Join(spool, spoolProject, parent+".agent-a0e0.jsonl"), spoolCwd)
	// Two spooled projects carrying one session.
	first := filepath.Join(spool, "-a-first", spoolSession+".jsonl")
	writeTranscript(t, first, spoolCwd)
	writeTranscript(t, filepath.Join(spool, "-b-second", spoolSession+".jsonl"), spoolCwd)
	buf := captureListLog(t)

	sessions, err := ListClaudeSessions()
	if err != nil {
		t.Fatalf("ListClaudeSessions: %v", err)
	}
	want := map[string]string{
		parent:                 filepath.Join(operator, parent+".jsonl"),
		parent + ".agent-a0e0": filepath.Join(operator, parent, "subagents", "agent-a0e0.jsonl"),
		spoolSession:           first,
	}
	if len(sessions) != len(want) {
		t.Fatalf("got %d sessions, want %d: %+v", len(sessions), len(want), sessions)
	}
	for _, s := range sessions {
		if s.Path != want[s.Key()] {
			t.Errorf("key %q listed from %q, want %q", s.Key(), s.Path, want[s.Key()])
		}
	}
	if got := strings.Count(buf.String(), "key already listed"); got != 4 {
		t.Errorf("logged %d collisions, want 4: %s", got, buf)
	}
}
