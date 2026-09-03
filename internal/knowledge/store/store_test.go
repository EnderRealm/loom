package store

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"loom/internal/config"
)

// Fixtures are real git repos in temp directories rather than mocks: what this
// package has to get right is git's own behaviour — what a pathspec matches,
// what `commit --only` leaves alone, where `rev-parse --show-toplevel` lands —
// and a mocked git would assert nothing about that.

// testGit runs a git command against a test store. gpgsign is pinned off so a
// signing key in the host's global config can't stall a fixture commit.
func testGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	full := append([]string{"-C", root, "-c", "commit.gpgsign=false"}, args...)
	out, err := exec.Command("git", full...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// seedRoot points the store at a fresh temp directory. LOOM_HOME is redirected
// too, so a failed commit writes knowledge-git.log into the test's own state
// root rather than the developer's ~/.loom. Every git these tests run — the code
// under test included — is isolated from the developer's config: a global
// core.hooksPath or init.templateDir would otherwise seed repos we did not ask
// for. Repo-local config is then the only identity source.
func seedRoot(t *testing.T) string {
	t.Helper()
	return seedRootAt(t, t.TempDir())
}

// seedRootAt is seedRoot for a store that has to sit somewhere in particular.
// The directory is created: a store is bootstrapped before anything writes to
// it, and Apply refuses a root it cannot open rather than materializing one.
func seedRootAt(t *testing.T, root string) string {
	t.Helper()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LOOM_KNOWLEDGE_ROOT", root)
	t.Setenv("LOOM_HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	return root
}

// initRepo makes root a git repo with an identity and no history.
func initRepo(t *testing.T, root string, initArgs ...string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	testGit(t, root, append([]string{"init"}, initArgs...)...)
	testGit(t, root, "config", "user.email", "test@example.com")
	testGit(t, root, "config", "user.name", "loom test")
}

// seedStore is the live store's shape: a repo with a tracked log.md and one
// commit to build on.
func seedStore(t *testing.T) string {
	t.Helper()
	root := seedRoot(t)
	initRepo(t, root)
	if err := os.WriteFile(filepath.Join(root, "log.md"), []byte("# Knowledge log\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testGit(t, root, "add", "-A")
	testGit(t, root, "commit", "-m", "seed store")
	return root
}

// gitLog returns the knowledge-git log this test's LOOM_HOME collects.
func gitLog(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(config.Home(), "knowledge-git.log"))
	if err != nil {
		t.Fatalf("knowledge-git.log: %v", err)
	}
	return string(b)
}

// committed returns the name-status of HEAD. --no-renames: a unit of work that
// writes and deletes is two changes, and rename detection would fold the pair
// into one R line.
func committed(t *testing.T, root string) string {
	t.Helper()
	return testGit(t, root, "show", "--name-status", "--no-renames", "--format=", "HEAD")
}

// TestApplyNewWriterNeedsNoCommitCode is the whole point of the package: a
// writer that knows only Apply and WriteFile leaves a commit, having written no
// commit code of its own.
func TestApplyNewWriterNeedsNoCommitCode(t *testing.T) {
	root := seedStore(t)
	dest := filepath.Join(root, "truths", "loom", "new-writer.md")

	warn, err := Apply("write truths/loom/new-writer.md", func(tx *Tx) error {
		return tx.WriteFile(dest, []byte("# a new writer\n"))
	})

	if err != nil || warn.NotCommitted != "" {
		t.Fatalf("Apply: err=%v warn=%q", err, warn.NotCommitted)
	}
	if subject := testGit(t, root, "log", "-1", "--format=%s"); subject != "write truths/loom/new-writer.md" {
		t.Errorf("commit subject = %q", subject)
	}
	if names := committed(t, root); !strings.Contains(names, "A\ttruths/loom/new-writer.md") {
		t.Errorf("commit does not add the written file:\n%s", names)
	}
	if st := testGit(t, root, "status", "--porcelain", "--", dest); st != "" {
		t.Errorf("written path still dirty:\n%s", st)
	}
}

// TestApplyCommitsEveryOp walks the op vocabulary in one unit of work: each
// recorded path is in the commit, and the mutation Touch declares — made outside
// the Tx, as $EDITOR makes it — is recorded like the rest.
func TestApplyCommitsEveryOp(t *testing.T) {
	root := seedStore(t)
	edited := filepath.Join(root, "truths", "loom", "edited.md")
	gone := filepath.Join(root, "truths", "loom", "gone.md")
	moved := filepath.Join(root, "truths", "loom", "moved.md")
	for _, p := range []string{edited, gone, moved} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("seeded\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	testGit(t, root, "add", "-A")
	testGit(t, root, "commit", "-m", "seed artifacts")
	if err := os.WriteFile(edited, []byte("edited outside the Tx\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	warn, err := Apply("every op", func(tx *Tx) error {
		tx.Touch(edited)
		if err := tx.WriteFile(filepath.Join(root, "truths", "loom", "written.md"), []byte("written\n")); err != nil {
			return err
		}
		if err := tx.Remove(gone); err != nil {
			return err
		}
		if err := tx.Rename(moved, filepath.Join(root, "truths", "loom", "arrived.md")); err != nil {
			return err
		}
		return tx.Append(filepath.Join(root, "log.md"), "\n## [2026-08-23] every op\n")
	})

	if err != nil || warn.NotCommitted != "" {
		t.Fatalf("Apply: err=%v warn=%q", err, warn.NotCommitted)
	}
	names := committed(t, root)
	for _, want := range []string{
		"M\ttruths/loom/edited.md",
		"A\ttruths/loom/written.md",
		"D\ttruths/loom/gone.md",
		"D\ttruths/loom/moved.md",
		"A\ttruths/loom/arrived.md",
		"M\tlog.md",
	} {
		if !strings.Contains(names, want) {
			t.Errorf("commit missing %q:\n%s", want, names)
		}
	}
	if st := testGit(t, root, "status", "--porcelain", "-uall"); st != "" {
		t.Errorf("store still dirty after Apply:\n%s", st)
	}
}

// TestApplyLeavesUnrelatedDirtAlone is why the commit is path-scoped: the live
// store's working tree is routinely dirty with candidates and edits this unit of
// work did not make, and a whole-tree commit would absorb them into the record.
func TestApplyLeavesUnrelatedDirtAlone(t *testing.T) {
	root := seedStore(t)
	tracked := filepath.Join(root, "index.md")
	if err := os.WriteFile(tracked, []byte("seeded\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testGit(t, root, "add", "-A")
	testGit(t, root, "commit", "-m", "seed index")
	if err := os.WriteFile(tracked, []byte("seeded\nedited by hand\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	untracked := filepath.Join(root, "stray.md")
	if err := os.WriteFile(untracked, []byte("untracked\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	warn, err := Apply("write one file", func(tx *Tx) error {
		return tx.WriteFile(filepath.Join(root, "truths", "loom", "one.md"), []byte("one\n"))
	})

	if err != nil || warn.NotCommitted != "" {
		t.Fatalf("Apply: err=%v warn=%q", err, warn.NotCommitted)
	}
	if names := committed(t, root); strings.Contains(names, "index.md") || strings.Contains(names, "stray.md") {
		t.Errorf("commit absorbed pre-existing dirt:\n%s", names)
	}
	st := testGit(t, root, "status", "--porcelain", "-uall")
	if !strings.Contains(st, "index.md") || !strings.Contains(st, "stray.md") {
		t.Errorf("pre-existing dirt no longer dirty:\n%s", st)
	}
}

// TestApplyDropsUntrackedRemovedPath: the store carries uncommitted candidates,
// and naming one as a pathspec after its removal is a fatal "did not match any
// files" that would sink the whole commit.
func TestApplyDropsUntrackedRemovedPath(t *testing.T) {
	root := seedStore(t)
	untracked := filepath.Join(root, "_candidates", "truths", "loom", "one.md")
	if err := os.MkdirAll(filepath.Dir(untracked), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(untracked, []byte("candidate\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	warn, err := Apply("promote one", func(tx *Tx) error {
		if err := tx.WriteFile(filepath.Join(root, "truths", "loom", "one.md"), []byte("one\n")); err != nil {
			return err
		}
		return tx.Remove(untracked)
	})

	if err != nil || warn.NotCommitted != "" {
		t.Fatalf("Apply: err=%v warn=%q", err, warn.NotCommitted)
	}
	names := committed(t, root)
	if !strings.Contains(names, "A\ttruths/loom/one.md") {
		t.Errorf("commit does not add the written file:\n%s", names)
	}
	if strings.Contains(names, "_candidates/") {
		t.Errorf("commit deletes a path that was never tracked:\n%s", names)
	}
}

// TestApplyNoRecordedPathsLeavesNoCommit: a unit of work that touched nothing
// has nothing to record, and an empty pathspec would be a git failure reported
// as if the work had failed.
func TestApplyNoRecordedPathsLeavesNoCommit(t *testing.T) {
	root := seedStore(t)
	head := testGit(t, root, "rev-parse", "HEAD")

	warn, err := Apply("touched nothing", func(tx *Tx) error { return nil })

	if err != nil || warn.NotCommitted != "" {
		t.Fatalf("Apply: err=%v warn=%q", err, warn.NotCommitted)
	}
	if now := testGit(t, root, "rev-parse", "HEAD"); now != head {
		t.Errorf("HEAD moved for a unit of work that touched nothing: %s -> %s", head, now)
	}
}

// TestApplyCommitsWritesThatLandedBeforeAnError: the writes are on disk whatever
// the closure then returned, and a store that carries them without the record is
// the state this package exists to prevent.
func TestApplyCommitsWritesThatLandedBeforeAnError(t *testing.T) {
	root := seedStore(t)
	dest := filepath.Join(root, "truths", "loom", "one.md")
	fail := errors.New("the second write failed")

	warn, err := Apply("half a unit of work", func(tx *Tx) error {
		if err := tx.WriteFile(dest, []byte("one\n")); err != nil {
			return err
		}
		return fail
	})

	if !errors.Is(err, fail) {
		t.Fatalf("Apply err = %v, want the closure's own error", err)
	}
	if warn.NotCommitted != "" {
		t.Fatalf("Apply warn = %q, want the commit to have landed", warn.NotCommitted)
	}
	if names := committed(t, root); !strings.Contains(names, "A\ttruths/loom/one.md") {
		t.Errorf("commit does not carry the write that landed:\n%s", names)
	}
	// The closure's failure is never silent, whatever the commit did.
	if logged := gitLog(t); !strings.Contains(logged, fail.Error()) {
		t.Errorf("log record does not carry the closure's failure:\n%q", logged)
	}
}

// TestApplyAppendNeedsAnExistingFile: log.md is bootstrapped at store init, and
// a writer pointed at a wrong root must not scatter one.
func TestApplyAppendNeedsAnExistingFile(t *testing.T) {
	root := seedStore(t)
	missing := filepath.Join(root, "elsewhere", "log.md")

	warn, err := Apply("append to a missing log", func(tx *Tx) error {
		return tx.Append(missing, "\n## [2026-08-23] entry\n")
	})

	if err == nil {
		t.Fatal("append to a missing file succeeded")
	}
	if !strings.Contains(err.Error(), "log.md") {
		t.Errorf("err = %v, want a reason naming the file", err)
	}
	if warn.NotCommitted != "" {
		t.Errorf("warn = %q, want no commit for a unit of work that wrote nothing", warn.NotCommitted)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Errorf("append bootstrapped the file: %v", err)
	}
}

// TestApplyDroppableIgnoredPathLeavesPathspec holds the guarantee that lets a
// path whose record lives elsewhere be in the pathspec at all: passing an
// ignored path to `git add` is a fatal "paths are ignored" that would sink the
// whole commit, so the ignored one is dropped and the record still lands.
func TestApplyDroppableIgnoredPathLeavesPathspec(t *testing.T) {
	root := seedStore(t)
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("_candidates/_rejected/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testGit(t, root, "add", "-A")
	testGit(t, root, "commit", "-m", "ignore the archive")
	archive := filepath.Join(root, "_candidates", "_rejected", "truths", "loom", "one.md")

	warn, err := Apply("reject one", func(tx *Tx) error {
		if err := tx.WriteFile(archive, []byte("archived\n")); err != nil {
			return err
		}
		tx.Droppable(archive)
		return tx.Append(filepath.Join(root, "log.md"), "\n## [2026-08-23] reject one\n")
	})

	if err != nil || warn.NotCommitted != "" {
		t.Fatalf("Apply: err=%v warn=%q", err, warn.NotCommitted)
	}
	names := committed(t, root)
	if !strings.Contains(names, "M\tlog.md") {
		t.Errorf("commit does not record the decision in log.md:\n%s", names)
	}
	if strings.Contains(names, "_candidates/_rejected/") {
		t.Errorf("commit names the ignored path:\n%s", names)
	}
	// A dropped path is otherwise invisible — the commit that lands looks like
	// any other — so knowledge-git.log names it.
	if logged := gitLog(t); !strings.Contains(logged, "dropping ignored path") || !strings.Contains(logged, "_candidates/_rejected/") {
		t.Errorf("log record does not name the dropped path:\n%q", logged)
	}
}

// TestApplyIgnoredRecordFailsLoudly is the other side of the drop: a path the
// caller did not declare droppable is the record, so an ignored one has to fail
// rather than yield a commit that omits it.
func TestApplyIgnoredRecordFailsLoudly(t *testing.T) {
	root := seedStore(t)
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("truths/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testGit(t, root, "add", "-A")
	testGit(t, root, "commit", "-m", "ignore the validated tree")
	head := testGit(t, root, "rev-parse", "HEAD")

	warn, err := Apply("promote one", func(tx *Tx) error {
		return tx.WriteFile(filepath.Join(root, "truths", "loom", "one.md"), []byte("one\n"))
	})

	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if warn.NotCommitted == "" {
		t.Fatal("an ignored record committed cleanly")
	}
	if now := testGit(t, root, "rev-parse", "HEAD"); now != head {
		t.Errorf("HEAD moved despite an ignored record: %s -> %s", head, now)
	}
}

// TestApplyIgnoredTrackedRemovalStaysInPathspec: a store can ignore its
// candidates while still carrying ones tracked from before the rule, and a
// dropped deletion would leave a phantom candidate in history forever. The
// pattern is file-granular because `git add` refuses an ignored *directory* named
// in a pathspec even when the file under it is tracked.
func TestApplyIgnoredTrackedRemovalStaysInPathspec(t *testing.T) {
	root := seedStore(t)
	tracked := filepath.Join(root, "_candidates", "truths", "loom", "one.md")
	if err := os.MkdirAll(filepath.Dir(tracked), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tracked, []byte("candidate\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testGit(t, root, "add", "-A")
	testGit(t, root, "commit", "-m", "seed candidate")
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("_candidates/**/*.md\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testGit(t, root, "add", "-A")
	testGit(t, root, "commit", "-m", "ignore the candidate files")

	warn, err := Apply("reject one", func(tx *Tx) error { return tx.Remove(tracked) })

	if err != nil || warn.NotCommitted != "" {
		t.Fatalf("Apply: err=%v warn=%q", err, warn.NotCommitted)
	}
	if names := committed(t, root); !strings.Contains(names, "D\t_candidates/truths/loom/one.md") {
		t.Errorf("commit does not delete the tracked candidate:\n%s", names)
	}
}

// TestApplyNonRepoDegrades: a store that is not under version control reports
// its own shape rather than relaying a git failure, and the write still lands.
func TestApplyNonRepoDegrades(t *testing.T) {
	root := seedRoot(t)
	dest := filepath.Join(root, "truths", "loom", "one.md")

	warn, err := Apply("write one", func(tx *Tx) error {
		return tx.WriteFile(dest, []byte("one\n"))
	})

	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if warn.NotCommitted != errNoGitRepo.Error() {
		t.Errorf("warn = %q, want %q", warn.NotCommitted, errNoGitRepo.Error())
	}
	if _, err := os.Stat(dest); err != nil {
		t.Errorf("written file missing: %v", err)
	}
	if logged := gitLog(t); !strings.Contains(logged, "write one") {
		t.Errorf("log line does not name the unit of work: %q", logged)
	}
}

// TestApplyEnclosingRepoDegrades: rev-parse walks up the tree, so a store that
// merely sits inside a repo — a git-managed home directory, a dotfiles checkout —
// resolves to that ancestor, which must not receive the commit.
func TestApplyEnclosingRepoDegrades(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	// The store is a plain subdirectory of a repo — the shape a git-managed home
	// directory produces.
	parent := t.TempDir()
	root := seedRootAt(t, filepath.Join(parent, "knowledge"))
	initRepo(t, parent)
	testGit(t, parent, "commit", "--allow-empty", "-m", "seed parent")
	head := testGit(t, parent, "rev-parse", "HEAD")

	warn, err := Apply("write one", func(tx *Tx) error {
		return tx.WriteFile(filepath.Join(root, "truths", "loom", "one.md"), []byte("one\n"))
	})

	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if warn.NotCommitted != errEnclosingRepo.Error() {
		t.Errorf("warn = %q, want %q", warn.NotCommitted, errEnclosingRepo.Error())
	}
	if now := testGit(t, parent, "rev-parse", "HEAD"); now != head {
		t.Errorf("enclosing repo HEAD moved: %s -> %s", head, now)
	}
	if logged := gitLog(t); !strings.Contains(logged, parent) {
		t.Errorf("log record does not name the enclosing repo:\n%q", logged)
	}
}

// TestApplyMissingGitDegrades points PATH at an empty directory, so git cannot
// be run at all. The store is a perfectly good repo, and neither the status line
// nor the log may attribute the failure to its layout.
func TestApplyMissingGitDegrades(t *testing.T) {
	root := seedStore(t)
	t.Setenv("PATH", t.TempDir())

	warn, err := Apply("write one", func(tx *Tx) error {
		return tx.WriteFile(filepath.Join(root, "truths", "loom", "one.md"), []byte("one\n"))
	})

	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if strings.Contains(warn.NotCommitted, errNoGitRepo.Error()) {
		t.Errorf("warn blames the store's layout for a git that would not run: %q", warn.NotCommitted)
	}
	if !strings.Contains(warn.NotCommitted, "git") {
		t.Errorf("warn = %q, want a reason naming git", warn.NotCommitted)
	}
	if logged := gitLog(t); !strings.Contains(logged, exec.ErrNotFound.Error()) {
		t.Errorf("log record does not record the missing binary:\n%q", logged)
	}
}

// lockHead leaves a stale lock on the branch ref, which git refuses at the
// commit rather than at the `git add` before it — the shape the unstaging
// cleanup exists for, with the multi-line output the log has to keep whole. The
// lock path is the loose-files ref backend's, so the fixture pins that backend
// rather than inheriting the host's default.
func lockHead(t *testing.T, root string) {
	t.Helper()
	branch := testGit(t, root, "symbolic-ref", "--short", "HEAD")
	if err := os.WriteFile(filepath.Join(root, ".git", "refs", "heads", branch+".lock"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

// seedLockedStore is seedStore on the loose-files ref backend, skipped on a git
// too old to know the flag (before 2.45) rather than failing in the fixture.
func seedLockedStore(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	probe := t.TempDir()
	if err := exec.Command("git", "-C", probe, "init", "--ref-format=files").Run(); err != nil {
		t.Skip("git does not support --ref-format")
	}
	root := seedRoot(t)
	initRepo(t, root, "--ref-format=files")
	if err := os.WriteFile(filepath.Join(root, "log.md"), []byte("# Knowledge log\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testGit(t, root, "add", "-A")
	testGit(t, root, "commit", "-m", "seed store")
	return root
}

// TestApplyFailedCommitUnstagesAndLogsInFull: a failed commit would otherwise
// leave the unit of work staged for the next commit a human makes in the store
// to absorb — the mirror of the absorption the pathspec scoping prevents — and
// the useful part of git's output is rarely its first line.
func TestApplyFailedCommitUnstagesAndLogsInFull(t *testing.T) {
	root := seedLockedStore(t)
	head := testGit(t, root, "rev-parse", "HEAD")
	dest := filepath.Join(root, "truths", "loom", "one.md")
	lockHead(t, root)

	warn, err := Apply("write one", func(tx *Tx) error {
		return tx.WriteFile(dest, []byte("one\n"))
	})

	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if warn.NotCommitted == "" {
		t.Fatal("a rejected commit reported success")
	}
	if len([]rune(warn.NotCommitted)) > gitReasonMax {
		t.Errorf("warn is %d runes, want at most %d: %q", len([]rune(warn.NotCommitted)), gitReasonMax, warn.NotCommitted)
	}
	if now := testGit(t, root, "rev-parse", "HEAD"); now != head {
		t.Errorf("HEAD moved despite a failed commit: %s -> %s", head, now)
	}
	if staged := testGit(t, root, "diff", "--cached", "--name-only"); staged != "" {
		t.Errorf("failed commit left the unit of work staged:\n%s", staged)
	}
	// The write itself still landed.
	if _, err := os.Stat(dest); err != nil {
		t.Errorf("written file missing after a failed commit: %v", err)
	}
	logged := gitLog(t)
	if !strings.Contains(logged, "cannot lock ref") {
		t.Errorf("log record dropped the cause below git's first line:\n%q", logged)
	}
	if n := strings.Count(logged, "\n"); n != 1 {
		t.Errorf("expected 1 log record, got %d:\n%q", n, logged)
	}
}

// seedRemoteStore is seedStore with a bare repo alongside it as origin and the
// branch tracking it, so a gesture's push has somewhere to land. file:// rather
// than a bare path: it is the form a real store's remote takes, and it is what
// makes the push a push rather than a local shortcut.
func seedRemoteStore(t *testing.T) (root, remote string) {
	t.Helper()
	root = seedStore(t)
	remote = filepath.Join(t.TempDir(), "origin.git")
	testGit(t, root, "init", "--bare", remote)
	testGit(t, root, "remote", "add", "origin", "file://"+remote)
	testGit(t, root, "push", "-u", "origin", "HEAD")
	return root, remote
}

// TestApplyPushesTheCommit: the store's commits are the only copy of a human's
// promote and reject decisions, so a gesture that commits and stops leaves the
// irreplaceable part of the store unpublished. The push is the gesture's, which
// is what bounds that window.
func TestApplyPushesTheCommit(t *testing.T) {
	root, remote := seedRemoteStore(t)
	branch := testGit(t, root, "symbolic-ref", "--short", "HEAD")
	// The seed left the remote's ref at HEAD, so "published == head" alone holds
	// for a gesture that committed nothing. The commit has to be seen to move.
	before := testGit(t, root, "rev-parse", "HEAD")

	warn, err := Apply("write one", func(tx *Tx) error {
		return tx.WriteFile(filepath.Join(root, "truths", "loom", "one.md"), []byte("one\n"))
	})

	if err != nil || warn != (Warn{}) {
		t.Fatalf("Apply: err=%v warn=%+v", err, warn)
	}
	head := testGit(t, root, "rev-parse", "HEAD")
	if head == before {
		t.Fatalf("HEAD did not move: the gesture left no commit to push (%s)", head)
	}
	if published := testGit(t, remote, "rev-parse", branch); published != head {
		t.Errorf("remote ref = %s, want the commit the gesture made (%s)", published, head)
	}
	if st := testGit(t, root, "status", "-sb"); strings.Contains(st, "ahead") {
		t.Errorf("branch is still ahead of its upstream after the gesture:\n%s", st)
	}
}

// TestApplyPushIgnoresHostPushConfig: the push names its remote and its
// remote-side ref, so the store cannot inherit a push it did not ask for.
// push.default=nothing would refuse a plain push every time — a not-pushed warn
// no later gesture could heal — matching would carry a second branch nobody
// pointed the store at, and followTags would publish a tag this gesture never
// touched. All three are set here, and exactly the current branch's commit is
// what lands.
func TestApplyPushIgnoresHostPushConfig(t *testing.T) {
	root, remote := seedRemoteStore(t)
	branch := testGit(t, root, "symbolic-ref", "--short", "HEAD")
	testGit(t, root, "checkout", "-b", "side")
	testGit(t, root, "commit", "--allow-empty", "-m", "side seed")
	testGit(t, root, "push", "origin", "side")
	published := testGit(t, root, "rev-parse", "side")
	testGit(t, root, "commit", "--allow-empty", "-m", "side work the gesture never touched")
	testGit(t, root, "checkout", branch)
	testGit(t, root, "tag", "local-tag")
	testGit(t, root, "config", "push.default", "nothing")
	testGit(t, root, "config", "push.followTags", "true")

	warn, err := Apply("write one", func(tx *Tx) error {
		return tx.WriteFile(filepath.Join(root, "truths", "loom", "one.md"), []byte("one\n"))
	})

	if err != nil || warn != (Warn{}) {
		t.Fatalf("Apply: err=%v warn=%+v", err, warn)
	}
	head := testGit(t, root, "rev-parse", "HEAD")
	if got := testGit(t, remote, "rev-parse", branch); got != head {
		t.Errorf("remote %s = %s, want the commit the gesture made (%s)", branch, got, head)
	}
	if got := testGit(t, remote, "rev-parse", "side"); got != published {
		t.Errorf("remote side moved: %s -> %s, a branch the gesture never touched", published, got)
	}
	if tags := testGit(t, remote, "tag"); tags != "" {
		t.Errorf("push published tags the gesture never touched:\n%s", tags)
	}
}

// TestApplyFailedPushKeepsTheCommit: a push that fails is not the commit
// failing. The record is correct and complete locally, the next gesture's push
// carries it, and rolling the commit back to keep the branch in sync would throw
// away the human judgment the commit exists to hold.
func TestApplyFailedPushKeepsTheCommit(t *testing.T) {
	root, remote := seedRemoteStore(t)
	head := testGit(t, root, "rev-parse", "HEAD")
	// An absent local path rather than an unreachable host: git fails at once,
	// so the test asserts the store's handling instead of waiting out a network
	// timeout.
	testGit(t, root, "remote", "set-url", "origin", filepath.Join(filepath.Dir(remote), "absent.git"))

	warn, err := Apply("write one", func(tx *Tx) error {
		return tx.WriteFile(filepath.Join(root, "truths", "loom", "one.md"), []byte("one\n"))
	})

	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if warn.NotCommitted != "" {
		t.Errorf("a failed push reported the commit as missing: %q", warn.NotCommitted)
	}
	if warn.NotPushed == "" {
		t.Fatal("a failed push reported a published record")
	}
	if now := testGit(t, root, "rev-parse", "HEAD"); now == head {
		t.Error("HEAD did not move: a failed push took the commit with it")
	}
	if st := testGit(t, root, "status", "-sb"); !strings.Contains(st, "ahead 1") {
		t.Errorf("branch is not ahead of its upstream after a failed push:\n%s", st)
	}
	logged := gitLog(t)
	if !strings.Contains(logged, "write one") || !strings.Contains(logged, "git push") {
		t.Errorf("log record does not name the gesture and the failed push:\n%q", logged)
	}
}

// TestApplyNoUpstreamDegrades: a store nobody configured a remote for is the
// bootstrap's own shape, not a failure. It degrades like a store that is not a
// repo — a stated reason, no error, and the commit still lands.
func TestApplyNoUpstreamDegrades(t *testing.T) {
	root := seedStore(t)
	head := testGit(t, root, "rev-parse", "HEAD")

	warn, err := Apply("write one", func(tx *Tx) error {
		return tx.WriteFile(filepath.Join(root, "truths", "loom", "one.md"), []byte("one\n"))
	})

	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if warn.NotCommitted != "" {
		t.Errorf("warn = %q, want the commit to have landed", warn.NotCommitted)
	}
	if warn.NotPushed != errNoUpstream.Error() {
		t.Errorf("push warn = %q, want %q", warn.NotPushed, errNoUpstream.Error())
	}
	if now := testGit(t, root, "rev-parse", "HEAD"); now == head {
		t.Error("HEAD did not move: the commit did not land")
	}
}

// TestApplyDetachedHeadDegrades: a detached HEAD names no branch whose tracking
// configuration the push could resolve, so it degrades with its own reason
// rather than moving a ref nobody pointed us at.
func TestApplyDetachedHeadDegrades(t *testing.T) {
	root, _ := seedRemoteStore(t)
	testGit(t, root, "checkout", "--detach")

	warn, err := Apply("write one", func(tx *Tx) error {
		return tx.WriteFile(filepath.Join(root, "truths", "loom", "one.md"), []byte("one\n"))
	})

	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if warn.NotCommitted != "" {
		t.Errorf("warn = %q, want the commit to have landed", warn.NotCommitted)
	}
	if warn.NotPushed != errDetachedHead.Error() {
		t.Errorf("push warn = %q, want %q", warn.NotPushed, errDetachedHead.Error())
	}
}

// TestApplyHooksDoNotRun: the store's hooks stay out of every writer. One that
// blocked would hang the TUI with no way to answer it and wedge an unattended
// sweep with nobody there to; the store is a data store loom's bootstrap
// creates, not a checkout whose hooks anyone meant to run here.
func TestApplyHooksDoNotRun(t *testing.T) {
	root := seedStore(t)
	hook := filepath.Join(root, ".git", "hooks", "pre-commit")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	testGit(t, root, "config", "core.hooksPath", filepath.Dir(hook))

	warn, err := Apply("write one", func(tx *Tx) error {
		return tx.WriteFile(filepath.Join(root, "truths", "loom", "one.md"), []byte("one\n"))
	})

	if err != nil || warn.NotCommitted != "" {
		t.Fatalf("Apply: err=%v warn=%q", err, warn.NotCommitted)
	}
	if subject := testGit(t, root, "log", "-1", "--format=%s"); subject != "write one" {
		t.Errorf("commit subject = %q", subject)
	}
}

// forgedRecord carries the shapes an extractor-written frontmatter id could
// smuggle into a commit subject or a log record: a line break, a control
// character, and the format characters that reorder a rendered record without
// appearing in it (a right-to-left override and a bidi isolate).
const forgedRecord = "write one\ninjected: forged\x07\u202e\u2066"

func TestApplyCommitSubjectIsOneLine(t *testing.T) {
	root := seedStore(t)

	warn, err := Apply(forgedRecord, func(tx *Tx) error {
		return tx.WriteFile(filepath.Join(root, "truths", "loom", "one.md"), []byte("one\n"))
	})

	if err != nil || warn.NotCommitted != "" {
		t.Fatalf("Apply: err=%v warn=%q", err, warn.NotCommitted)
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

func TestApplyLogRecordIsOneLine(t *testing.T) {
	// A non-repo store, so the commit degrades and writes the log line.
	root := seedRoot(t)

	if _, err := Apply(forgedRecord, func(tx *Tx) error {
		return tx.WriteFile(filepath.Join(root, "truths", "loom", "one.md"), []byte("one\n"))
	}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	logged := gitLog(t)
	if n := strings.Count(logged, "\n"); n != 1 {
		t.Errorf("expected 1 log record, got %d:\n%q", n, logged)
	}
	if strings.Contains(logged, "\x07") {
		t.Errorf("log record retains a control character:\n%q", logged)
	}
	if strings.ContainsAny(logged, "\u202e\u2066") {
		t.Errorf("log record retains a bidi format character:\n%q", logged)
	}
}

// TestSanitizeRecordBoundsRunes: the bound counts runes, so a non-ASCII id near
// the limit is never cut mid-rune into the very record the flattening exists to
// keep readable.
func TestSanitizeRecordBoundsRunes(t *testing.T) {
	bounded := SanitizeRecord(strings.Repeat("é", MessageMax+50))

	if n := len([]rune(bounded)); n != MessageMax {
		t.Errorf("bounded record is %d runes, want %d", n, MessageMax)
	}
	if !strings.HasSuffix(bounded, "…") {
		t.Errorf("bounded record does not read as truncated: %q", bounded)
	}
	if !utf8Valid(bounded) {
		t.Errorf("bounded record is not valid UTF-8: %q", bounded)
	}
}

func utf8Valid(s string) bool {
	for _, r := range s {
		if r == '\uFFFD' {
			return false
		}
	}
	return true
}

// TestApplyRefusesWritesOutsideTheStore: the plan `loom knowledge write` applies
// is a string a non-Go writer composed, and a mis-rooted or traversing path
// would otherwise write, delete or rename anywhere the user can reach while
// reporting nothing worse than a commit warning. Confinement is the Tx's, so it
// covers every op and both sides of a rename. A path that escapes through a
// symlink is refused for the symlink before it is ever resolved, so the open
// root's own refusal is the backstop below this — for a link swapped in between
// the check and the syscall, which is the case no test can stage.
func TestApplyRefusesWritesOutsideTheStore(t *testing.T) {
	root := seedStore(t)
	outside := t.TempDir()
	external := filepath.Join(outside, "keep.md")
	if err := os.WriteFile(external, []byte("untouched\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A symlink inside the store pointing out of it: the unresolved path reads
	// as inside, so only resolving it catches the escape.
	link := filepath.Join(root, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(root, "truths", "loom", "one.md")
	if err := os.MkdirAll(filepath.Dir(inside), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inside, []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// The two refusals the Tx makes by itself: a path that is not in the store,
	// and one that would be reached through a link the store does not write
	// through — which is what a path out of the store through a symlink hits.
	const outsideStore = "outside the knowledge store"
	const throughALink = "is a symlink, which the knowledge store does not write through"

	for _, tc := range []struct {
		name string
		want string
		op   func(tx *Tx) error
	}{
		{"absolute path outside", outsideStore, func(tx *Tx) error { return tx.WriteFile(external, []byte("forged\n")) }},
		{"traversal out of the store", outsideStore, func(tx *Tx) error {
			return tx.WriteFile(filepath.Join(root, "..", filepath.Base(outside), "keep.md"), []byte("forged\n"))
		}},
		{"through a symlinked directory", throughALink, func(tx *Tx) error {
			return tx.WriteFile(filepath.Join(link, "keep.md"), []byte("forged\n"))
		}},
		{"remove outside", outsideStore, func(tx *Tx) error { return tx.Remove(external) }},
		{"append outside", outsideStore, func(tx *Tx) error { return tx.Append(external, "forged\n") }},
		{"touch outside", outsideStore, func(tx *Tx) error { return tx.Touch(external) }},
		{"rename out of the store", throughALink, func(tx *Tx) error { return tx.Rename(inside, filepath.Join(link, "moved.md")) }},
		{"rename into the store from outside", outsideStore, func(tx *Tx) error {
			return tx.Rename(external, filepath.Join(root, "truths", "loom", "stolen.md"))
		}},
		{"remove through a symlinked directory", throughALink, func(tx *Tx) error {
			return tx.Remove(filepath.Join(link, "keep.md"))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			head := testGit(t, root, "rev-parse", "HEAD")

			warn, err := Apply("write outside the store", tc.op)

			if err == nil {
				t.Fatal("a path outside the store was accepted")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want a reason naming %q", err, tc.want)
			}
			if tc.want == outsideStore && !strings.Contains(err.Error(), root) && !strings.Contains(err.Error(), shortenPath(root)) {
				t.Errorf("err = %v, does not name the root it was held against", err)
			}
			if warn.NotCommitted != "" {
				t.Errorf("warn = %q, want no commit for a unit of work that wrote nothing", warn.NotCommitted)
			}
			if body, err := os.ReadFile(external); err != nil || string(body) != "untouched\n" {
				t.Errorf("the external file was touched: %q, %v", string(body), err)
			}
			if _, err := os.Stat(inside); err != nil {
				t.Errorf("the in-store file was moved out: %v", err)
			}
			if now := testGit(t, root, "rev-parse", "HEAD"); now != head {
				t.Errorf("HEAD moved for a refused write: %s -> %s", head, now)
			}
		})
	}
}

// TestApplyCommitsThroughASymlinkedRoot: a store reached through a symlink — a
// LOOM_KNOWLEDGE_ROOT pointing at one, and /var/folders on macOS — is still the
// store, so containment must resolve both sides rather than compare strings.
func TestApplyCommitsThroughASymlinkedRoot(t *testing.T) {
	real := seedStore(t)
	link := filepath.Join(t.TempDir(), "knowledge")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LOOM_KNOWLEDGE_ROOT", link)

	warn, err := Apply("write one", func(tx *Tx) error {
		return tx.WriteFile(filepath.Join(link, "truths", "loom", "one.md"), []byte("one\n"))
	})

	if err != nil || warn.NotCommitted != "" {
		t.Fatalf("Apply: err=%v warn=%q", err, warn.NotCommitted)
	}
	if names := committed(t, real); !strings.Contains(names, "A\ttruths/loom/one.md") {
		t.Errorf("commit does not add the written file:\n%s", names)
	}
}

// TestApplyNoChangeLeavesNoCommit: a recorded path is not by itself a change.
// $EDITOR quit without saving is the common case — the pathspec holds one
// unmodified tracked file — and committing it would report a phantom failure
// ("nothing to commit") and log a record for a gesture where nothing was lost.
func TestApplyNoChangeLeavesNoCommit(t *testing.T) {
	root := seedStore(t)
	tracked := filepath.Join(root, "log.md")
	head := testGit(t, root, "rev-parse", "HEAD")

	warn, err := Apply("edit truth loom/unchanged", func(tx *Tx) error {
		return tx.Touch(tracked)
	})

	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if warn.NotCommitted != "" {
		t.Errorf("warn = %q, want no failure for a path that did not change", warn.NotCommitted)
	}
	if now := testGit(t, root, "rev-parse", "HEAD"); now != head {
		t.Errorf("HEAD moved for a path that did not change: %s -> %s", head, now)
	}
	if staged := testGit(t, root, "diff", "--cached", "--name-only"); staged != "" {
		t.Errorf("the unchanged path was left staged:\n%s", staged)
	}
	// Nothing to commit is not a failure, so nothing is written down as one.
	if _, err := os.Stat(filepath.Join(config.Home(), "knowledge-git.log")); !os.IsNotExist(err) {
		t.Errorf("a no-op wrote a failure record: %v", err)
	}
}

// TestApplyInCommitsAgainstTheRootItIsGiven: the caller that resolves the store
// itself — internal/extract, from the extractor's persisted tunables — gets the
// writes, the containment check and the commit held against that root rather
// than against knowledge.Root().
func TestApplyInCommitsAgainstTheRootItIsGiven(t *testing.T) {
	root := seedStore(t)
	// knowledge.Root() now points elsewhere: ApplyIn must ignore it.
	t.Setenv("LOOM_KNOWLEDGE_ROOT", t.TempDir())

	warn, err := ApplyIn(root, "retrospect loom/one | loom | 1 truth candidates, 0 decision candidates",
		func(tx *Tx) error {
			return tx.Append(filepath.Join(root, "log.md"), "\n## [2026-08-23] retrospect loom/one\n")
		})

	if err != nil || warn.NotCommitted != "" {
		t.Fatalf("ApplyIn: err=%v warn=%q", err, warn.NotCommitted)
	}
	if names := committed(t, root); !strings.Contains(names, "M\tlog.md") {
		t.Errorf("commit does not record the entry:\n%s", names)
	}
	if st := testGit(t, root, "status", "--porcelain", "--", filepath.Join(root, "log.md")); st != "" {
		t.Errorf("log.md still dirty after ApplyIn:\n%s", st)
	}
}

// TestApplyInConfinesToTheRootItIsGiven: containment follows the root the unit
// of work is against, or routing a caller with its own root through the entry
// point would refuse every write it makes.
func TestApplyInConfinesToTheRootItIsGiven(t *testing.T) {
	root := seedStore(t)
	other := t.TempDir()
	t.Setenv("LOOM_KNOWLEDGE_ROOT", other)

	if _, err := ApplyIn(other, "write into the other root", func(tx *Tx) error {
		return tx.WriteFile(filepath.Join(root, "truths", "loom", "one.md"), []byte("one\n"))
	}); err == nil {
		t.Fatal("a path outside the given root was accepted")
	}
	if _, err := ApplyIn(other, "write into the other root", func(tx *Tx) error {
		return tx.WriteFile(filepath.Join(other, "truths", "loom", "one.md"), []byte("one\n"))
	}); err != nil {
		t.Fatalf("a path inside the given root was refused: %v", err)
	}
}

// TestApplyRefusesTheStoresOwnRepository: the repository is the store's history,
// not its content. A plan that rewrote .git/config or .git/HEAD would corrupt
// it, and one that dropped a hook would leave code to run under the next git
// command a human types in the store — each landing before the commit that
// follows it fails. os.Root knows nothing about .git, so the Tx says so itself.
func TestApplyRefusesTheStoresOwnRepository(t *testing.T) {
	root := seedStore(t)
	config := filepath.Join(root, ".git", "config")
	hook := filepath.Join(root, ".git", "hooks", "pre-commit")
	before, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(root, "truths", "loom", "one.md")

	for _, tc := range []struct {
		name string
		op   func(tx *Tx) error
	}{
		{"write config", func(tx *Tx) error { return tx.WriteFile(config, []byte("[core]\n\tbare = true\n")) }},
		{"write a hook", func(tx *Tx) error { return tx.WriteFile(hook, []byte("#!/bin/sh\ntouch /tmp/pwned\n")) }},
		{"remove config", func(tx *Tx) error { return tx.Remove(config) }},
		{"remove a hook", func(tx *Tx) error { return tx.Remove(hook) }},
		{"rename config away", func(tx *Tx) error { return tx.Rename(config, inside) }},
		{"rename onto a hook", func(tx *Tx) error { return tx.Rename(inside, hook) }},
		{"touch config", func(tx *Tx) error { return tx.Touch(config) }},
		{"append to config", func(tx *Tx) error { return tx.Append(config, "\n[core]\n\tbare = true\n") }},
		// The store usually sits on a case-insensitive volume, where .GIT names
		// the same directory.
		{"write config through .GIT", func(tx *Tx) error {
			return tx.WriteFile(filepath.Join(root, ".GIT", "config"), []byte("[core]\n"))
		}},
		// A worktree's .git is a file, and a nested repository's is neither the
		// store's own nor content a writer has any business rewriting.
		{"write a nested .git file", func(tx *Tx) error {
			return tx.WriteFile(filepath.Join(root, "truths", ".git"), []byte("gitdir: /elsewhere\n"))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			head := testGit(t, root, "rev-parse", "HEAD")

			warn, err := Apply("write the repository", tc.op)

			if err == nil {
				t.Fatal("a path inside the store's repository was accepted")
			}
			if !strings.Contains(err.Error(), "the store's own git repository") {
				t.Errorf("err = %v, want a reason naming the rule", err)
			}
			if warn.NotCommitted != "" {
				t.Errorf("warn = %q, want no commit for a unit of work that wrote nothing", warn.NotCommitted)
			}
			if now, err := os.ReadFile(config); err != nil || string(now) != string(before) {
				t.Errorf("the repository's config was touched: %v", err)
			}
			if _, err := os.Stat(hook); !os.IsNotExist(err) {
				t.Errorf("a hook was left in the store's repository: %v", err)
			}
			if now := testGit(t, root, "rev-parse", "HEAD"); now != head {
				t.Errorf("HEAD moved for a refused write: %s -> %s", head, now)
			}
		})
	}
}

// TestApplyRefusesARootItCannotOpen: a store is bootstrapped before anything
// writes to it, so a root that is absent or is not a directory is a
// misconfiguration to report
// rather than a tree to create — a writer that materialized one would scatter a
// store across whatever the misconfiguration pointed at.
func TestApplyRefusesARootItCannotOpen(t *testing.T) {
	parent := seedRoot(t)
	file := filepath.Join(parent, "not-a-directory")
	if err := os.WriteFile(file, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, root := range []string{filepath.Join(parent, "absent"), file} {
		warn, err := ApplyIn(root, "write one", func(tx *Tx) error {
			return tx.WriteFile(filepath.Join(root, "truths", "loom", "one.md"), []byte("one\n"))
		})

		if err == nil {
			t.Fatalf("root %s was accepted", root)
		}
		if !strings.Contains(err.Error(), "knowledge store") || !strings.Contains(err.Error(), shortenPath(root)) {
			t.Errorf("err = %v, does not name the store it could not open", err)
		}
		if warn.NotCommitted != "" {
			t.Errorf("warn = %q, want no commit for a store that was never opened", warn.NotCommitted)
		}
		// Checked for absence rather than for ErrNotExist: a root that is a file
		// answers "not a directory", which is equally a tree that was not created.
		if _, err := os.Stat(filepath.Join(root, "truths")); err == nil {
			t.Error("a store was created under an unopenable root")
		}
	}
	// The failure is written down like any other, not swallowed.
	if logged := gitLog(t); !strings.Contains(logged, "write one") {
		t.Errorf("log record does not name the unit of work:\n%q", logged)
	}
}

// TestApplyRefusesASymlinkInTheStore: the .git rule is on the path's name, and a
// symlink inside the store supplies the target without the name — `alias -> .git`
// reaches the repository through a path with no .git component, and the open
// root permits it, since the target stays inside the root. Every in-store
// symlink is refused rather than the ones aimed at .git: git records a symlink
// as a symlink, so writing through one writes outside the tree git tracks, and
// the commit would record something git never had.
func TestApplyRefusesASymlinkInTheStore(t *testing.T) {
	root := seedStore(t)
	config := filepath.Join(root, ".git", "config")
	before, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	notes := filepath.Join(root, "notes.md")
	if err := os.WriteFile(notes, []byte("tracked\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testGit(t, root, "add", "-A")
	testGit(t, root, "commit", "-m", "seed notes")
	// Relative targets: an absolute one is refused by the open root outright,
	// and these have to be links it would otherwise happily follow.
	for _, link := range []struct{ name, target string }{
		{"alias", ".git"},      // a directory symlink into the repository
		{"cfg", ".git/config"}, // a final-component symlink to a repository file
		{"plain", "notes.md"},  // an ordinary one, aimed at store content
	} {
		if err := os.Symlink(link.target, filepath.Join(root, link.name)); err != nil {
			t.Fatal(err)
		}
	}

	for _, tc := range []struct {
		name string
		op   func(tx *Tx) error
	}{
		{"write a hook through a directory link", func(tx *Tx) error {
			return tx.WriteFile(filepath.Join(root, "alias", "hooks", "pre-commit"), []byte("#!/bin/sh\ntouch /tmp/pwned\n"))
		}},
		{"append to config through a directory link", func(tx *Tx) error {
			return tx.Append(filepath.Join(root, "alias", "config"), "\n[core]\n\tbare = true\n")
		}},
		{"write through a link to config", func(tx *Tx) error {
			return tx.WriteFile(filepath.Join(root, "cfg"), []byte("[core]\n\tbare = true\n"))
		}},
		{"append through a link to config", func(tx *Tx) error {
			return tx.Append(filepath.Join(root, "cfg"), "\n[core]\n\tbare = true\n")
		}},
		{"write through an ordinary link", func(tx *Tx) error {
			return tx.WriteFile(filepath.Join(root, "plain"), []byte("rewritten\n"))
		}},
		{"append through an ordinary link", func(tx *Tx) error {
			return tx.Append(filepath.Join(root, "plain"), "appended\n")
		}},
		{"remove a link", func(tx *Tx) error { return tx.Remove(filepath.Join(root, "cfg")) }},
		{"rename through a link", func(tx *Tx) error {
			return tx.Rename(notes, filepath.Join(root, "alias", "notes.md"))
		}},
		{"touch a link", func(tx *Tx) error { return tx.Touch(filepath.Join(root, "plain")) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			head := testGit(t, root, "rev-parse", "HEAD")

			warn, err := Apply("write through a link", tc.op)

			if err == nil {
				t.Fatal("a path through a symlink was accepted")
			}
			if !strings.Contains(err.Error(), "is a symlink") {
				t.Errorf("err = %v, want a reason naming the link", err)
			}
			if warn.NotCommitted != "" {
				t.Errorf("warn = %q, want no commit for a unit of work that wrote nothing", warn.NotCommitted)
			}
			if now, err := os.ReadFile(config); err != nil || string(now) != string(before) {
				t.Errorf("the repository's config was touched: %v", err)
			}
			if _, err := os.Stat(filepath.Join(root, ".git", "hooks", "pre-commit")); !os.IsNotExist(err) {
				t.Errorf("a hook was left in the store's repository: %v", err)
			}
			if now, err := os.ReadFile(notes); err != nil || string(now) != "tracked\n" {
				t.Errorf("the linked file was written through: %q, %v", string(now), err)
			}
			if now := testGit(t, root, "rev-parse", "HEAD"); now != head {
				t.Errorf("HEAD moved for a refused write: %s -> %s", head, now)
			}
		})
	}
}

// TestApplyRefusesTouchingADirectory: Touch records without writing, so it is
// the one op a directory can reach — and a directory in the pathspec stages
// everything dirty beneath it, which is the absorption the path scoping exists
// to prevent. The store root resolves to one, so a path that collapsed to the
// whole store is the same refusal.
func TestApplyRefusesTouchingADirectory(t *testing.T) {
	root := seedStore(t)
	dir := filepath.Join(root, "truths", "loom")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Dirt the store carries that a directory pathspec would absorb.
	if err := os.WriteFile(filepath.Join(dir, "stray.md"), []byte("untracked\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{dir, root} {
		head := testGit(t, root, "rev-parse", "HEAD")

		warn, err := Apply("edit truth loom/a-directory", func(tx *Tx) error {
			return tx.Touch(path)
		})

		if err == nil {
			t.Fatalf("%s was accepted as a path to record", path)
		}
		if !strings.Contains(err.Error(), "is a directory") {
			t.Errorf("err = %v, want a reason naming the directory", err)
		}
		if warn.NotCommitted != "" {
			t.Errorf("warn = %q, want no commit for a unit of work that wrote nothing", warn.NotCommitted)
		}
		if now := testGit(t, root, "rev-parse", "HEAD"); now != head {
			t.Errorf("HEAD moved for a refused touch: %s -> %s", head, now)
		}
		if st := testGit(t, root, "status", "--porcelain", "-uall"); !strings.Contains(st, "stray.md") {
			t.Errorf("the untracked file was absorbed:\n%s", st)
		}
	}
}

// TestApplyDeferredCommitsCalledConcurrentlyBothLand: ApplyDeferred lets one
// process hold several units of work's commits at once — the TUI's gestures run
// theirs as a tea.Cmd, so two are called together rather than ordered by the
// update loop — and two overlapping `git add`/`commit` in one repo contend on
// index.lock, which git answers by failing rather than waiting. The store
// serializes its commits in-process, so the two run one after the other; this is
// a smoke check that both records land, not a deterministic reproduction of that
// contention — without the serialization the two git sequences may simply not
// overlap.
func TestApplyDeferredCommitsCalledConcurrentlyBothLand(t *testing.T) {
	root, _ := seedRemoteStore(t)
	before := testGit(t, root, "rev-parse", "HEAD")

	var commits []Commit
	for _, name := range []string{"one", "two"} {
		commit, err := ApplyDeferred(root, "write truths/loom/"+name+".md", func(tx *Tx) error {
			return tx.WriteFile(filepath.Join(root, "truths", "loom", name+".md"), []byte(name+"\n"))
		})
		if err != nil {
			t.Fatalf("ApplyDeferred %s: %v", name, err)
		}
		commits = append(commits, commit)
	}

	warns := make([]Warn, len(commits))
	var wg sync.WaitGroup
	for i, commit := range commits {
		wg.Add(1)
		go func() {
			defer wg.Done()
			warns[i] = commit()
		}()
	}
	wg.Wait()

	for i, warn := range warns {
		if warn != (Warn{}) {
			t.Errorf("commit %d: warn = %+v", i, warn)
		}
	}
	if n := testGit(t, root, "rev-list", "--count", before+"..HEAD"); n != "2" {
		t.Errorf("commits since the seed = %s, want one per unit of work", n)
	}
}
