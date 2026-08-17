package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"loom/internal/config"
)

// gitTimeout bounds every git invocation. Promote and reject run inside the
// fullscreen TUI, where a child that blocks — on an index lock, a credential or
// a signing prompt — is unrecoverable for the user.
const gitTimeout = 10 * time.Second

// gitReasonMax bounds the git failure text that reaches the status line, so a
// multi-line failure contributes only its head to a bar that already carries the
// destination path. It is not what keeps the line inside the terminal — the view
// clamps the composed status to the window width — and it bounds nothing that is
// written down: knowledge-git.log keeps git's whole output.
const gitReasonMax = 60

// gestureMessageMax bounds the commit subject, which is also the body of the
// knowledge-git log record.
const gestureMessageMax = 200

// errNoGitRepo marks a knowledge root that is not under version control, so the
// gesture can say the store isn't a repo rather than relay a git failure.
var errNoGitRepo = errors.New("knowledge root is not a git repo")

// errEnclosingRepo marks a knowledge root that is itself untracked but sits
// inside a repo — a git-managed home directory, a dotfiles checkout. It degrades
// exactly like errNoGitRepo but needs its own reason, because the user can
// falsify "not a git repo" by running git status in the store.
var errEnclosingRepo = errors.New("knowledge root is inside another git repo")

// knowledgeGitLogPath returns the canonical log file for knowledge-store
// commits that could not be recorded.
func knowledgeGitLogPath() string {
	return filepath.Join(config.Home(), "knowledge-git.log")
}

// runGit runs one git command in the knowledge store, returning its combined
// output. commit.gpgsign=false because a signing passphrase prompt would hang
// the TUI with no way to answer it.
func runGit(root string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	full := append([]string{"-C", root, "-c", "commit.gpgsign=false"}, args...)
	out, err := exec.CommandContext(ctx, "git", full...).CombinedOutput()
	// CommandContext reports a killed child as a generic signal, so the
	// deadline that fired is visible only on the context — and a child killed
	// mid-prompt has printed nothing to attribute the failure to.
	if err != nil && ctx.Err() != nil {
		err = ctx.Err()
	}
	return strings.TrimSpace(string(out)), err
}

// gitCause pairs git's output with the error that ended the child, so the two
// failures that print nothing — a git that could not be run, a child killed by
// gitTimeout — still record a cause.
func gitCause(out string, err error) string {
	if out == "" {
		return err.Error()
	}
	return err.Error() + ": " + out
}

// commitKnowledge records the paths a gesture touched as one commit in the
// knowledge store. The commit is path-scoped — `git add` over those paths, then
// `commit --only` with the same pathspec — because the store's working tree is
// routinely dirty with untracked candidates and edits this gesture did not
// make, and a whole-tree commit would absorb them into the record. A root that
// is not itself a git repo yields errNoGitRepo. Failures carry git's whole
// output, because the useful part is rarely the first line — a rejected commit
// leads with "On branch main" and names the cause below it — and the caller,
// not this function, decides what a one-line status bar can show.
func commitKnowledge(paths []string, message string) error {
	root := KnowledgeRoot()
	top, err := runGit(root, "rev-parse", "--show-toplevel")
	if err != nil {
		// rev-parse fails both for a store that is not a repo and for a git we
		// could not run at all — missing from PATH, or killed by gitTimeout.
		// Only an exit status means git ran and answered; blaming the store's
		// layout for anything else would send the user to fix a healthy repo.
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			return fmt.Errorf("git rev-parse: %s", gitCause(top, err))
		}
		return fmt.Errorf("%w: %s", errNoGitRepo, gitCause(top, err))
	}
	// rev-parse walks up the tree, so a store that is merely *inside* a repo —
	// a git-managed home directory, a dotfiles checkout — resolves to that
	// ancestor and the commit would land in history the user never pointed us
	// at. The toplevel has to be the store itself.
	if !sameDir(top, root) {
		return fmt.Errorf("%w: enclosing repo at %s", errEnclosingRepo, top)
	}
	// A path the gesture removed matters to git only if it was tracked; the
	// store carries uncommitted candidates, and passing one as a pathspec after
	// its removal is a fatal "did not match any files" that would sink the
	// whole commit.
	var pathspec []string
	for _, p := range paths {
		if _, err := os.Stat(p); err != nil {
			if _, err := runGit(root, "ls-files", "--error-unmatch", "--", p); err != nil {
				continue
			}
		}
		pathspec = append(pathspec, p)
	}
	if len(pathspec) == 0 {
		return errors.New("no tracked paths to commit")
	}
	if out, err := runGit(root, append([]string{"add", "--"}, pathspec...)...); err != nil {
		return fmt.Errorf("git add: %s", gitCause(out, err))
	}
	if out, err := runGit(root, append([]string{"commit", "--only", "-m", message, "--"}, pathspec...)...); err != nil {
		// A failed commit leaves the gesture staged, and the next commit a
		// human makes in the store would absorb it — the mirror of the
		// absorption the pathspec scoping exists to prevent. Unstaging also
		// drops any pre-gesture staging of these exact paths, which for a
		// just-written destination and a just-removed candidate is no real loss.
		runGit(root, append([]string{"reset", "-q", "--"}, pathspec...)...)
		return fmt.Errorf("git commit: %s", gitCause(out, err))
	}
	return nil
}

// recordKnowledgeCommit commits a gesture and returns "" on success or a short
// reason on failure. A failure is never silent: the failure is appended to
// knowledgeGitLogPath() in full, and its head handed back for the status line,
// which is gone by the next keystroke.
func recordKnowledgeCommit(paths []string, message string) string {
	message = sanitizeRecord(message)
	err := commitKnowledge(paths, message)
	if err == nil {
		return ""
	}
	// Flattened but not bounded: the log is the debugging mechanism for this
	// gesture, so it keeps every line git produced as one record.
	appendKnowledgeGitLog(message + ": " + flattenRecord(err.Error()))
	// Both no-repo shapes degrade the same way; the sentinel text is the whole
	// status-line reason, since the enclosing path detail belongs in the log.
	if errors.Is(err, errNoGitRepo) {
		return errNoGitRepo.Error()
	}
	if errors.Is(err, errEnclosingRepo) {
		return errEnclosingRepo.Error()
	}
	return shortGit(err.Error())
}

// sanitizeRecord flattens a gesture message into a single bounded record — the
// commit subject, which is also the body of the knowledge-git log line. The
// bound counts runes, not bytes: a non-ASCII id near the limit would otherwise
// be cut mid-rune, writing invalid UTF-8 into the very record flattenRecord
// exists to keep readable.
func sanitizeRecord(message string) string {
	flat := []rune(flattenRecord(message))
	if len(flat) <= gestureMessageMax {
		return string(flat)
	}
	return string(flat[:gestureMessageMax-1]) + "…"
}

// flattenRecord maps every rune that could break a one-line record onto a space:
// control characters, the Unicode format characters (bidi overrides and
// isolates, which reorder a record without appearing in it) and the line and
// paragraph separators. The text it guards names an artifact id and scope taken
// from candidate frontmatter the LLM extractor wrote out of a session
// transcript, so an unfiltered rune would forge a commit-message trailer, an
// extra knowledge-git.log line, or a misleading rendering of either —
// corrupting the audit trail these records exist to be.
func flattenRecord(s string) string {
	flat := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == '\u2028' || r == '\u2029' {
			return ' '
		}
		return r
	}, s)
	return strings.TrimSpace(flat)
}

// appendKnowledgeGitLog appends one timestamped line to the knowledge-git log.
func appendKnowledgeGitLog(line string) {
	p := knowledgeGitLogPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s\n", time.Now().Format(time.RFC3339), line)
}

// sameDir reports whether two paths name the same directory. Both sides are
// resolved first: git reports an absolute physical path while KnowledgeRoot()
// hands back LOOM_KNOWLEDGE_ROOT verbatim, which may be relative, and on macOS
// a store under /var/folders comes back as /private/var/folders — the raw
// strings would never compare equal either way.
func sameDir(a, b string) bool {
	ra, err := resolveDir(a)
	if err != nil {
		return false
	}
	rb, err := resolveDir(b)
	if err != nil {
		return false
	}
	return ra == rb
}

// resolveDir makes a path absolute and then resolves its symlinks.
func resolveDir(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}

// shortGit collapses a git failure to its first line, bounded, so it fits the
// one-line status bar. It is the display boundary only — callers log the whole
// text before shortening it.
func shortGit(out string) string {
	first, _, _ := strings.Cut(out, "\n")
	return truncate(first, gitReasonMax)
}
