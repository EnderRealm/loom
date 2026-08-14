package extract

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"unicode/utf8"

	"loom/internal/summaries"
)

// scopePattern bounds a derived scope to one conservative path segment.
// git_remote is client-supplied — the shipper sends it, the receiver stores it
// verbatim, the summarizer copies it into sessions.git_remote — and the scope
// derived from it becomes both a --scope argument and a path under the
// knowledge store. Requiring a leading alphanumeric is what rejects "." and
// "..", which filepath.Join would otherwise clean into the store's parent.
var scopePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// errNoRemote is the commonest unresolvable case — a session captured outside
// a git checkout — and a sentinel so the backfill's report can count it apart
// from a scope the store simply doesn't have.
var errNoRemote = errors.New("no git remote")

// markerName is the repo-root file a project declares its canonical name in.
// This path walks for it as extractors/resolve_project.py does — nearest usable
// marker from the cwd up to the repo root wins, an unusable one continues the
// walk rather than ending it — so both entry points file the same repo's
// candidates under the same scope. Two documented divergences: the case fold
// markerScope describes, and the truths/<scope>/ check, which counts a marker
// naming a scope this store doesn't have as unusable — resolve_project.py has
// no store to make that check against.
const markerName = ".loom-project"

// Where a resolved scope came from, so the extract log accounts for the
// derivation rather than leaving a disagreement between the two invisible.
const (
	sourceMarker = "marker"
	sourceRemote = "git-remote"
)

// resolution is a resolved scope and the derivation that produced it.
type resolution struct {
	scope  string
	source string
}

// resolveScope derives a session's knowledge scope, preferring the
// .loom-project marker of the checkout it ran in and falling back to the
// basename of its normalized git remote (github.com/enderrealm/loom → loom).
// A session with neither, one whose basename isn't a safe scope name, or one
// whose scope has no directory in the knowledge store, has nowhere correct to
// file candidates, so it is skipped rather than extracted into a default (or
// traversed) scope.
//
// The marker is an additional source of truth, never a new way to fail: a cwd
// that names nothing on this host, and a marker chain with nothing usable in
// it, both fall through to the remote, so every session that resolves today
// still resolves.
func resolveScope(cwdRaw, gitRemote string, seen logOnce) (resolution, error) {
	remote := scopeFromRemote(gitRemote)
	if scope, marker := markerScope(cwdRaw, seen); scope != "" {
		if remote != "" && remote != scope {
			// The three-competing-lists case the marker exists to close: the
			// same repo filing candidates under two names depending on which
			// derivation ran. The marker wins, but doing so silently is wrong —
			// one of the two is stale, and only an operator can say which.
			seen.printf("%s names scope=%s, git remote %s names scope=%s — using the marker",
				logSafe(marker), scope, echoRemote(gitRemote), echoRemote(remote))
		}
		return resolution{scope: scope, source: sourceMarker}, nil
	}
	if remote == "" {
		return resolution{}, errNoRemote
	}
	if err := validScope(remote); err != nil {
		return resolution{}, fmt.Errorf("%w (from git remote %q)", err, gitRemote)
	}
	return resolution{scope: remote, source: sourceRemote}, nil
}

// scopeFromRemote is the git-remote derivation on its own: the basename of the
// normalized remote, or "" when the session records no remote.
func scopeFromRemote(gitRemote string) string {
	if strings.TrimSpace(gitRemote) == "" {
		return ""
	}
	scope := summaries.NormalizeRemote(gitRemote)
	if i := strings.LastIndex(scope, "/"); i >= 0 {
		scope = scope[i+1:]
	}
	return scope
}

// validScope is the last boundary before a derived name becomes a --scope
// argument and a path under the knowledge store. A marker's value is
// repo-controlled text reached through a client-supplied cwd, so it clears the
// same gate a remote-derived name does rather than a shorter one.
func validScope(scope string) error {
	if !scopePattern.MatchString(scope) {
		return fmt.Errorf("unsafe scope %s", echoScope(scope))
	}
	// truths/<scope>/ is what extract.py loads as few-shot references; its
	// absence means the store has no such scope.
	truths := filepath.Join(knowledgeRoot(), "truths")
	dir := filepath.Join(truths, scope)
	// Belt and braces on the pattern above: the scope must still name a direct
	// child of truths/ after Join has cleaned the path.
	if filepath.Dir(dir) != truths {
		return fmt.Errorf("scope %s escapes %s", echoScope(scope), truths)
	}
	// The store root rather than the joined path: dir repeats the name, and this
	// message is what a rejected marker echoes into the log.
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return fmt.Errorf("unknown scope %s (no such directory under %s)", echoScope(scope), truths)
	}
	return nil
}

// markerScope reads the .loom-project marker for a session's recorded cwd,
// returning the scope it names and the marker that named it — both "" when this
// host has nothing usable there. The cwd is a real absolute path
// (sessions.cwd_raw, from the receiver's per-session sidecar), but it was
// recorded on whichever host ran the session, so a path that has moved or never
// existed here is an ordinary input rather than an error.
//
// Lowercased, unlike resolve_project.py's exact-case rule: everything on this
// side is lowercase by construction — summaries.NormalizeRemote lowercases and
// scopePattern requires a leading [a-z0-9] — and wantedScopes lowercases
// --scope for the same reason, so an exact-case marker derivation would make
// `--backfill --scope Loom` stop matching sessions it matches today.
func markerScope(cwdRaw string, seen logOnce) (string, string) {
	start := strings.TrimSpace(cwdRaw)
	// Absolute only: a relative path would resolve against the daemon's working
	// directory, which has nothing to do with the session's checkout.
	if !filepath.IsAbs(start) {
		return "", ""
	}
	// Resolved as resolve_project.py resolves its input: the walk below is
	// lexical, so a symlinked component would otherwise make it climb a chain of
	// directories that are not the checkout's real ancestors. Fails when the
	// path doesn't exist on this host, which is the ordinary case for a session
	// captured elsewhere.
	start, err := filepath.EvalSymlinks(start)
	if err != nil {
		return "", ""
	}
	if fi, err := os.Stat(start); err != nil || !fi.IsDir() {
		return "", ""
	}
	root := repoRoot(start)
	if root == "" {
		return "", ""
	}
	// The nearest usable marker from the cwd up to the repo root inclusive wins,
	// and the walk stops there: a stray marker in $HOME must never capture a
	// repo. Validity is judged inside the walk rather than by the caller so an
	// unusable marker continues the chain instead of discarding it — a repo
	// whose root declares its name resolves to it despite a bad marker in a
	// subdirectory, as it does under resolve_project.py.
	for dir := start; ; dir = filepath.Dir(dir) {
		path := filepath.Join(dir, markerName)
		v, err := markerValue(path)
		if err != nil {
			// "No marker here" is silent — it is most of this walk, every walk —
			// but a marker that exists and can't be used is the answer to why a
			// repo that has one still resolves through its remote.
			seen.printf("%s %s — ignoring it", logSafe(path), logSafe(err.Error()))
		}
		if v != "" {
			scope := strings.ToLower(v)
			if err := validScope(scope); err != nil {
				seen.printf("%s unusable (%v) — ignoring it", logSafe(path), err)
			} else {
				if dir != root {
					// Nearest-wins is what makes a session captured in a
					// subdirectory work, but the marker is a repo-root
					// convention: one below the root is as likely a vendored
					// subtree's own declaration as the project's.
					seen.printf("%s is below the repo root %s — using it, but the marker belongs at %s",
						logSafe(path), logSafe(root), logSafe(filepath.Join(root, markerName)))
				}
				return scope, path
			}
		}
		if dir == root {
			return "", ""
		}
	}
}

// repoRoot returns the first ancestor of dir (inclusive) holding a .git entry,
// or "" when there is none. Worktrees and submodules carry a .git file rather
// than a directory, so both count.
func repoRoot(dir string) string {
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// maxMarkerBytes caps the marker read. A marker is one line of text declaring a
// project name, but the file is repo-controlled and reached through a
// client-supplied cwd, so a session can point this read at any file of that name
// on the host: it is read through a bound rather than whole, and anything past
// the bound is not a marker, so it fails closed to the remote derivation.
const maxMarkerBytes = 4096

// markerValue is a marker file's first non-empty, non-comment line. An empty
// value with a nil error is "no marker here" — most of every walk, and correctly
// silent; every other refusal carries the reason, so a marker that exists but
// can't be used says so in the log rather than looking like no marker at all.
func markerValue(path string) (string, error) {
	// Regular files only: reading follows links, and cwd_raw is client-supplied,
	// so a symlinked marker would be a read-anything channel into a value this
	// process then acts on and logs. resolve_project.py skips one for the same
	// reason; the argument is stronger here. O_NOFOLLOW covers the final
	// component only — the ancestors are whatever EvalSymlinks resolved the cwd
	// to, and are trusted from there — and what it does guarantee is that the
	// regular-file check and the read see one inode, since both go through this
	// descriptor rather than re-resolving the path. O_NONBLOCK so a fifo left at
	// the path fails the regular-file check instead of hanging the open.
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		if errors.Is(err, syscall.ELOOP) {
			// What O_NOFOLLOW reports for a symlink, dangling or not.
			return "", errors.New("is a symlink")
		}
		return "", fmt.Errorf("unreadable (%v)", err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("unreadable (%v)", err)
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("is not a regular file (%s)", fi.Mode().Type())
	}
	data, err := io.ReadAll(io.LimitReader(f, maxMarkerBytes+1))
	if err != nil {
		return "", fmt.Errorf("unreadable (%v)", err)
	}
	if len(data) > maxMarkerBytes {
		return "", fmt.Errorf("is larger than %d bytes", maxMarkerBytes)
	}
	// The marker's byte format is a documented contract, not a property of this
	// host's locale, and it arrives from a repo this host didn't author.
	if !utf8.Valid(data) {
		return "", errors.New("is not UTF-8")
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			return line, nil
		}
	}
	return "", errors.New("holds no project name")
}
