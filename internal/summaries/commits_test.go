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

// TestExtractCommitsQuietCommit covers commits made with "git commit -q",
// which prints no bracket line: the "<hash> <subject>" line a follow-up
// "git log --oneline -1" prints is taken instead, but only for a call whose
// command runs git commit and wrote that subject, only the first such line,
// and never a push range or diff index line.
func TestExtractCommitsQuietCommit(t *testing.T) {
	at := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	quiet := "cd /Users/steve/code/loom\ngit add -A\ngit commit -q -F - <<'EOF'\n" +
		"[loom/add-claude-opus-3917] Price claude-opus-5-5 at its published rates\n\nBody.\nEOF\n" +
		"echo \"commit-exit=$?\"\ngit log --oneline -1"
	calls := []summary.ToolCall{
		// The /work shape: exit echo, oneline, then work-candidate.sh's check line.
		{Kind: summary.KindBash, StartedAt: at, KeyArg: quiet, ResultSummary: "commit-exit=0\n" +
			"787b923 [loom/add-claude-opus-3917] Price claude-opus-5-5 at its published rates\n" +
			"work-candidate.sh: commit 787b923c90510c76b69badc2ecda7bb50f03915b carries candidate 8e741d873f32's tree cd2b27b0\n" +
			"check-exit=0"},
		// Trailing-marker subject, via git -C.
		{Kind: summary.KindBash, StartedAt: at,
			KeyArg:        "git -C /Users/steve/code/loom commit -q -m 'Add Codex and Cursor pricing rates [loom/add-pricing-rates-04d8]' && git log --oneline -1",
			ResultSummary: "aaa23b5 Add Codex and Cursor pricing rates [loom/add-pricing-rates-04d8]"},
		// A push range and a diff index line precede the commit line; neither
		// matches, and only the first oneline line counts.
		{Kind: summary.KindBash, StartedAt: at, KeyArg: "git -c core.hooksPath=/dev/null commit -q -am 'release: v8.7.0'\ngit push\ngit log --oneline -2",
			ResultSummary: "To github.com:EnderRealm/ticket.git\n   4a15f51..0ae1262  master -> master\n" +
				"4a15f51..0ae1262  master -> master\nindex 6ada054..d173914 100644\n" +
				"0ae1262 release: v8.7.0\n4a15f51 [ticket/older-0001] Older commit"},
		// Long global options before the subcommand.
		{Kind: summary.KindBash, StartedAt: at,
			KeyArg:        "git --git-dir=/r/.git --work-tree=/r --no-pager commit -q -m 'Commit through long options' && git log --oneline -1",
			ResultSummary: "5a5a5a5 Commit through long options"},
		// The command was cut at the key-argument limit mid-subject.
		{Kind: summary.KindBash, StartedAt: at,
			KeyArg:        "cd /r\ngit add Sources\ngit commit -q -m \"[weft/record-weft-worker-dd8b] Record the weft-wor…",
			ResultSummary: "26895a2 [weft/record-weft-worker-dd8b] Record the weft-worker build revision on the run log's started line"},
		// The echoed subject was cut at the result limit.
		{Kind: summary.KindBash, StartedAt: at,
			KeyArg:        "git commit -q -m '[weft/open-weft-dashboard-3b61] Open weft on a dashboard' && git log --oneline -1",
			ResultSummary: " M Sources/Weft/RunStore.swift\n73926fe [weft/open…"},
		// A failed quiet commit: the echo prints the HEAD already there, whose
		// subject the command didn't write.
		{Kind: summary.KindBash, StartedAt: at,
			KeyArg:        "git commit -q -m '[loom/new-work-0001] Do the new work'; git log --oneline -1",
			ResultSummary: "nothing to commit, working tree clean\n0b4698d [loom/fold-ticket-state-99fe] Add synthesis-input builder"},
		// Cut before enough of the subject survives to show the command wrote it.
		{Kind: summary.KindBash, StartedAt: at,
			KeyArg:        "git commit -q -m \"[weft/rec…",
			ResultSummary: "1111111 [weft/record-weft-worker-dd8b] Record the weft-worker build revision"},
		// An indented line (a diff context line) can't pose as the commit.
		{Kind: summary.KindBash, StartedAt: at, KeyArg: "git diff\ngit commit -q -m 'context line from a diff'",
			ResultSummary: " abc1234 context line from a diff"},
		// No commit in the command: oneline output is a log, not a commit.
		{Kind: summary.KindBash, StartedAt: at, KeyArg: "git log --oneline -3",
			ResultSummary: "0b4698d [loom/fold-ticket-state-99fe] Add synthesis-input builder\n787b923 Older"},
		// Plumbing that prints a bare hash is not a commit.
		{Kind: summary.KindBash, StartedAt: at, KeyArg: "git commit-tree HEAD^{tree} -m 'not a commit line'",
			ResultSummary: "1234567 not a commit line"},
		// A denied commit printed nothing oneline-shaped.
		{Kind: summary.KindBash, StartedAt: at, KeyArg: quiet,
			ResultSummary: "work-gate: commit denied — no lens dispatch recorded this session"},
		// A bracket line wins; the oneline echo after it is not a second commit.
		{Kind: summary.KindBash, StartedAt: at, KeyArg: "git commit -m x && git log --oneline -1",
			ResultSummary: "[main c33d065] Show partial run costs [loom/run-cost-reads-2571]\n 4 files changed\nc33d065 Show partial run costs [loom/run-cost-reads-2571]"},
	}

	recs := extractCommits(calls)
	want := []struct {
		branch  string
		hash    string
		subject string
	}{
		{"", "787b923", "[loom/add-claude-opus-3917] Price claude-opus-5-5 at its published rates"},
		{"", "aaa23b5", "Add Codex and Cursor pricing rates [loom/add-pricing-rates-04d8]"},
		{"", "0ae1262", "release: v8.7.0"},
		{"", "5a5a5a5", "Commit through long options"},
		{"", "26895a2", "[weft/record-weft-worker-dd8b] Record the weft-worker build revision on the run log's started line"},
		{"", "73926fe", "[weft/open…"},
		{"main", "c33d065", "Show partial run costs [loom/run-cost-reads-2571]"},
	}
	if len(recs) != len(want) {
		t.Fatalf("extractCommits: got %d records %+v, want %d", len(recs), recs, len(want))
	}
	for i, w := range want {
		if recs[i].branch != w.branch || recs[i].commitHash != w.hash || recs[i].subject != w.subject {
			t.Errorf("rec %d: got {%q %q %q}, want {%q %q %q}", i,
				recs[i].branch, recs[i].commitHash, recs[i].subject,
				w.branch, w.hash, w.subject)
		}
	}
	if !recs[0].committedAt.Equal(at) {
		t.Errorf("rec 0 committedAt: got %v, want %v", recs[0].committedAt, at)
	}
	if recs[0].filesChanged != nil {
		t.Errorf("rec 0 filesChanged: got %v, want nil", *recs[0].filesChanged)
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
