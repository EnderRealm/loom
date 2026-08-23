package tui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"loom/internal/config"
	"loom/internal/knowledge/store"
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

// seedCandidate seeds the fixture into a fresh knowledge root and points the
// store at it. LOOM_HOME is redirected too, so a gesture that can't commit
// writes knowledge-git.log into the test's own state root rather than the
// developer's ~/.loom.
func seedCandidate(t *testing.T) (string, Artifact) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("LOOM_KNOWLEDGE_ROOT", root)
	t.Setenv("LOOM_HOME", t.TempDir())
	// Isolate every git these tests run — the code under test included — from
	// the developer's config: init.templateDir would seed repos we did not ask
	// for, and a global identity would mask a fixture that configures none.
	// Repo-local config is then the only identity source.
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

// requireRefFormatFiles skips on a git that predates --ref-format (2.45), whose
// testGit would otherwise die inside the fixture and read as a failure of the
// code under test.
func requireRefFormatFiles(t *testing.T) {
	t.Helper()
	if err := exec.Command("git", "-C", t.TempDir(), "init", "--ref-format=files").Run(); err != nil {
		t.Skip("git does not support --ref-format")
	}
}

// seedGitCandidate seeds the fixture into a knowledge root that is a git repo
// with the candidate committed, so a gesture has a HEAD to build on. Identity
// is configured repo-locally; the host's global git config is never written.
func seedGitCandidate(t *testing.T, initArgs ...string) (root string, art Artifact) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	root, art = seedCandidate(t)
	testGit(t, root, append([]string{"init"}, initArgs...)...)
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
	if err := os.WriteFile(filepath.Join(root, "log.md"), []byte(logFixture+pending), 0o644); err != nil {
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
	if n := len([]rune(entry)); n > store.MessageMax {
		t.Errorf("entry is %d runes, want at most %d:\n%q", n, store.MessageMax, entry)
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
// store.MessageMax, or store.SanitizeRecord backstops the composed line by cutting
// the tail — the failure the per-field bounds exist to prevent.
func TestRejectLogEntryBoundsFitTheLine(t *testing.T) {
	entry := strings.TrimSpace(rejectLogEntry(Artifact{
		ID:    strings.Repeat("a", logFieldIDMax),
		Scope: strings.Repeat("b", logFieldScopeMax),
		Type:  strings.Repeat("c", logFieldTypeMax),
		Path:  strings.Repeat("d", logFieldBaseMax),
	}))
	if n := len([]rune(entry)); n != store.MessageMax {
		t.Errorf("worst-case entry is %d runes, want exactly %d:\n%q", n, store.MessageMax, entry)
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
// to buy a tidy history. The commit is failed by a stale lock on the branch ref,
// which git refuses at the commit while leaving the `git add` before it alone;
// the store pins hooks off, so the pre-commit hook cannot force this. The lock
// path is the loose-files ref backend's, so the fixture pins that backend rather
// than inheriting the host's default.
func TestRejectCommitFailureKeepsArchive(t *testing.T) {
	requireRefFormatFiles(t)
	root, art := seedGitCandidate(t, "--ref-format=files")
	head := testGit(t, root, "rev-parse", "HEAD")
	branch := testGit(t, root, "symbolic-ref", "--short", "HEAD")
	if err := os.WriteFile(filepath.Join(root, ".git", "refs", "heads", branch+".lock"), nil, 0o644); err != nil {
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

// TestEditCommits: the edit is not a third gesture with commit code of its own.
// $EDITOR writes the file, commitEdit declares the path it wrote, and the store
// records it like any other unit of work.
func TestEditCommits(t *testing.T) {
	root, art := seedGitCandidate(t)
	if err := os.WriteFile(art.Path, []byte(candidateFixture+"\nEdited by hand.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if warn := commitEdit(art); warn != "" {
		t.Fatalf("edit in a git store did not commit: %s", warn)
	}

	if subject := testGit(t, root, "log", "-1", "--format=%s"); subject != "edit truth loom/"+art.ID {
		t.Errorf("commit subject = %q, want the edited artifact's type, scope and id", subject)
	}
	names := testGit(t, root, "show", "--name-status", "--format=", "HEAD")
	if !strings.Contains(names, "M\t_candidates/truths/loom/loom-notify-state-default-path--20260426-100856.md") {
		t.Errorf("commit does not record the edited file:\n%s", names)
	}
	if st := testGit(t, root, "status", "--porcelain", "--", art.Path); st != "" {
		t.Errorf("edited path still dirty after the edit:\n%s", st)
	}
}

// TestEditWithoutChangesLeavesNoCommit: quitting $EDITOR without saving is
// likely the most common use of the gesture, and the declared path is then a
// file that did not change. It must read as nothing to record rather than as a
// failed one — no status-line reason, no record in knowledge-git.log.
func TestEditWithoutChangesLeavesNoCommit(t *testing.T) {
	root, art := seedGitCandidate(t)
	head := testGit(t, root, "rev-parse", "HEAD")

	if warn := commitEdit(art); warn != "" {
		t.Fatalf("an edit that changed nothing reported %q", warn)
	}

	if now := testGit(t, root, "rev-parse", "HEAD"); now != head {
		t.Errorf("HEAD moved for an edit that changed nothing: %s -> %s", head, now)
	}
	if _, err := os.Stat(filepath.Join(config.Home(), "knowledge-git.log")); !os.IsNotExist(err) {
		t.Errorf("an edit that changed nothing wrote a failure record: %v", err)
	}
}

// TestPromoteWithAnUnremovableCandidateStillLands: the promoted file is written
// and committed before the candidate is dropped, so a failure at the removal is
// a gesture that landed with an incomplete record — not "promote failed", which
// would leave the candidate listed and the next attempt refused for a
// destination that is now there.
func TestPromoteWithAnUnremovableCandidateStillLands(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	root, art := seedGitCandidate(t)
	// Removing a file needs write permission on its directory, not on the file.
	dir := filepath.Dir(art.Path)
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	// Restored before the temp directory's own cleanup, which runs after this.
	t.Cleanup(func() { os.Chmod(dir, 0o755) })

	dest, warn, err := promoteCandidate(art)

	if err != nil {
		t.Fatalf("promote reported a failure for a gesture that landed: %v", err)
	}
	if dest == "" {
		t.Fatal("promote returned no destination for a file it wrote")
	}
	if warn == "" {
		t.Error("promote reported a clean record despite the candidate surviving")
	}
	if _, err := os.Stat(dest); err != nil {
		t.Errorf("promoted file missing: %v", err)
	}
	if names := testGit(t, root, "show", "--name-status", "--format=", "HEAD"); !strings.Contains(names, "A\ttruths/loom/notify-state-default-path.md") {
		t.Errorf("the write that landed was not committed:\n%s", names)
	}
}

// TestEditOutsideTheStoreDegrades: the store can refuse the path itself, and an
// edit that recorded nothing must reach the status line rather than read as a
// clean one.
func TestEditOutsideTheStoreDegrades(t *testing.T) {
	_, art := seedGitCandidate(t)
	art.Path = filepath.Join(t.TempDir(), "elsewhere.md")

	if warn := commitEdit(art); warn == "" {
		t.Error("an edit the store refused reported a clean record")
	}
}
