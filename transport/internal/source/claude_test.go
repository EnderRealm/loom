package source

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
