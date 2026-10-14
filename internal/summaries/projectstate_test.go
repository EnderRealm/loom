package summaries

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// TestLoadProjectStateEscapesURIDelimiters pins that a path carrying '#' or
// '?' is read as the file it names: no query is lost and no database appears
// at the prefix a file: URI would otherwise cut the path down to.
func TestLoadProjectStateEscapesURIDelimiters(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "summaries#a?b.db")
	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	row := ProjectState{Project: "loom", ComputedAt: time.Now().UTC(), WindowSeconds: 3600, CommitsInWindow: 2}
	if err := st.ReplaceProjectState(context.Background(), []ProjectState{row}); err != nil {
		t.Fatalf("ReplaceProjectState: %v", err)
	}
	st.Close()

	got, err := LoadProjectState(path)
	if err != nil {
		t.Fatalf("LoadProjectState: %v", err)
	}
	if len(got) != 1 || got[0].Project != "loom" || got[0].CommitsInWindow != 2 {
		t.Fatalf("LoadProjectState = %+v, want the loom row with 2 commits", got)
	}
	assertNoDatabaseAt(t, filepath.Join(dir, "summaries"))
}
