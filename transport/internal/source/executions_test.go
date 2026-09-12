package source

import (
	"os"
	"path/filepath"
	"testing"
)

// The registry ships as one session under the host's slug, and only once
// something has been written to it.
func TestExecutionsAdapterListsRegistry(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LOOM_HOME", home)

	sessions, err := executionsAdapter{}.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("got %d sessions with no registry, want 0", len(sessions))
	}

	path := filepath.Join(home, executionsFile)
	if err := os.WriteFile(path, []byte(`{"v":1,"kind":"run","run_id":"r1"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write registry: %v", err)
	}
	sessions, err = executionsAdapter{}.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("got %d sessions, want 1", len(sessions))
	}
	s := sessions[0]
	if s.Path != path {
		t.Errorf("Path = %q, want %q", s.Path, path)
	}
	if s.SessionID != executionsSessionID {
		t.Errorf("SessionID = %q, want %q", s.SessionID, executionsSessionID)
	}
	host, _ := os.Hostname()
	if want := encodeProjectPath(host); s.Project != want {
		t.Errorf("Project = %q, want %q", s.Project, want)
	}
	if s.Cwd != "" {
		t.Errorf("Cwd = %q, want empty (no identity sidecar for a registry)", s.Cwd)
	}
	if s.Subagent != nil {
		t.Error("Subagent set, want nil")
	}
	if s.Key() != executionsSessionID {
		t.Errorf("Key = %q, want %q", s.Key(), executionsSessionID)
	}
}

func TestAdaptersIncludeExecutions(t *testing.T) {
	for _, ad := range Adapters() {
		if ad.Agent() == ExecutionsAgent {
			return
		}
	}
	t.Fatalf("Adapters() lacks %q", ExecutionsAgent)
}
