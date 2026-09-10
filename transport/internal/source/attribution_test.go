package source

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeRegistry lays down a stamp registry under a fresh LOOM_HOME and
// returns that root.
func writeRegistry(t *testing.T, lines ...string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("LOOM_HOME", home)
	body := strings.Join(lines, "\n")
	if body != "" {
		body += "\n"
	}
	if err := os.WriteFile(filepath.Join(home, attributionFile), []byte(body), 0o644); err != nil {
		t.Fatalf("write registry: %v", err)
	}
	return home
}

func record(workRoot, project, at string) string {
	b, _ := json.Marshal(stamp{WorkRoot: workRoot, ProjectCwd: project, StampedAt: at})
	return string(b)
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return parsed
}

func TestProjectCwdResolvesStampedWorkRoot(t *testing.T) {
	writeRegistry(t, record("/var/folders/x7/T/tmp.aaa", "/Users/steve/code/warp", "2026-09-09T18:00:00Z"))
	idx := loadStamps()

	got := idx.projectCwd("/var/folders/x7/T/tmp.aaa", mustTime(t, "2026-09-09T18:00:05Z"))
	if want := "/Users/steve/code/warp"; got != want {
		t.Errorf("projectCwd = %q, want %q", got, want)
	}
	if got := idx.projectCwd("/var/folders/x7/T/tmp.other", mustTime(t, "2026-09-09T18:00:05Z")); got != "" {
		t.Errorf("unstamped work root resolved to %q, want \"\"", got)
	}
}

// A work root can recur: mktemp names are random but finite, and a reused
// name must not re-point an older session at a newer project. The newest
// record at or before the session's start is the one that describes it.
func TestProjectCwdPicksNewestStampAtOrBeforeStart(t *testing.T) {
	root := "/var/folders/x7/T/tmp.reused"
	writeRegistry(t,
		record(root, "/Users/steve/code/loom", "2026-09-01T10:00:00Z"),
		record(root, "/Users/steve/code/ticket", "2026-09-05T10:00:00Z"),
		record(root, "/Users/steve/code/warp", "2026-09-09T10:00:00Z"),
	)
	idx := loadStamps()

	cases := []struct {
		start string
		want  string
	}{
		{"2026-09-01T10:00:00Z", "/Users/steve/code/loom"},   // exactly at the stamp
		{"2026-09-03T00:00:00Z", "/Users/steve/code/loom"},   // between the first two
		{"2026-09-05T10:00:01Z", "/Users/steve/code/ticket"}, // just after the second
		{"2026-09-20T00:00:00Z", "/Users/steve/code/warp"},   // after all of them
		{"2026-08-01T00:00:00Z", ""},                         // before all of them
	}
	for _, c := range cases {
		if got := idx.projectCwd(root, mustTime(t, c.start)); got != c.want {
			t.Errorf("start %s: projectCwd = %q, want %q", c.start, got, c.want)
		}
	}
}

// Records arrive in whatever order the producers appended them; ordering is
// the index's job, not the file's.
func TestProjectCwdOrdersOutOfOrderRecords(t *testing.T) {
	root := "/var/folders/x7/T/tmp.unordered"
	writeRegistry(t,
		record(root, "/Users/steve/code/warp", "2026-09-09T10:00:00Z"),
		record(root, "/Users/steve/code/loom", "2026-09-01T10:00:00Z"),
	)
	idx := loadStamps()

	if got, want := idx.projectCwd(root, mustTime(t, "2026-09-02T00:00:00Z")), "/Users/steve/code/loom"; got != want {
		t.Errorf("projectCwd = %q, want %q", got, want)
	}
}

// A session with no usable start time still resolves: without a clock to
// compare against, the newest record is the best available answer.
func TestProjectCwdWithoutStartTakesNewest(t *testing.T) {
	root := "/var/folders/x7/T/tmp.noclock"
	writeRegistry(t,
		record(root, "/Users/steve/code/loom", "2026-09-01T10:00:00Z"),
		record(root, "/Users/steve/code/warp", "2026-09-09T10:00:00Z"),
	)
	idx := loadStamps()

	if got, want := idx.projectCwd(root, time.Time{}), "/Users/steve/code/warp"; got != want {
		t.Errorf("projectCwd = %q, want %q", got, want)
	}
}

// A stamp is an additional source of identity and must never be a new way to
// fail: every unusable registry resolves nothing and reports no error.
func TestLoadStampsDegradesQuietly(t *testing.T) {
	t.Run("no-file", func(t *testing.T) {
		t.Setenv("LOOM_HOME", t.TempDir())
		if idx := loadStamps(); len(idx) != 0 {
			t.Errorf("missing registry gave %d entries, want 0", len(idx))
		}
	})

	t.Run("garbage-and-partial-records", func(t *testing.T) {
		writeRegistry(t,
			"not json at all",
			`{"work_root":"/var/folders/x7/T/tmp.a"}`,  // no project
			`{"project_cwd":"/Users/steve/code/warp"}`, // no work root
			`{"work_root":"","project_cwd":""}`,        // both empty
			"",                                         // blank line
			record("/var/folders/x7/T/tmp.good", "/Users/steve/code/warp", "2026-09-09T18:00:00Z"),
		)
		idx := loadStamps()
		if got, want := idx.projectCwd("/var/folders/x7/T/tmp.good", mustTime(t, "2026-09-09T18:00:01Z")), "/Users/steve/code/warp"; got != want {
			t.Errorf("good record after garbage: got %q, want %q", got, want)
		}
		if got := idx.projectCwd("/var/folders/x7/T/tmp.a", mustTime(t, "2026-09-09T18:00:01Z")); got != "" {
			t.Errorf("partial record resolved to %q, want \"\"", got)
		}
	})

	// An unparseable stamped_at leaves the record usable — it just sorts
	// before every dated one — so a producer with a broken clock format
	// still attributes its runs.
	t.Run("unparseable-timestamp", func(t *testing.T) {
		root := "/var/folders/x7/T/tmp.undated"
		writeRegistry(t, record(root, "/Users/steve/code/warp", "whenever"))
		idx := loadStamps()
		if got, want := idx.projectCwd(root, mustTime(t, "2026-09-09T18:00:00Z")), "/Users/steve/code/warp"; got != want {
			t.Errorf("projectCwd = %q, want %q", got, want)
		}
	})
}

func TestIsEphemeralCwd(t *testing.T) {
	// TMPDIR is honored so a machine whose scratch space is elsewhere is
	// covered without listing its layout here.
	t.Setenv("TMPDIR", "/scratch/steve")

	cases := []struct {
		cwd  string
		want bool
	}{
		{"/var/folders/x7/jntw/T/tmp.abc", true},
		{"/private/var/folders/x7/jntw/T/tmp.abc", true},
		{"/tmp/whatever", true},
		{"/private/tmp/claude-501/scratchpad", true},
		{"/var/tmp/thing", true},
		{"/scratch/steve/tmp.abc", true},
		{"/Users/steve/code/warp", false},
		{"/Users/steve/code/tmp-tools", false}, // not under a temp root, just named like one
		{"/scratch/steve", false},              // the temp root itself is not a run's work root
		{"/", false},
		{"", false},
	}
	for _, c := range cases {
		if got := isEphemeralCwd(c.cwd); got != c.want {
			t.Errorf("isEphemeralCwd(%q) = %v, want %v", c.cwd, got, c.want)
		}
	}
}
