package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"loom/internal/config"
)

// gitTimeout bounds one git invocation, not one unit of work: a unit of work
// runs several sequentially — commitKnowledge does rev-parse, a check-ignore per
// droppable path, an ls-files per path the work removed, add, diff --cached and
// commit, plus a reset when the commit failed, and pushKnowledge adds
// symbolic-ref, two config reads and the push — so what bounds a commit that
// pushes is the sum of those, on the order of a minute and a half rather than
// ten seconds. Apply runs both inside the fullscreen TUI and unattended on the
// LaunchAgent, and a child that blocks — on an index lock, a credential or a
// signing prompt — wedges the sweep with nobody there to answer it. The TUI's
// gestures run their commit as a tea.Cmd rather than on the update loop, so the
// sum is not paid on a frame; the unattended sweep is what the per-call bound
// protects.
const gitTimeout = 10 * time.Second

// gitReasonMax bounds the git failure text that reaches the status line, so a
// multi-line failure contributes only its head to a bar that already carries the
// destination path. It is not what keeps the line inside the terminal — the view
// clamps the composed status to the window width — and it bounds nothing that is
// written down: knowledge-git.log keeps git's whole output.
const gitReasonMax = 60

// MessageMax bounds the commit subject, which is also the body of the
// knowledge-git log record.
const MessageMax = 200

// errNoGitRepo marks a knowledge root that is not under version control, so the
// caller can say the store isn't a repo rather than relay a git failure.
var errNoGitRepo = errors.New("knowledge root is not a git repo")

// errEnclosingRepo marks a knowledge root that is itself untracked but sits
// inside a repo — a git-managed home directory, a dotfiles checkout. It degrades
// exactly like errNoGitRepo but needs its own reason, because the user can
// falsify "not a git repo" by running git status in the store.
var errEnclosingRepo = errors.New("knowledge root is inside another git repo")

// errNoUpstream marks a branch whose tracking configuration names nowhere to
// push: no remote at all, or no branch.<name>.remote / branch.<name>.merge for
// this branch. One sentinel for both, because the store's answer is the same —
// the commit stands and nothing publishes it — and the configuration detail
// belongs in the log rather than on the status line. Configuration that names a
// remote which does not exist is not this: it is a push that runs and fails,
// reported with git's own reason.
var errNoUpstream = errors.New("no upstream to push to")

// errDetachedHead marks a store whose HEAD names no branch, so there is no
// branch whose tracking configuration the push could resolve. It degrades like
// errNoUpstream rather than pushing a ref the user never asked us to move.
var errDetachedHead = errors.New("detached HEAD, no branch to push")

// Warn carries the two ways a unit of work can be short of a published record.
// They are separate fields because they degrade differently: NotCommitted means
// no commit records the work at all, while NotPushed means the record landed
// locally and is merely unpublished, which the next gesture's push heals when
// the failure was transient — a push is cumulative, so the next one carries this
// commit too, but a remote that has diverged rejects every later push the same
// way until a human pulls. Collapsing both onto one string would read a
// recoverable state as data loss. recordKnowledgeCommit sets at most one of
// them — a commit that did not land is never pushed — but the field pair is not
// exclusive, because a caller that composes its own record reason sets
// NotCommitted beside a NotPushed the store already returned: the TUI's promote
// and reject do exactly that when the gesture's closure failed after the commit
// landed. Read the two independently rather than as an either/or, or a status
// line drops the unpublished half.
type Warn struct {
	NotCommitted string // short reason no commit records the work
	NotPushed    string // short reason a commit that did land is still local
}

// knowledgeGitLogPath returns the canonical log file for knowledge-store
// commits that could not be recorded.
func knowledgeGitLogPath() string {
	return filepath.Join(config.Home(), "knowledge-git.log")
}

// runGit runs one git command in the knowledge store, returning its combined
// output. Signing and hooks are both pinned off for the same reason: a
// signing passphrase prompt would hang the TUI with no way to answer it, and a
// hook that blocks or rejects would wedge an unattended sweep with nobody there
// to answer it. The store is a data store loom's bootstrap creates, not a
// project checkout whose hooks anyone meant to run here.
// core.hooksPath=/dev/null names a location git finds no hooks in — it is a file
// rather than a directory, and git treats that as no hooks rather than an error.
func runGit(root string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	full := append([]string{"-C", root, "-c", "commit.gpgsign=false", "-c", "core.hooksPath=" + os.DevNull}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	// GIT_TERMINAL_PROMPT=0 turns a credential prompt into an immediate failure
	// rather than a child waiting on a terminal the fullscreen TUI has taken
	// over: the push is the one call that asks for credentials, and the user
	// could neither see nor answer the question. gitTimeout stays the backstop
	// for a network that hangs after authentication.
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	// GIT_TERMINAL_PROMPT closes the credential helper's prompt but not ssh's:
	// ssh reads a key passphrase from /dev/tty directly rather than from this
	// child's stdin, so it would draw over the fullscreen TUI's alt-screen and
	// compete for its keystrokes — and CommandContext kills only the git child,
	// leaving an ssh that holds the terminal past gitTimeout. BatchMode=yes
	// fails instead of asking; agent- and key-based auth are untouched. The
	// option is appended to a caller's own command rather than replacing it —
	// a GIT_SSH_COMMAND carrying an identity or a jump host is why one is set,
	// and dropping it would turn a working remote into an auth failure. ssh
	// keeps the first value it is given for an option, so a caller that spelled
	// out BatchMode=no keeps it: that is an explicit choice to be prompted, and
	// the appended option covers the case the guard is for, a command set for
	// its identity that never mentions BatchMode. A core.sshCommand in gitconfig
	// is overridden either way, since the env var wins over that setting.
	ssh := os.Getenv("GIT_SSH_COMMAND")
	if ssh == "" {
		ssh = "ssh"
	}
	cmd.Env = append(cmd.Env, "GIT_SSH_COMMAND="+ssh+" -o BatchMode=yes")
	out, err := cmd.CombinedOutput()
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

// commitKnowledge records the paths one unit of work touched as one commit in
// the knowledge store. The commit is path-scoped — `git add` over those paths,
// then `commit --only` with the same pathspec — because the store's working tree
// is routinely dirty with untracked candidates and edits this work did not
// make, and a whole-tree commit would absorb them into the record. droppable
// names the paths whose record lives elsewhere, committed alongside the rest but
// abandoned rather than allowed to sink the commit; every other path is the
// record and fails loudly; a dropped path is named in knowledgeGitLogPath(),
// since the commit that lands is otherwise indistinguishable from any other.
// That record is written when the path leaves the pathspec, so it stands even
// when the commit it was headed for then fails. A pathspec that turns out to
// hold no change is not an error and leaves no commit: a unit of work may name a
// path it did not alter, which is what committed reports — the caller pushes
// only what a commit produced. A root that is not itself a git repo yields
// errNoGitRepo. Failures carry git's whole output, because the useful part is
// rarely the first line — a rejected commit leads with "On branch main" and
// names the cause below it — and the caller, not this function, decides what a
// one-line status bar can show.
func commitKnowledge(root string, paths, droppable []string, message string) (committed bool, err error) {
	top, err := runGit(root, "rev-parse", "--show-toplevel")
	if err != nil {
		// rev-parse fails both for a store that is not a repo and for a git we
		// could not run at all — missing from PATH, or killed by gitTimeout.
		// Only an exit status means git ran and answered; blaming the store's
		// layout for anything else would send the user to fix a healthy repo.
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			return false, fmt.Errorf("git rev-parse: %s", gitCause(top, err))
		}
		return false, fmt.Errorf("%w: %s", errNoGitRepo, gitCause(top, err))
	}
	// rev-parse walks up the tree, so a store that is merely *inside* a repo —
	// a git-managed home directory, a dotfiles checkout — resolves to that
	// ancestor and the commit would land in history the user never pointed us
	// at. The toplevel has to be the store itself.
	if !sameDir(top, root) {
		return false, fmt.Errorf("%w: enclosing repo at %s", errEnclosingRepo, top)
	}
	// A droppable path leaves the pathspec when git is ignoring it: its record
	// is elsewhere — the reject archive's decision is the log.md entry — so the
	// store's ignore rules are a storage policy there, while passing an ignored
	// path to `git add` is a fatal "paths are ignored" that would sink the whole
	// commit. This is not a general rule about ignored paths: a path the caller
	// did not declare droppable is the record, and an ignored one has to fail
	// loudly rather than yield a commit that omits it. check-ignore exits 0 only
	// when the path is ignored — 1 when git would track it, 128 on an error it
	// could not answer — so only a clean exit drops the path.
	kept := append([]string{}, paths...)
	for _, p := range droppable {
		if _, err := runGit(root, "check-ignore", "-q", "--", p); err == nil {
			appendKnowledgeGitLog(message + ": dropping ignored path " + shortenPath(p))
			continue
		}
		kept = append(kept, p)
	}
	// A path the work removed matters to git only if it was tracked; the
	// store carries uncommitted candidates, and passing one as a pathspec after
	// its removal is a fatal "did not match any files" that would sink the
	// whole commit.
	var pathspec []string
	for _, p := range kept {
		if _, err := os.Stat(p); err != nil {
			if _, err := runGit(root, "ls-files", "--error-unmatch", "--", p); err != nil {
				continue
			}
		}
		pathspec = append(pathspec, p)
	}
	if len(pathspec) == 0 {
		return false, errors.New("no tracked paths to commit")
	}
	if out, err := runGit(root, append([]string{"add", "--"}, pathspec...)...); err != nil {
		return false, fmt.Errorf("git add: %s", gitCause(out, err))
	}
	// A unit of work can name a path it did not change: Touch declares a file
	// $EDITOR was handed and may have left alone, and an op that failed has
	// already recorded its path. Committing an empty pathspec is git's "nothing to
	// commit" — an exit status this would report as a failed record, and log, for
	// a gesture where nothing was lost. diff --cached exits 0 when nothing is
	// staged for these paths, which is the whole condition: anything else, up to
	// and including a git that could not answer, goes on to the commit and lets it
	// report its own failure.
	if _, err := runGit(root, append([]string{"diff", "--cached", "--quiet", "--"}, pathspec...)...); err == nil {
		return false, nil
	}
	if out, err := runGit(root, append([]string{"commit", "--only", "-m", message, "--"}, pathspec...)...); err != nil {
		// A failed commit leaves the work staged, and the next commit a
		// human makes in the store would absorb it — the mirror of the
		// absorption the pathspec scoping exists to prevent. Unstaging also
		// drops any pre-existing staging of these exact paths, which for a
		// just-written destination and a just-removed candidate is no real loss.
		runGit(root, append([]string{"reset", "-q", "--"}, pathspec...)...)
		return false, fmt.Errorf("git commit: %s", gitCause(out, err))
	}
	return true, nil
}

// pushKnowledge publishes the store's branch to its upstream. The store's
// commits are the only copy of a human's promote and reject decisions —
// candidates are recoverable by re-extraction, the decisions are not — so the
// window between a commit and its publication is the store's real exposure, and
// it is closed by the gesture that opened it rather than by a timer. No retry
// machinery: a push carries every commit before it, so the next gesture's push
// is this one's retry — for a transient failure. A non-fast-forward rejection is
// the exception, and the likeliest one for a store shared across machines: every
// later push is rejected identically until a human pulls or rebases, which is
// theirs to do rather than ours to do behind them. A store with nothing to push
// to degrades with a stated reason rather than an error, the way a store that is
// not a repo does.
func pushKnowledge(root string) error {
	// A detached HEAD and absent tracking configuration are both answered by git
	// exiting non-zero. Only an exit status means git ran and answered: a git
	// that could not be run, or one killed by gitTimeout, must not be reported as
	// the store's configuration, which would send the user to fix a healthy repo.
	branch, err := runGit(root, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			return fmt.Errorf("push: git symbolic-ref: %s", gitCause(branch, err))
		}
		return fmt.Errorf("%w: %s", errDetachedHead, gitCause(branch, err))
	}
	// branch.<name>.remote and branch.<name>.merge are what @{upstream} is
	// composed from, and reading them directly yields the two halves the push
	// needs named outright. Reading them beats splitting @{upstream}'s
	// "origin/main", which is ambiguous when a remote or a branch name holds a
	// slash. Either being absent is the store having nowhere to push; a remote
	// that is configured but does not exist is not, and reaches the push below to
	// fail there with git's own reason. git config also exits non-zero for a
	// malformed config file or an invalid key, so its output travels in the error
	// and the log names the real cause under the sentinel's sentence.
	remote, err := runGit(root, "config", "--get", "branch."+branch+".remote")
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			return fmt.Errorf("push: git config: %s", gitCause(remote, err))
		}
		return fmt.Errorf("%w: no branch.%s.remote: %s", errNoUpstream, branch, gitCause(remote, err))
	}
	merge, err := runGit(root, "config", "--get", "branch."+branch+".merge")
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			return fmt.Errorf("push: git config: %s", gitCause(merge, err))
		}
		return fmt.Errorf("%w: no branch.%s.merge: %s", errNoUpstream, branch, gitCause(merge, err))
	}
	// Remote and refspec named outright, because a plain `git push` resolves
	// through host configuration the store inherits — push.default,
	// remote.pushDefault, branch.<name>.pushRemote, remote.<name>.push — which
	// could send this commit to a ref nobody pointed the store at, or under
	// push.default=nothing fail every time, leaving a warn no later gesture can
	// heal. Naming both takes all four out of the resolution, the way runGit pins
	// commit.gpgsign and core.hooksPath; push.followTags is the one that survives
	// an explicit refspec, so it is pinned here on the same terms — a tag this
	// gesture never touched is not part of the record. What lands is exactly this
	// branch's commit on its upstream ref, and nothing else.
	if out, err := runGit(root, "-c", "push.followTags=false", "push", remote, "HEAD:"+merge); err != nil {
		return fmt.Errorf("git push: %s", gitCause(out, err))
	}
	return nil
}

// commitMu serializes one process's commits. ApplyDeferred lets a caller hold
// several units of work's commits at once — the TUI's gestures run theirs as a
// tea.Cmd, so two of them can be in flight together rather than ordered by the
// update loop — and two concurrent `git add`/`commit` in one repo contend on
// index.lock, which git answers by failing rather than waiting. The push is
// inside the lock too, for its own reason: git takes no index.lock for it, but
// two pushes of one branch race and the loser comes back non-fast-forward. It is
// also the dominant queueing cost — what a second gesture waits for is the
// first's commit and push, not its commit alone. In-process only: the LaunchAgent
// sweep is a separate process, and gitTimeout stays the backstop there. That
// queueing is paid off the update loop, so it costs latency and not
// responsiveness.
var commitMu sync.Mutex

// recordKnowledgeCommit commits one unit of work, publishes it, and returns the
// zero Warn on success or the short reason the record fell short. Neither
// failure is ever silent: it is appended to knowledgeGitLogPath() in full, and
// its head handed back for the status line, which is gone by the next
// keystroke. The push sits here rather than inside commitKnowledge so that a
// push that fails cannot reach that function's unstaging recovery: the commit
// landed, and the local record is correct and complete.
func recordKnowledgeCommit(root string, paths, droppable []string, message string) Warn {
	commitMu.Lock()
	defer commitMu.Unlock()
	message = SanitizeRecord(message)
	committed, err := commitKnowledge(root, paths, droppable, message)
	if err != nil {
		reason := logFailure(message, err)
		// Both no-repo shapes degrade the same way; the sentinel text is the whole
		// status-line reason, since the enclosing path detail belongs in the log.
		if errors.Is(err, errNoGitRepo) {
			return Warn{NotCommitted: errNoGitRepo.Error()}
		}
		if errors.Is(err, errEnclosingRepo) {
			return Warn{NotCommitted: errEnclosingRepo.Error()}
		}
		return Warn{NotCommitted: reason}
	}
	// A pathspec that held no change left no commit, and a store with nothing to
	// publish owes the network nothing.
	if !committed {
		return Warn{}
	}
	if err := pushKnowledge(root); err != nil {
		reason := logFailure(message, err)
		if errors.Is(err, errDetachedHead) {
			return Warn{NotPushed: errDetachedHead.Error()}
		}
		if errors.Is(err, errNoUpstream) {
			return Warn{NotPushed: errNoUpstream.Error()}
		}
		return Warn{NotPushed: reason}
	}
	return Warn{}
}

// logFailure writes one failure to knowledgeGitLogPath() in full and returns its
// head for the status line, which is gone by the next keystroke. Shared by the
// commit and by the write errors Apply reports, so both failures reach the same
// two places. The message is sanitized here — idempotent on text a caller
// already sanitized — so the bound a record shares with the commit subject is
// applied once, at the boundary both failures pass through, rather than in each
// caller; the flattening half of the invariant sits lower still, in
// appendKnowledgeGitLog.
func logFailure(message string, err error) string {
	message = SanitizeRecord(message)
	// Flattened but not bounded: the log is the debugging mechanism for this
	// store, so it keeps every line the failure produced as one record.
	appendKnowledgeGitLog(message + ": " + flattenRecord(err.Error()))
	return shortGit(err.Error())
}

// SanitizeRecord flattens a message into a single bounded record — the commit
// subject, which is also the body of the knowledge-git log line, and the shape
// the store's own log.md entries are held to. The bound counts runes, not bytes:
// a non-ASCII id near the limit would otherwise be cut mid-rune, writing invalid
// UTF-8 into the very record flattenRecord exists to keep readable.
func SanitizeRecord(message string) string {
	flat := []rune(flattenRecord(message))
	if len(flat) <= MessageMax {
		return string(flat)
	}
	return string(flat[:MessageMax-1]) + "…"
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
// The line is flattened here rather than in each caller, so no caller can write
// a record that forges a second one; flattenRecord is idempotent, so the callers
// that flatten their own variable part first are unaffected. Defence in depth
// rather than a live hole: no extractor-written text reaches this unflattened
// today, since CANDIDATE_ID_RE gates the candidate filename.
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
	fmt.Fprintf(f, "%s %s\n", time.Now().Format(time.RFC3339), flattenRecord(line))
}

// sameDir reports whether two paths name the same directory. Both sides are
// resolved first: git reports an absolute physical path while the root arrives
// as the caller resolved it, which may be relative, and on macOS
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

// shortGit collapses a failure to its first line, flattened and bounded, so it
// fits the one-line status bar. Flattening is not cosmetic here: git relays a
// remote's side-band and a hook's output verbatim, so a push or commit failure
// carries text the store never trusted, and an ANSI or OSC sequence pasted into
// the status line would spoof the display or drive the terminal. The cut comes
// first, since flattenRecord maps a newline onto a space and would otherwise
// hide where git's first line ended. It is the display boundary only — callers
// log the whole text before shortening it, and appendKnowledgeGitLog flattens
// that copy on the same terms. The bound counts runes, not bytes, for the reason
// SanitizeRecord does: a push relays a remote's own text, which is likelier to
// be non-ASCII than a local git error, and a byte cut near the limit would write
// a broken rune into the very line the flattening exists to keep readable.
func shortGit(out string) string {
	first, _, _ := strings.Cut(out, "\n")
	return truncate(flattenRecord(first), gitReasonMax)
}

// shortenPath and truncate are the TUI's display helpers, duplicated here rather
// than imported: the store is written by the CLI and the extractor too, and
// importing internal/tui for two string functions would drag the whole
// fullscreen model in behind them.
func shortenPath(p string) string {
	home, err := os.UserHomeDir()
	if err == nil && home != "" && strings.HasPrefix(p, home) {
		return "~" + p[len(home):]
	}
	return p
}

// truncate bounds a display string to n runes rather than n bytes, the way
// SanitizeRecord does: its one caller carries git's output, which for a push is
// a remote's own text, and a byte cut lands mid-rune on anything non-ASCII.
func truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return string(r[:n])
	}
	return string(r[:n-1]) + "…"
}
