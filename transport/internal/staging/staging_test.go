package staging

import (
	"os"
	"path/filepath"
	"testing"

	"loom/transport/internal/source"
)

const (
	testAgent   = "claude-code"
	testProject = "-Users-steve-code-loom"
	testParent  = "195f819e-1e11-4e08-8c16-a340f512f892"
)

// A subagent stages under its parent — mirroring the receiver layout — and
// lists back with everything the ship pass needs to rebuild the payload.
func TestListReturnsSubagentsAlongsideSessions(t *testing.T) {
	t.Setenv("LOOM_HOME", t.TempDir())

	if err := Append(testAgent, testProject, "", testParent, []byte("{}\n")); err != nil {
		t.Fatal(err)
	}
	if err := WriteIdentity(testAgent, testProject, "", testParent, Identity{
		GitRemote: "git@github.com:EnderRealm/loom.git",
		Cwd:       "/Users/steve/code/loom",
		RootSlug:  testProject,
	}); err != nil {
		t.Fatal(err)
	}
	if err := Append(testAgent, testProject, testParent, "agent-a0e0", []byte("{}\n")); err != nil {
		t.Fatal(err)
	}
	sub := Subagent{
		ParentSessionID: testParent,
		AgentType:       "reviewer",
		Description:     "Contract lens round 3",
		ToolUseID:       "toolu_015BC3bRz7V19vyVDAMXf5FX",
		SpawnDepth:      1,
	}
	if err := WriteSubagent(testAgent, testProject, testParent, "agent-a0e0", sub); err != nil {
		t.Fatal(err)
	}
	// No sidecar for this one — it must still list, with its parent
	// recovered from the directory name.
	if err := Append(testAgent, testProject, testParent, "agent-b111", []byte("{}\n")); err != nil {
		t.Fatal(err)
	}

	want := filepath.Join(Dir(), testAgent, testProject, testParent, "subagents", "agent-a0e0.jsonl")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("subagent not staged under its parent: %v", err)
	}

	entries, err := List(testAgent)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("got %d entries, want 3: %+v", len(entries), entries)
	}
	byKey := map[string]Entry{}
	for _, e := range entries {
		byKey[e.Key()] = e
	}

	top, ok := byKey[testParent]
	if !ok {
		t.Fatalf("top-level session missing, keys: %v", byKey)
	}
	if top.Subagent != nil {
		t.Errorf("top-level Subagent = %+v, want nil", top.Subagent)
	}
	if top.Identity.Cwd != "/Users/steve/code/loom" {
		t.Errorf("top-level Identity = %+v, want the staged cwd", top.Identity)
	}

	withMeta, ok := byKey[testParent+".agent-a0e0"]
	if !ok {
		t.Fatalf("subagent with sidecar missing, keys: %v", byKey)
	}
	if withMeta.Subagent == nil || *withMeta.Subagent != sub {
		t.Errorf("Subagent = %+v, want %+v", withMeta.Subagent, sub)
	}
	if withMeta.Path != want {
		t.Errorf("Path = %q, want %q", withMeta.Path, want)
	}

	noMeta, ok := byKey[testParent+".agent-b111"]
	if !ok {
		t.Fatalf("sidecar-less subagent missing, keys: %v", byKey)
	}
	if noMeta.Subagent == nil || *noMeta.Subagent != (Subagent{ParentSessionID: testParent}) {
		t.Errorf("Subagent = %+v, want parent only", noMeta.Subagent)
	}
}

// Size and cursor keys must separate a subagent from a bare uuid of the
// same name, and from the same agent id under a different parent.
func TestSubagentKeysDoNotCollide(t *testing.T) {
	t.Setenv("LOOM_HOME", t.TempDir())

	const otherParent = "3f2b7c60-0000-4000-8000-000000000000"
	if err := Append(testAgent, testProject, testParent, "agent-a0e0", []byte("{}\n")); err != nil {
		t.Fatal(err)
	}
	if err := Append(testAgent, testProject, otherParent, "agent-a0e0", []byte("{}\n{}\n")); err != nil {
		t.Fatal(err)
	}

	first, err := Size(testAgent, testProject, testParent, "agent-a0e0")
	if err != nil {
		t.Fatal(err)
	}
	second, err := Size(testAgent, testProject, otherParent, "agent-a0e0")
	if err != nil {
		t.Fatal(err)
	}
	if first != 3 || second != 6 {
		t.Fatalf("sizes = %d, %d — want 3, 6 (the two must be separate files)", first, second)
	}

	entries, err := List(testAgent)
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string]bool{}
	for _, e := range entries {
		if keys[e.Key()] {
			t.Fatalf("duplicate cursor key %q", e.Key())
		}
		keys[e.Key()] = true
	}
	if len(keys) != 2 {
		t.Fatalf("got keys %v, want 2 distinct", keys)
	}
}

// Entry.Key and source.Session.Key format the same identifier in two
// packages; if they drift, capture and ship key the same transcript
// differently and it re-ships from offset 0 forever.
func TestKeyMatchesSourceSessionKey(t *testing.T) {
	cases := []struct {
		name    string
		session source.Session
		entry   Entry
	}{
		{
			name:    "top-level",
			session: source.Session{Project: testProject, SessionID: testParent},
			entry:   Entry{Agent: testAgent, Project: testProject, SessionID: testParent},
		},
		{
			name: "subagent",
			session: source.Session{
				Project:   testProject,
				SessionID: "workflows.wf_5daf2eee-720.agent-a0e0",
				Subagent:  &source.Subagent{ParentSessionID: testParent},
			},
			entry: Entry{
				Agent:     testAgent,
				Project:   testProject,
				SessionID: "workflows.wf_5daf2eee-720.agent-a0e0",
				Subagent:  &Subagent{ParentSessionID: testParent},
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got, want := c.entry.Key(), c.session.Key(); got != want {
				t.Errorf("Entry.Key() = %q, source.Session.Key() = %q", got, want)
			}
		})
	}
}
