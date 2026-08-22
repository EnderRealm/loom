package tui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"loom/internal/config"
)

const candidateFixture = `---
id: loom-notify-state-default-path
title: Shipper notification state persists at $LOOM_HOME/transport/notify.state
scope: loom
type: truth
status: candidate
evidence:
  - path: transport/internal/notify/notify.go
    line: 42
    note: statePath() returns filepath.Join(config.TransportDir(), "notify.state")
sources:
  - session: n/a
    project: loom
related: []
verified_at: 2026-04-26
status: candidate
extracted_at: 2026-04-26T10:08:56
extracted_by: claude:sonnet
---

## Claim

The shipper persists notification state at $LOOM_HOME/transport/notify.state.
`

// logFixture mirrors the log.md the store bootstraps at init — the file reject
// records into, and which no tool creates.
const logFixture = `# Ingest Log

Append-only chronological record of extraction events and store changes. Format: one ` + "`## [YYYY-MM-DD]`" + ` entry per event with a one-line summary.
`

func seedCandidate(t *testing.T) (root string, art Artifact) {
	t.Helper()
	return seedCandidateIn(t, t.TempDir())
}

// seedCandidateIn seeds the fixture into the given knowledge root and points
// the store at it. LOOM_HOME is redirected too, so a gesture that can't commit
// writes knowledge-git.log into the test's own state root rather than the
// developer's ~/.loom.
func seedCandidateIn(t *testing.T, root string) (string, Artifact) {
	t.Helper()
	t.Setenv("LOOM_KNOWLEDGE_ROOT", root)
	t.Setenv("LOOM_HOME", t.TempDir())
	// Isolate every git these tests run — the code under test included — from
	// the developer's config: a global core.hooksPath (husky, a dotfiles
	// checkout) would disable the pre-commit hook a test depends on, and
	// init.templateDir would seed repos we did not ask for. Repo-local config
	// is then the only identity source.
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)

	dir := filepath.Join(root, "_candidates", "truths", "loom")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "loom-notify-state-default-path--20260426-100856.md")
	if err := os.WriteFile(path, []byte(candidateFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "log.md"), []byte(logFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	arts, err := LoadKnowledge()
	if err != nil {
		t.Fatalf("LoadKnowledge: %v", err)
	}
	if len(arts) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(arts))
	}
	return root, arts[0]
}

func TestPromoteCandidate(t *testing.T) {
	root, art := seedCandidate(t)
	if art.Status != "candidate" {
		t.Fatalf("seed status = %q, want candidate", art.Status)
	}

	dest, _, err := promoteCandidate(art)
	if err != nil {
		t.Fatalf("promoteCandidate: %v", err)
	}

	// Scope prefix stripped from the validated filename; lands in truths/loom/.
	want := filepath.Join(root, "truths", "loom", "notify-state-default-path.md")
	if dest != want {
		t.Errorf("dest = %q, want %q", dest, want)
	}
	if _, err := os.Stat(art.Path); !os.IsNotExist(err) {
		t.Errorf("source still present after promote: %v", err)
	}

	body, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	s := string(body)
	if strings.Contains(s, "status: candidate") {
		t.Errorf("promoted file still has candidate status:\n%s", s)
	}
	if strings.Count(s, "status:") != 1 {
		t.Errorf("expected exactly one status line, got %d:\n%s", strings.Count(s, "status:"), s)
	}
	if !strings.Contains(s, "status: validated") {
		t.Errorf("promoted file missing validated status:\n%s", s)
	}
	if strings.Contains(s, "extracted_at") || strings.Contains(s, "extracted_by") {
		t.Errorf("promoted file retains candidate-only provenance:\n%s", s)
	}
	if !strings.Contains(s, "verified_at:") {
		t.Errorf("promoted file missing verified_at:\n%s", s)
	}
	// Body and evidence sub-fields survive untouched.
	if !strings.Contains(s, "## Claim") || !strings.Contains(s, "statePath()") {
		t.Errorf("promoted file lost body/evidence content:\n%s", s)
	}

	// Reloading the store now classifies it as validated.
	arts, err := LoadKnowledge()
	if err != nil {
		t.Fatal(err)
	}
	if len(arts) != 1 || arts[0].Status != "validated" {
		t.Errorf("after promote, expected 1 validated artifact, got %+v", arts)
	}
}

func TestPromoteCandidateNoOverwrite(t *testing.T) {
	root, art := seedCandidate(t)
	// Pre-create the validated target.
	dir := filepath.Join(root, "truths", "loom")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	existing := filepath.Join(dir, "notify-state-default-path.md")
	if err := os.WriteFile(existing, []byte("keep me\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, _, err := promoteCandidate(art); err == nil {
		t.Fatal("expected promote to refuse overwriting an existing validated file")
	}
	if b, _ := os.ReadFile(existing); string(b) != "keep me\n" {
		t.Errorf("existing validated file was clobbered: %q", string(b))
	}
	if _, err := os.Stat(art.Path); err != nil {
		t.Errorf("candidate should remain after a refused promote: %v", err)
	}
}

func TestRejectCandidate(t *testing.T) {
	root, art := seedCandidate(t)

	dest, _, err := rejectCandidate(art)
	if err != nil {
		t.Fatalf("rejectCandidate: %v", err)
	}
	want := filepath.Join(root, "_candidates", "_rejected", "truths", "loom",
		"loom-notify-state-default-path--20260426-100856.md")
	if dest != want {
		t.Errorf("dest = %q, want %q", dest, want)
	}
	if _, err := os.Stat(art.Path); !os.IsNotExist(err) {
		t.Errorf("source still present after reject: %v", err)
	}
	// Rejected content is preserved verbatim for extractor tuning.
	body, _ := os.ReadFile(dest)
	if string(body) != candidateFixture {
		t.Errorf("rejected file content altered")
	}
	// _rejected/ is skipped by the loader, so the store now lists nothing.
	arts, err := LoadKnowledge()
	if err != nil {
		t.Fatal(err)
	}
	if len(arts) != 0 {
		t.Errorf("rejected candidate still surfaced in list: %+v", arts)
	}
}

// testGit runs a git command against a test store. gpgsign is pinned off so a
// signing key in the host's global config can't stall the seed commit.
func testGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	full := append([]string{"-C", root, "-c", "commit.gpgsign=false"}, args...)
	out, err := exec.Command("git", full...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// seedGitCandidate seeds the fixture into a knowledge root that is a git repo
// with the candidate committed, so a gesture has a HEAD to build on. Identity
// is configured repo-locally; the host's global git config is never written.
func seedGitCandidate(t *testing.T) (root string, art Artifact) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	root, art = seedCandidate(t)
	testGit(t, root, "init")
	testGit(t, root, "config", "user.email", "test@example.com")
	testGit(t, root, "config", "user.name", "loom test")
	testGit(t, root, "add", "-A")
	testGit(t, root, "commit", "-m", "seed store")
	return root, art
}

func TestPromoteCandidateCommits(t *testing.T) {
	root, art := seedGitCandidate(t)

	dest, warn, err := promoteCandidate(art)
	if err != nil {
		t.Fatalf("promoteCandidate: %v", err)
	}
	if warn != "" {
		t.Fatalf("promote in a git store did not commit: %s", warn)
	}

	subject := testGit(t, root, "log", "-1", "--format=%s")
	if !strings.Contains(subject, art.ID) || !strings.Contains(subject, art.Scope) {
		t.Errorf("commit subject %q does not name id %q and scope %q", subject, art.ID, art.Scope)
	}
	if st := testGit(t, root, "status", "--porcelain", "--", dest, art.Path); st != "" {
		t.Errorf("touched paths still dirty after promote:\n%s", st)
	}
	// --no-renames: the gesture is a write plus a delete, and rename detection
	// would fold the pair into one R line.
	names := testGit(t, root, "show", "--name-status", "--no-renames", "--format=", "HEAD")
	if !strings.Contains(names, "A\ttruths/loom/notify-state-default-path.md") {
		t.Errorf("commit does not add the promoted file:\n%s", names)
	}
	if !strings.Contains(names, "D\t_candidates/truths/loom/loom-notify-state-default-path--20260426-100856.md") {
		t.Errorf("commit does not delete the candidate:\n%s", names)
	}
}

// seedGitUntrackedCandidate seeds a git store whose candidate was never
// committed — the live store's shape, where the working tree is full of
// uncommitted candidates. An empty seed commit gives the gesture a HEAD without
// tracking anything.
func seedGitUntrackedCandidate(t *testing.T) (root string, art Artifact) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	root, art = seedCandidate(t)
	testGit(t, root, "init")
	testGit(t, root, "config", "user.email", "test@example.com")
	testGit(t, root, "config", "user.name", "loom test")
	testGit(t, root, "commit", "--allow-empty", "-m", "seed store")
	return root, art
}

func TestPromoteUntrackedCandidateCommits(t *testing.T) {
	root, art := seedGitUntrackedCandidate(t)

	dest, warn, err := promoteCandidate(art)
	if err != nil {
		t.Fatalf("promoteCandidate: %v", err)
	}
	if warn != "" {
		t.Fatalf("promote of an untracked candidate did not commit: %s", warn)
	}

	names := testGit(t, root, "show", "--name-status", "--no-renames", "--format=", "HEAD")
	if !strings.Contains(names, "A\ttruths/loom/notify-state-default-path.md") {
		t.Errorf("commit does not add the promoted file:\n%s", names)
	}
	// The candidate was never tracked, so git has nothing to delete; keeping it
	// in the pathspec after its removal would be a fatal "did not match any
	// files" that sinks the whole commit.
	if strings.Contains(names, "D\t_candidates/") {
		t.Errorf("commit deletes a candidate that was never tracked:\n%s", names)
	}
	if st := testGit(t, root, "status", "--porcelain", "--", dest); st != "" {
		t.Errorf("promoted file still dirty after promote:\n%s", st)
	}
}

// forgedID carries the shapes an extractor-written frontmatter id could smuggle
// into a commit subject or a log record: a line break, a control character, and
// the format characters that reorder a rendered record without appearing in it
// (a right-to-left override and a bidi isolate).
const forgedID = "loom-notify-state\ninjected: forged\x07\u202e\u2066"

func TestPromoteCommitSubjectIsOneLine(t *testing.T) {
	root, art := seedGitCandidate(t)
	art.ID = forgedID

	if _, warn, err := promoteCandidate(art); err != nil || warn != "" {
		t.Fatalf("promoteCandidate: err=%v warn=%s", err, warn)
	}

	body := testGit(t, root, "log", "-1", "--format=%B")
	if strings.Contains(body, "\n") {
		t.Errorf("commit message spans multiple lines:\n%q", body)
	}
	if strings.Contains(body, "\x07") {
		t.Errorf("commit message retains a control character:\n%q", body)
	}
	if strings.ContainsAny(body, "\u202e\u2066") {
		t.Errorf("commit message retains a bidi format character:\n%q", body)
	}
}

func TestPromoteLogRecordIsOneLine(t *testing.T) {
	// A non-repo store, so the gesture degrades and writes the log line.
	_, art := seedCandidate(t)
	art.ID = forgedID

	if _, warn, err := promoteCandidate(art); err != nil || warn == "" {
		t.Fatalf("promoteCandidate: err=%v warn=%q, want a degraded commit", err, warn)
	}

	logged, err := os.ReadFile(filepath.Join(config.Home(), "knowledge-git.log"))
	if err != nil {
		t.Fatalf("knowledge-git.log: %v", err)
	}
	if n := strings.Count(string(logged), "\n"); n != 1 {
		t.Errorf("expected 1 log record, got %d:\n%q", n, string(logged))
	}
	if strings.Contains(string(logged), "\x07") {
		t.Errorf("log record retains a control character:\n%q", string(logged))
	}
	if strings.ContainsAny(string(logged), "\u202e\u2066") {
		t.Errorf("log record retains a bidi format character:\n%q", string(logged))
	}
}

// TestPromoteLogRecordKeepsFullGitOutput pins the log to git's whole output: a
// rejected commit leads with a line that says nothing, so a record cut to the
// first line would drop the reason the log exists to preserve.
func TestPromoteLogRecordKeepsFullGitOutput(t *testing.T) {
	root, art := seedGitCandidate(t)
	hook := filepath.Join(root, ".git", "hooks", "pre-commit")
	script := "#!/bin/sh\necho 'checking things'\necho 'rejected: policy check failed'\nexit 1\n"
	if err := os.WriteFile(hook, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	dest, warn, err := promoteCandidate(art)
	if err != nil {
		t.Fatalf("promoteCandidate: %v", err)
	}
	if warn == "" {
		t.Fatal("expected a degraded commit, got a clean promote")
	}
	// The failed commit left nothing staged: an index still holding the
	// gesture's paths would be absorbed by the next commit made in the store.
	// Read via --cached, not status --porcelain, whose index and worktree
	// columns differ by one leading space and are indistinguishable once the
	// output is trimmed.
	if staged := testGit(t, root, "diff", "--cached", "--name-only", "--", dest, art.Path); staged != "" {
		t.Errorf("failed commit left the gesture staged:\n%s", staged)
	}
	// The gesture itself still landed in the working tree.
	if st := testGit(t, root, "status", "--porcelain", "-uall", "--", dest, art.Path); st == "" {
		t.Error("gesture paths are clean after a failed commit")
	}
	// The status line carries only the head of the failure.
	if strings.Contains(warn, "policy check failed") {
		t.Errorf("status reason is not bounded to the failure head: %q", warn)
	}

	logged, err := os.ReadFile(filepath.Join(config.Home(), "knowledge-git.log"))
	if err != nil {
		t.Fatalf("knowledge-git.log: %v", err)
	}
	if !strings.Contains(string(logged), "policy check failed") {
		t.Errorf("log record dropped the cause below git's first line:\n%q", string(logged))
	}
	if n := strings.Count(string(logged), "\n"); n != 1 {
		t.Errorf("expected 1 log record, got %d:\n%q", n, string(logged))
	}
}

func TestPromoteCandidateLeavesDirtUncommitted(t *testing.T) {
	root, art := seedGitCandidate(t)

	// Pre-existing working-tree dirt of both kinds the live store carries.
	untracked := filepath.Join(root, "_candidates", "truths", "loom", "unrelated--20260101-000000.md")
	if err := os.WriteFile(untracked, []byte("untracked\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	modified := filepath.Join(root, "index.md")
	if err := os.WriteFile(modified, []byte("seeded\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testGit(t, root, "add", "--", modified)
	testGit(t, root, "commit", "-m", "seed index")
	if err := os.WriteFile(modified, []byte("seeded\nedited\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, warn, err := promoteCandidate(art); err != nil || warn != "" {
		t.Fatalf("promoteCandidate: err=%v warn=%s", err, warn)
	}

	names := testGit(t, root, "show", "--name-status", "--no-renames", "--format=", "HEAD")
	if strings.Contains(names, "index.md") || strings.Contains(names, "unrelated--") {
		t.Errorf("commit absorbed pre-existing dirt:\n%s", names)
	}
	// -uall: with the candidate committed away, _candidates/ holds only the
	// untracked file and porcelain would otherwise collapse it to the directory.
	st := testGit(t, root, "status", "--porcelain", "-uall")
	if !strings.Contains(st, "index.md") || !strings.Contains(st, "unrelated--") {
		t.Errorf("pre-existing dirt no longer dirty after promote:\n%s", st)
	}
}

func TestRejectCandidateCommits(t *testing.T) {
	root, art := seedGitCandidate(t)

	dest, warn, err := rejectCandidate(art)
	if err != nil {
		t.Fatalf("rejectCandidate: %v", err)
	}
	if warn != "" {
		t.Fatalf("reject in a git store did not commit: %s", warn)
	}
	subject := testGit(t, root, "log", "-1", "--format=%s")
	if !strings.Contains(subject, art.ID) || !strings.Contains(subject, art.Scope) {
		t.Errorf("commit subject %q does not name id %q and scope %q", subject, art.ID, art.Scope)
	}
	// The record is the log.md entry plus the candidate's removal; the archive
	// rides along so the tuning corpus lives in history rather than as
	// untracked working-tree state.
	names := testGit(t, root, "show", "--name-status", "--no-renames", "--format=", "HEAD")
	if !strings.Contains(names, "M\tlog.md") {
		t.Errorf("commit does not record the decision in log.md:\n%s", names)
	}
	if !strings.Contains(names, "D\t_candidates/truths/loom/loom-notify-state-default-path--20260426-100856.md") {
		t.Errorf("commit does not delete the candidate:\n%s", names)
	}
	if !strings.Contains(names, "A\t_candidates/_rejected/truths/loom/loom-notify-state-default-path--20260426-100856.md") {
		t.Errorf("commit does not add the archived file:\n%s", names)
	}
	entry := readLog(t, root)
	if !strings.Contains(entry, "reject "+art.ID+" | "+art.Scope+" | truth candidate "+filepath.Base(dest)+" archived") {
		t.Errorf("log.md entry does not name the rejected candidate:\n%s", entry)
	}
	if st := testGit(t, root, "status", "--porcelain", "--", art.Path, dest, filepath.Join(root, "log.md")); st != "" {
		t.Errorf("recorded paths still dirty after reject:\n%s", st)
	}
}

// readLog returns the store's log.md.
func readLog(t *testing.T, root string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, "log.md"))
	if err != nil {
		t.Fatalf("log.md: %v", err)
	}
	return string(b)
}

// TestRejectCandidateIgnoredArchiveCommits holds the guarantee that lets the
// archive be in the pathspec at all: with _candidates/_rejected/ gitignored,
// `git add` over the archived file is a fatal "paths are ignored" that would
// sink the whole record, so commitKnowledge drops the ignored path and the
// log.md entry lands regardless.
func TestRejectCandidateIgnoredArchiveCommits(t *testing.T) {
	root, art := seedGitCandidate(t)
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("_candidates/_rejected/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testGit(t, root, "add", "--", filepath.Join(root, ".gitignore"))
	testGit(t, root, "commit", "-m", "ignore the archive")
	head := testGit(t, root, "rev-parse", "HEAD")

	if _, warn, err := rejectCandidate(art); err != nil || warn != "" {
		t.Fatalf("rejectCandidate: err=%v warn=%q", err, warn)
	}
	if now := testGit(t, root, "rev-parse", "HEAD"); now == head {
		t.Error("HEAD did not move: the reject left no committed record")
	}
	names := testGit(t, root, "show", "--name-status", "--no-renames", "--format=", "HEAD")
	if !strings.Contains(names, "M\tlog.md") {
		t.Errorf("commit does not record the decision in log.md:\n%s", names)
	}
	if strings.Contains(names, "_candidates/_rejected/") {
		t.Errorf("commit names the ignored archive:\n%s", names)
	}
	// A dropped path is otherwise invisible — the commit that lands looks like
	// any other — so knowledge-git.log names it.
	logged, err := os.ReadFile(filepath.Join(config.Home(), "knowledge-git.log"))
	if err != nil {
		t.Fatalf("knowledge-git.log: %v", err)
	}
	if !strings.Contains(string(logged), "dropping ignored path") || !strings.Contains(string(logged), "_candidates/_rejected/") {
		t.Errorf("log record does not name the dropped archive:\n%q", string(logged))
	}
}

// TestRejectIgnoredTrackedCandidateCommitsRemoval pins the drop to the paths the
// caller declared droppable. A store can ignore its candidates while still
// carrying ones tracked from before the rule; the source's removal is the
// record, so it stays in the pathspec however the ignore rules read, and a
// dropped deletion would leave a phantom candidate in history forever.
func TestRejectIgnoredTrackedCandidateCommitsRemoval(t *testing.T) {
	root, art := seedGitCandidate(t)
	// A file pattern rather than "_candidates/": git add refuses an ignored
	// *directory* named in a pathspec even when the file under it is tracked,
	// so a directory rule fails the whole gesture loudly at the add and leaves
	// no commit to inspect. The file pattern ignores the same candidates while
	// letting the tracked deletion through, which is the branch under test.
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("_candidates/**/*.md\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testGit(t, root, "add", "--", filepath.Join(root, ".gitignore"))
	testGit(t, root, "commit", "-m", "ignore the candidate files")

	if _, warn, err := rejectCandidate(art); err != nil || warn != "" {
		t.Fatalf("rejectCandidate: err=%v warn=%q", err, warn)
	}
	names := testGit(t, root, "show", "--name-status", "--no-renames", "--format=", "HEAD")
	if !strings.Contains(names, "D\t_candidates/truths/loom/loom-notify-state-default-path--20260426-100856.md") {
		t.Errorf("commit does not delete the tracked candidate:\n%s", names)
	}
}

// TestPromoteIgnoredDestinationDegrades is the other side of the drop: a
// promote declares nothing droppable, because the promoted file is the record.
// A store whose rules cover the validated tree has to fail loudly rather than
// commit the candidate's removal alone and report a clean promote, which would
// leave the promoted artifact nowhere in history.
func TestPromoteIgnoredDestinationDegrades(t *testing.T) {
	root, art := seedGitCandidate(t)
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("truths/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testGit(t, root, "add", "--", filepath.Join(root, ".gitignore"))
	testGit(t, root, "commit", "-m", "ignore the validated tree")
	head := testGit(t, root, "rev-parse", "HEAD")

	if _, warn, err := promoteCandidate(art); err != nil || warn == "" {
		t.Fatalf("promote of an ignored destination did not degrade: err=%v warn=%q", err, warn)
	}
	if now := testGit(t, root, "rev-parse", "HEAD"); now != head {
		t.Errorf("HEAD moved despite an ignored destination: %s -> %s", head, now)
	}
}

func TestRejectUntrackedCandidateCommits(t *testing.T) {
	root, art := seedGitUntrackedCandidate(t)

	if _, warn, err := rejectCandidate(art); err != nil || warn != "" {
		t.Fatalf("rejectCandidate: err=%v warn=%q", err, warn)
	}
	names := testGit(t, root, "show", "--name-status", "--no-renames", "--format=", "HEAD")
	if !strings.Contains(names, "A\tlog.md") {
		t.Errorf("commit does not record the decision in log.md:\n%s", names)
	}
	// The candidate was never tracked, so git has nothing to delete.
	if strings.Contains(names, "D\t_candidates/") {
		t.Errorf("commit deletes a candidate that was never tracked:\n%s", names)
	}
}

func TestRejectCandidateLeavesDirtUncommitted(t *testing.T) {
	root, art := seedGitCandidate(t)

	// Pre-existing working-tree dirt of both kinds the live store carries.
	untracked := filepath.Join(root, "_candidates", "truths", "loom", "unrelated--20260101-000000.md")
	if err := os.WriteFile(untracked, []byte("untracked\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	modified := filepath.Join(root, "index.md")
	if err := os.WriteFile(modified, []byte("seeded\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testGit(t, root, "add", "--", modified)
	testGit(t, root, "commit", "-m", "seed index")
	if err := os.WriteFile(modified, []byte("seeded\nedited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// log.md is the exception, and the live store's normal state: the extractor
	// appends entries and never commits them, so a reject's file-granular
	// pathspec folds whatever is pending into its own commit.
	pending := "\n## [2026-08-16] extract 92118425 | loom | 8 truth candidate(s)\n"
	if err := appendRejectLog(filepath.Join(root, "log.md"), pending); err != nil {
		t.Fatal(err)
	}

	if _, warn, err := rejectCandidate(art); err != nil || warn != "" {
		t.Fatalf("rejectCandidate: err=%v warn=%q", err, warn)
	}

	names := testGit(t, root, "show", "--name-status", "--no-renames", "--format=", "HEAD")
	if strings.Contains(names, "index.md") || strings.Contains(names, "unrelated--") {
		t.Errorf("commit absorbed pre-existing dirt:\n%s", names)
	}
	st := testGit(t, root, "status", "--porcelain", "-uall")
	if !strings.Contains(st, "index.md") || !strings.Contains(st, "unrelated--") {
		t.Errorf("pre-existing dirt no longer dirty after reject:\n%s", st)
	}
	// The pending extractor entry rode along, and log.md is clean afterwards.
	if !strings.Contains(testGit(t, root, "show", "HEAD", "--", "log.md"), "extract 92118425") {
		t.Error("commit did not absorb the pending log.md entry")
	}
	if strings.Contains(st, "log.md") {
		t.Errorf("log.md still dirty after reject:\n%s", st)
	}
}

// TestRejectLogEntryFieldsAreAllowListed: log.md is rendered markdown and the
// fields come from extractor-written frontmatter, so an unterminated HTML
// comment would hide every entry below it and an injected " | " would misstate
// the entry's own field boundaries.
func TestRejectLogEntryFieldsAreAllowListed(t *testing.T) {
	root, art := seedGitCandidate(t)
	art.ID = "loom-notify <!-- hidden | forged <script>x</script> [link](u)"

	if _, warn, err := rejectCandidate(art); err != nil || warn != "" {
		t.Fatalf("rejectCandidate: err=%v warn=%q", err, warn)
	}

	entry := rejectEntry(t, root)
	if strings.ContainsAny(entry, "<>()") {
		t.Errorf("entry retains markdown/HTML syntax:\n%q", entry)
	}
	// The only brackets left are the entry's own date.
	if strings.Count(entry, "[") != 1 || strings.Count(entry, "]") != 1 {
		t.Errorf("entry retains injected brackets:\n%q", entry)
	}
	if n := strings.Count(entry, " | "); n != 2 {
		t.Errorf("entry has %d field separators, want 2:\n%q", n, entry)
	}
	if !strings.Contains(entry, "?") {
		t.Errorf("substituted runes vanished instead of reading as odd:\n%q", entry)
	}
}

// TestRejectLogEntryLongIDKeepsTail: the bound is per-field, so a model-length
// id cannot truncate the scope, type and basename off the end of the entry.
func TestRejectLogEntryLongIDKeepsTail(t *testing.T) {
	root, art := seedGitCandidate(t)
	art.ID = strings.Repeat("a", 400)

	dest, warn, err := rejectCandidate(art)
	if err != nil || warn != "" {
		t.Fatalf("rejectCandidate: err=%v warn=%q", err, warn)
	}

	entry := rejectEntry(t, root)
	if !strings.Contains(entry, "| loom | truth candidate "+filepath.Base(dest)+" archived") {
		t.Errorf("entry lost its tail to a long id:\n%q", entry)
	}
	if n := len([]rune(entry)); n > gestureMessageMax {
		t.Errorf("entry is %d runes, want at most %d:\n%q", n, gestureMessageMax, entry)
	}
}

// TestRejectLogEntryKeepsBasenameSuffix: a basename is "<id>--<timestamp>.md",
// so it outgrows its bound exactly when the id is long — and the part a tail cut
// would take is the timestamp, the only thing that tells apart the siblings a
// re-run of the extractor emits under one id.
func TestRejectLogEntryKeepsBasenameSuffix(t *testing.T) {
	longID := strings.Repeat("a", 90)
	first := Artifact{
		ID:    longID,
		Scope: "loom",
		Type:  "truth",
		Path:  filepath.Join("_candidates", "truths", "loom", longID+"--20260426-100856.md"),
	}
	second := first
	second.Path = filepath.Join("_candidates", "truths", "loom", longID+"--20260426-113000.md")

	one, two := rejectLogEntry(first), rejectLogEntry(second)
	if !strings.Contains(one, "--20260426-100856.md archived") {
		t.Errorf("entry dropped the basename's timestamp suffix:\n%q", one)
	}
	if one == two {
		t.Errorf("siblings differing only in timestamp produced the same entry:\n%q", one)
	}
}

// TestRejectLogEntryBoundsFitTheLine composes an entry with every field at its
// bound: the fixed scaffolding plus the four bounds has to land inside
// gestureMessageMax, or sanitizeRecord backstops the composed line by cutting
// the tail — the failure the per-field bounds exist to prevent.
func TestRejectLogEntryBoundsFitTheLine(t *testing.T) {
	entry := strings.TrimSpace(rejectLogEntry(Artifact{
		ID:    strings.Repeat("a", logFieldIDMax),
		Scope: strings.Repeat("b", logFieldScopeMax),
		Type:  strings.Repeat("c", logFieldTypeMax),
		Path:  strings.Repeat("d", logFieldBaseMax),
	}))
	if n := len([]rune(entry)); n != gestureMessageMax {
		t.Errorf("worst-case entry is %d runes, want exactly %d:\n%q", n, gestureMessageMax, entry)
	}
	if !strings.HasSuffix(entry, " archived") {
		t.Errorf("worst-case entry was cut by the backstop:\n%q", entry)
	}
}

// rejectEntry returns the single "## [" entry the reject appended to log.md.
func rejectEntry(t *testing.T, root string) string {
	t.Helper()
	for _, line := range strings.Split(readLog(t, root), "\n") {
		if strings.HasPrefix(line, "## [") {
			return line
		}
	}
	t.Fatal("log.md carries no entry")
	return ""
}

// TestRejectLogEntryIsOneEntry pins the log.md record against forgery: the id
// comes from extractor-written frontmatter, and a newline in it would append a
// second entry to the store's own history.
func TestRejectLogEntryIsOneEntry(t *testing.T) {
	root, art := seedGitCandidate(t)
	art.ID = forgedID

	if _, warn, err := rejectCandidate(art); err != nil || warn != "" {
		t.Fatalf("rejectCandidate: err=%v warn=%q", err, warn)
	}

	logged := readLog(t, root)
	// Counted by line start: the seeded header names the entry format inline.
	entries := 0
	for _, line := range strings.Split(logged, "\n") {
		if strings.HasPrefix(line, "## [") {
			entries++
		}
	}
	if entries != 1 {
		t.Errorf("expected 1 log.md entry, got %d:\n%q", entries, logged)
	}
	if strings.Contains(logged, "\x07") {
		t.Errorf("log.md entry retains a control character:\n%q", logged)
	}
	if strings.ContainsAny(logged, "\u202e\u2066") {
		t.Errorf("log.md entry retains a bidi format character:\n%q", logged)
	}
}

// TestRejectMissingLogDegrades: log.md is bootstrapped at store init, so a TUI
// pointed at a root without one must report the missing record rather than
// write a log.md into existence.
func TestRejectMissingLogDegrades(t *testing.T) {
	root, art := seedGitCandidate(t)
	testGit(t, root, "rm", "-q", "--", filepath.Join(root, "log.md"))
	testGit(t, root, "commit", "-m", "drop log")

	dest, warn, err := rejectCandidate(art)
	if err != nil {
		t.Fatalf("rejectCandidate: %v", err)
	}
	if warn == "" {
		t.Fatal("expected a degraded reject, got a clean record")
	}
	// The archive move still stands.
	if _, err := os.Stat(dest); err != nil {
		t.Errorf("archived file missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "log.md")); !os.IsNotExist(err) {
		t.Errorf("reject bootstrapped a log.md: %v", err)
	}
	logged, err := os.ReadFile(filepath.Join(config.Home(), "knowledge-git.log"))
	if err != nil {
		t.Fatalf("knowledge-git.log: %v", err)
	}
	if !strings.Contains(string(logged), "log.md") || !strings.Contains(string(logged), art.ID) {
		t.Errorf("log record does not carry the reason and the gesture:\n%q", string(logged))
	}
}

// TestRejectCommitFailureKeepsArchive: the archive is in the pathspec now, so a
// failed commit has to degrade like every other knowledge-store commit failure
// — the file stays archived, log.md keeps the entry, and neither is rolled back
// to buy a tidy history. A failing pre-commit hook fails the commit itself,
// leaving the `git add` over the pathspec alone.
func TestRejectCommitFailureKeepsArchive(t *testing.T) {
	root, art := seedGitCandidate(t)
	head := testGit(t, root, "rev-parse", "HEAD")
	hook := filepath.Join(root, ".git", "hooks", "pre-commit")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	dest, warn, err := rejectCandidate(art)
	if err != nil {
		t.Fatalf("rejectCandidate: %v", err)
	}
	if warn == "" {
		t.Fatal("expected a degraded reject, got a clean record")
	}
	if _, err := os.Stat(dest); err != nil {
		t.Errorf("archived file missing after a failed commit: %v", err)
	}
	if _, err := os.Stat(art.Path); !os.IsNotExist(err) {
		t.Errorf("archive move rolled back after a failed commit: %v", err)
	}
	if !strings.Contains(readLog(t, root), art.ID) {
		t.Errorf("log.md does not record the reject after a failed commit:\n%s", readLog(t, root))
	}
	if now := testGit(t, root, "rev-parse", "HEAD"); now != head {
		t.Errorf("HEAD moved despite a failed commit: %s -> %s", head, now)
	}
	logged, err := os.ReadFile(filepath.Join(config.Home(), "knowledge-git.log"))
	if err != nil {
		t.Fatalf("knowledge-git.log: %v", err)
	}
	// Naming the commit step pins which failure degraded the gesture — the
	// commit, not the add; the archive's place in the pathspec is
	// TestRejectCandidateCommits'.
	if !strings.Contains(string(logged), art.ID) || !strings.Contains(string(logged), "git commit") {
		t.Errorf("log record does not name the gesture and the failed commit:\n%q", string(logged))
	}
}

func TestPromoteCandidateNonRepoDegrades(t *testing.T) {
	// seedCandidate's root is a bare temp dir, not a git repo.
	_, art := seedCandidate(t)

	dest, warn, err := promoteCandidate(art)
	if err != nil {
		t.Fatalf("promoteCandidate: %v", err)
	}
	// The gesture still lands; only the record is missing.
	if _, err := os.Stat(dest); err != nil {
		t.Errorf("promoted file missing: %v", err)
	}
	if _, err := os.Stat(art.Path); !os.IsNotExist(err) {
		t.Errorf("source still present after promote: %v", err)
	}
	if !strings.Contains(warn, "not a git repo") {
		t.Errorf("warn = %q, want a not-a-git-repo reason", warn)
	}
	logged, err := os.ReadFile(filepath.Join(config.Home(), "knowledge-git.log"))
	if err != nil {
		t.Fatalf("knowledge-git.log: %v", err)
	}
	if !strings.Contains(string(logged), art.ID) {
		t.Errorf("log line does not name the gesture: %q", string(logged))
	}
}

// TestPromoteCandidateMissingGitDegrades points PATH at an empty directory, so
// git cannot be run at all. The store is a perfectly good repo, and neither the
// status line nor the log may attribute the failure to its layout.
func TestPromoteCandidateMissingGitDegrades(t *testing.T) {
	_, art := seedGitCandidate(t)
	t.Setenv("PATH", t.TempDir())

	dest, warn, err := promoteCandidate(art)
	if err != nil {
		t.Fatalf("promoteCandidate: %v", err)
	}
	if _, err := os.Stat(dest); err != nil {
		t.Errorf("promoted file missing: %v", err)
	}
	if strings.Contains(warn, errNoGitRepo.Error()) {
		t.Errorf("warn blames the store's layout for a git that would not run: %q", warn)
	}
	if !strings.Contains(warn, "git") {
		t.Errorf("warn = %q, want a reason naming git", warn)
	}
	logged, err := os.ReadFile(filepath.Join(config.Home(), "knowledge-git.log"))
	if err != nil {
		t.Fatalf("knowledge-git.log: %v", err)
	}
	if !strings.Contains(string(logged), exec.ErrNotFound.Error()) {
		t.Errorf("log record does not record the missing binary:\n%q", string(logged))
	}
}

func TestPromoteCandidateEnclosingRepoDegrades(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	// The store is a plain subdirectory of a repo — the shape a git-managed
	// home directory produces. rev-parse walks up to the parent, which must
	// not receive the gesture.
	parent := t.TempDir()
	testGit(t, parent, "init")
	testGit(t, parent, "config", "user.email", "test@example.com")
	testGit(t, parent, "config", "user.name", "loom test")
	testGit(t, parent, "commit", "--allow-empty", "-m", "seed parent")
	head := testGit(t, parent, "rev-parse", "HEAD")

	_, art := seedCandidateIn(t, filepath.Join(parent, "knowledge"))

	dest, warn, err := promoteCandidate(art)
	if err != nil {
		t.Fatalf("promoteCandidate: %v", err)
	}
	if _, err := os.Stat(dest); err != nil {
		t.Errorf("promoted file missing: %v", err)
	}
	if !strings.Contains(warn, "inside another git repo") {
		t.Errorf("warn = %q, want an enclosing-repo reason", warn)
	}
	if now := testGit(t, parent, "rev-parse", "HEAD"); now != head {
		t.Errorf("enclosing repo HEAD moved: %s -> %s", head, now)
	}
	if log := testGit(t, parent, "log", "--format=%s"); strings.Contains(log, art.ID) {
		t.Errorf("enclosing repo recorded the gesture:\n%s", log)
	}
}
