package summaries

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"loom/internal/parse/summary"
)

// TestExtractCommits exercises the pure extractor directly: every documented
// commit-line shape must parse, a failed commit and a bracketed line from a
// non-bash tool must not, and stat lines populate files_changed.
func TestExtractCommits(t *testing.T) {
	at := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)
	calls := []summary.ToolCall{
		{Kind: summary.KindBash, StartedAt: at, ResultSummary: "[main 2bbeb99] Release v1.2.2\n 1 file changed, 2 insertions(+)"},
		{Kind: summary.KindBash, StartedAt: at, ResultSummary: "[loom/persist-receiver-token-cbe1 ac13670] Persist receiver token to a file"},
		{Kind: summary.KindBash, StartedAt: at, ResultSummary: "[main (root-commit) abc1234] Initial commit"},
		{Kind: summary.KindBash, StartedAt: at, ResultSummary: "[detached HEAD abc1234] Some subject"},
		// Failed commit — git printed no confirmation line.
		{Kind: summary.KindBash, StartedAt: at, ResultSummary: "nothing to commit, working tree clean"},
		// Bracketed line from a non-bash tool must be ignored.
		{Kind: summary.KindRead, StartedAt: at, ResultSummary: "[main deadbee] not a commit"},
	}

	recs := extractCommits(calls)
	if len(recs) != 4 {
		t.Fatalf("extractCommits: got %d records, want 4", len(recs))
	}

	want := []struct {
		branch  string
		hash    string
		subject string
	}{
		{"main", "2bbeb99", "Release v1.2.2"},
		{"loom/persist-receiver-token-cbe1", "ac13670", "Persist receiver token to a file"},
		{"main (root-commit)", "abc1234", "Initial commit"},
		{"detached HEAD", "abc1234", "Some subject"},
	}
	for i, w := range want {
		if recs[i].branch != w.branch || recs[i].commitHash != w.hash || recs[i].subject != w.subject {
			t.Errorf("rec %d: got {%q %q %q}, want {%q %q %q}", i,
				recs[i].branch, recs[i].commitHash, recs[i].subject,
				w.branch, w.hash, w.subject)
		}
	}
	if recs[0].filesChanged == nil || *recs[0].filesChanged != 1 {
		t.Errorf("rec 0 filesChanged: got %v, want 1", recs[0].filesChanged)
	}
	if recs[1].filesChanged != nil {
		t.Errorf("rec 1 filesChanged: got %v, want nil", recs[1].filesChanged)
	}
}

// TestExtractCommitsMultiplePerCall confirms one bash call that commits twice
// yields two records, each carrying its own stat line.
func TestExtractCommitsMultiplePerCall(t *testing.T) {
	out := "[main aaaaaaa] first\n 1 file changed, 1 insertion(+)\n" +
		"[main bbbbbbb] second\n 3 files changed, 9 insertions(+)"
	recs := extractCommits([]summary.ToolCall{
		{Kind: summary.KindBash, ResultSummary: out},
	})
	if len(recs) != 2 {
		t.Fatalf("got %d records, want 2", len(recs))
	}
	if recs[0].commitHash != "aaaaaaa" || recs[1].commitHash != "bbbbbbb" {
		t.Errorf("hashes: got %q, %q", recs[0].commitHash, recs[1].commitHash)
	}
	if recs[0].filesChanged == nil || *recs[0].filesChanged != 1 {
		t.Errorf("rec 0 filesChanged: got %v, want 1", recs[0].filesChanged)
	}
	if recs[1].filesChanged == nil || *recs[1].filesChanged != 3 {
		t.Errorf("rec 1 filesChanged: got %v, want 3", recs[1].filesChanged)
	}
}

// TestWriteSummaryCommits writes a session whose bash output contains commits
// and asserts the commits table mirrors them, while a failed commit and a
// non-bash bracketed line are excluded.
func TestWriteSummaryCommits(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(filepath.Join(dir, "summaries.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	ctx := context.Background()
	at := time.Date(2026, 6, 26, 9, 30, 0, 0, time.UTC)
	src := SourceInfo{
		Project:   "loom",
		Path:      "/tmp/loom/s.jsonl",
		Size:      10,
		Mtime:     at,
		GitRemote: "https://github.com/EnderRealm/loom.git",
		CwdRaw:    "/Users/steve/code/loom",
	}
	sum := &summary.SessionSummary{
		SessionID: "sess-commits",
		Agent:     summary.AgentClaude,
		StartTime: at,
		EndTime:   at.Add(time.Minute),
		ToolCalls: []summary.ToolCall{
			{Kind: summary.KindBash, StartedAt: at, ResultSummary: "[main 2bbeb99] Release v1.2.2\n 1 file changed, 2 insertions(+)"},
			{Kind: summary.KindBash, StartedAt: at.Add(time.Second), ResultSummary: "nothing to commit, working tree clean"},
			{Kind: summary.KindRead, StartedAt: at, ResultSummary: "[main deadbee] not a commit"},
		},
	}
	if err := st.WriteSummary(ctx, sum, src); err != nil {
		t.Fatalf("WriteSummary: %v", err)
	}

	var n int
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM commits`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("commits count: got %d, want 1", n)
	}

	var hash, branch, subject, committedAt, gitRemote string
	row := st.DB().QueryRow(`SELECT commit_hash, branch, subject, committed_at, git_remote FROM commits`)
	if err := row.Scan(&hash, &branch, &subject, &committedAt, &gitRemote); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if hash != "2bbeb99" || branch != "main" || subject != "Release v1.2.2" {
		t.Errorf("row: got {%q %q %q}, want {2bbeb99 main Release v1.2.2}", hash, branch, subject)
	}
	if committedAt != at.UTC().Format(time.RFC3339Nano) {
		t.Errorf("committed_at: got %q, want %q", committedAt, at.UTC().Format(time.RFC3339Nano))
	}
	if gitRemote != src.GitRemote {
		t.Errorf("git_remote: got %q, want %q", gitRemote, src.GitRemote)
	}

	// Re-folding the same session must not duplicate commit rows.
	if err := st.WriteSummary(ctx, sum, src); err != nil {
		t.Fatalf("WriteSummary (re-fold): %v", err)
	}
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM commits`).Scan(&n); err != nil {
		t.Fatalf("count after re-fold: %v", err)
	}
	if n != 1 {
		t.Errorf("commits count after re-fold: got %d, want 1", n)
	}
}

// TestWriteSummaryCommitZeroTimestamp guards the failure the NOT NULL
// constraint used to cause: a bash call whose StartedAt is the zero time
// (transcript record carried no parseable timestamp) must not abort the
// session write, and its committed_at must fall back to the session start so
// the commit stays inside the 24h window.
func TestWriteSummaryCommitZeroTimestamp(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(filepath.Join(dir, "summaries.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	ctx := context.Background()
	start := time.Date(2026, 6, 26, 8, 0, 0, 0, time.UTC)
	src := SourceInfo{Project: "loom", Path: "/tmp/loom/z.jsonl", GitRemote: "https://github.com/EnderRealm/loom.git"}
	sum := &summary.SessionSummary{
		SessionID: "sess-zero-ts",
		Agent:     summary.AgentClaude,
		StartTime: start,
		EndTime:   start.Add(time.Minute),
		ToolCalls: []summary.ToolCall{
			// StartedAt left as the zero value on purpose.
			{Kind: summary.KindBash, ResultSummary: "[main 1234abc] Commit with no call timestamp"},
		},
	}
	if err := st.WriteSummary(ctx, sum, src); err != nil {
		t.Fatalf("WriteSummary with zero-timestamp commit: %v", err)
	}

	var committedAt string
	if err := st.DB().QueryRow(`SELECT committed_at FROM commits`).Scan(&committedAt); err != nil {
		t.Fatalf("scan committed_at: %v", err)
	}
	if committedAt != start.UTC().Format(time.RFC3339Nano) {
		t.Errorf("committed_at: got %q, want session-start fallback %q",
			committedAt, start.UTC().Format(time.RFC3339Nano))
	}
}

// TestWriteSummaryCommitNullTimestamp covers the pathological all-zero path:
// both the bash call's StartedAt and the session StartTime are zero, so there
// is no timestamp to fall back to. The write must still succeed (the nullable
// committed_at column accepts NULL) rather than aborting the session, and the
// row lands with a NULL committed_at the 24h reader simply skips.
func TestWriteSummaryCommitNullTimestamp(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(filepath.Join(dir, "summaries.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	ctx := context.Background()
	sum := &summary.SessionSummary{
		SessionID: "sess-null-ts",
		Agent:     summary.AgentClaude,
		// StartTime and EndTime left zero on purpose.
		ToolCalls: []summary.ToolCall{
			{Kind: summary.KindBash, ResultSummary: "[main 9999aaa] No timestamp anywhere"},
		},
	}
	if err := st.WriteSummary(ctx, sum, SourceInfo{Project: "loom"}); err != nil {
		t.Fatalf("WriteSummary with all-zero timestamps: %v", err)
	}

	var committedAt sql.NullString
	if err := st.DB().QueryRow(`SELECT committed_at FROM commits`).Scan(&committedAt); err != nil {
		t.Fatalf("scan committed_at: %v", err)
	}
	if committedAt.Valid {
		t.Errorf("committed_at: got %q, want NULL", committedAt.String)
	}
}
