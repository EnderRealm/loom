// Package store is the single entry point for writing the durable knowledge
// store at ~/.loom/knowledge. Every writer — the TUI's promote, reject and edit
// gestures, the retrospect run's log.md entry, and, through `loom knowledge
// write`, the Python extractor — performs its writes inside an Apply closure,
// and Apply commits what the closure touched. Committing is a property of this
// package rather than of each caller, so a new writer carries no commit code and
// cannot forget one; the store's rules (writes confined to the store,
// path-scoped commits, an untouched dirty tree, the non-repo and enclosing-repo
// sentinels, record sanitization) live here and nowhere else.
package store

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"loom/internal/knowledge"
)

// Tx records every path one unit of work touches and performs its writes. The
// recorded paths are the commit's pathspec, so a write that is not made through
// a Tx — or a mutation made outside it and never declared with Touch — is not in
// the record.
//
// Every filesystem op goes through an open handle on the store directory, so a
// path that leaves the store is refused by the syscall rather than by a check on
// the pathname: a directory component swapped for a symlink out of the store
// between a check and the write it guards would defeat any check made on the
// name alone, and this package is the one place a caller's path is trusted.
type Tx struct {
	// root is the store the writes are confined to, as the caller named it: it
	// names the store in a refusal, and the commit runs against it. rootAbs is
	// the same root made absolute, the form a caller's path is made relative to.
	root    string
	rootAbs string
	// dir is the open store directory every op is performed through.
	dir       *os.Root
	paths     []string
	droppable []string
}

// record notes a store-relative path as part of the unit of work. Recorded
// absolute because git runs with -C root: a relative path — which a relative
// LOOM_KNOWLEDGE_ROOT produces — would be re-resolved against the store rather
// than the cwd the caller built it against, match nothing, and sink the commit.
func (t *Tx) record(rel string) {
	t.paths = append(t.paths, filepath.Join(t.rootAbs, rel))
}

// abs makes a path absolute, keeping one it cannot resolve as given.
func abs(path string) string {
	if a, err := filepath.Abs(path); err == nil {
		return a
	}
	return path
}

// relative turns a caller's path into the store-relative form the ops use, and
// carries everything the store decides about a path by its name: that it lands
// inside the store, and that it is not the store's own repository. The callers
// this has to hold against include `loom knowledge write`, whose plan is a
// string a non-Go writer composed, so an absolute path elsewhere, a traversal
// out of the store, or a root that resolved against an unexpected cwd would
// otherwise scatter files across the user's filesystem and report nothing worse
// than a commit warning. Containment itself is the open root's — this refusal is
// the readable half, named for the caller before any op is attempted.
func (t *Tx) relative(path string) (string, error) {
	rel, err := filepath.Rel(t.rootAbs, abs(path))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%s is outside the knowledge store %s", shortenPath(path), shortenPath(t.root))
	}
	if isGitPath(rel) {
		return "", fmt.Errorf("%s is inside the store's own git repository", shortenPath(path))
	}
	if err := t.noSymlinks(rel); err != nil {
		return "", err
	}
	return rel, nil
}

// noSymlinks refuses a path any of whose existing components is a symlink,
// intermediate or final. The store keeps no symlinks, and this is the check that
// makes the .git rule hold: a name has no .git component when an in-store
// symlink supplies it — `alias -> .git`, or a file that is itself a link to
// .git/config — and the open root permits those, since their targets stay inside
// the root. Refusing every symlink rather than the ones aimed at .git closes it
// by construction instead of by enumerating targets, and costs nothing real:
// git records a symlink as a symlink, so writing through one writes outside the
// tree git tracks, and a path-scoped commit of it would record something git
// never had. The refusal names the link, which is what has to go.
func (t *Tx) noSymlinks(rel string) error {
	if rel == "." {
		return nil
	}
	parts := strings.Split(rel, string(filepath.Separator))
	for i := range parts {
		partial := filepath.Join(parts[:i+1]...)
		info, err := t.dir.Lstat(partial)
		if err != nil {
			// A component that is not there is not a link, and nothing below it
			// exists either; the op creates what it needs or fails on its own.
			return nil
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symlink, which the knowledge store does not write through",
				shortenPath(filepath.Join(t.root, partial)))
		}
	}
	return nil
}

// isGitPath reports whether any component of a store-relative path names .git.
// The repository is the store's history, not its content: a plan that rewrote
// .git/config or .git/HEAD would corrupt it, and one that dropped a hook would
// leave code to run under the next git command a human types in the store —
// each landing before the commit that follows it fails. Both forms are covered
// by matching components: a worktree's .git is a file rather than a directory.
// Matched case-insensitively, because the store usually sits on a
// case-insensitive APFS volume, where .GIT names the same directory.
func isGitPath(rel string) bool {
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if strings.EqualFold(part, ".git") {
			return true
		}
	}
	return false
}

// ensureParent creates the parent directories of a store-relative path, unless
// they already resolve. A Stat that fails for any reason other than the
// directory's absence is left alone for the op itself to report: a mkdir over a
// component that is a symlink out of the store answers "file exists", where the
// op answers that the path escapes — the refusal the caller needs to read.
func (t *Tx) ensureParent(rel string) error {
	dir := filepath.Dir(rel)
	if _, err := t.dir.Stat(dir); err == nil || !errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return t.dir.MkdirAll(dir, 0o755)
}

// WriteFile writes one file, creating its parent directories.
func (t *Tx) WriteFile(path string, body []byte) error {
	rel, err := t.relative(path)
	if err != nil {
		return err
	}
	if err := t.ensureParent(rel); err != nil {
		return err
	}
	// Opened rather than handed to the root's own WriteFile so the record can be
	// taken between the two failures: an open that failed changed nothing, while a
	// write that failed part-way has still changed the file, and only the second
	// belongs in the record.
	f, err := t.dir.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	t.record(rel)
	if _, err := f.Write(body); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// Remove deletes one file.
func (t *Tx) Remove(path string) error {
	rel, err := t.relative(path)
	if err != nil {
		return err
	}
	// Recorded only once the file is gone: a removal that failed left the store
	// as it was, and a path in the pathspec that nothing changed would put an
	// unrelated working-tree edit into the record.
	if err := t.dir.Remove(rel); err != nil {
		return err
	}
	t.record(rel)
	return nil
}

// Rename moves one file, creating the destination's parent directories. Both
// sides are checked and both are recorded: the source's removal is as much a
// change to the store as the destination's arrival.
func (t *Tx) Rename(from, to string) error {
	fromRel, err := t.relative(from)
	if err != nil {
		return err
	}
	toRel, err := t.relative(to)
	if err != nil {
		return err
	}
	if err := t.ensureParent(toRel); err != nil {
		return err
	}
	// Recorded only once the move has happened, for the reason Remove is: a
	// rename that failed changed neither side.
	if err := t.dir.Rename(fromRel, toRel); err != nil {
		return err
	}
	t.record(fromRel)
	t.record(toRel)
	return nil
}

// Append appends text to an existing file. Opened without O_CREATE — log.md is
// bootstrapped at store init, and a writer pointed at a wrong root must not
// scatter one — so the perm argument is 0: it applies to nothing, and a mode
// here would be the wrong one the day O_CREATE were added. The error names the
// file rather than the whole path, since it reaches a one-line status bar.
func (t *Tx) Append(path, text string) error {
	rel, err := t.relative(path)
	if err != nil {
		return err
	}
	f, err := t.dir.OpenFile(rel, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		// Nothing was opened, so nothing changed and the path is not recorded.
		return fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	defer f.Close()
	t.record(rel)
	if _, err := f.WriteString(text); err != nil {
		return fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	return nil
}

// Touch declares a path that was mutated outside the Tx — by $EDITOR, or by any
// other process the caller handed the file to — so that it is committed with the
// rest of the unit of work. It performs no write of its own, but it is still the
// store that decides which paths a unit of work may name, so a path outside the
// store or inside its repository is refused here as it would be for a write.
func (t *Tx) Touch(path string) error {
	rel, err := t.relative(path)
	if err != nil {
		return err
	}
	// The only op that records without writing, so the only one a directory can
	// reach: the others fail at the syscall on one. A directory in the pathspec
	// stages everything dirty beneath it, which is the absorption the path
	// scoping exists to prevent — and the store root itself is a directory, so
	// this covers a declared path that resolved to the whole store.
	if info, err := t.dir.Stat(rel); err == nil && info.IsDir() {
		return fmt.Errorf("%s is a directory, not a file the store can record", shortenPath(path))
	}
	t.record(rel)
	return nil
}

// Droppable marks a recorded path whose record lives elsewhere: an ignored one
// leaves the pathspec instead of sinking the commit. Every other path is the
// record and fails loudly when git will not take it.
func (t *Tx) Droppable(path string) {
	t.droppable = append(t.droppable, abs(path))
}

// pathspec splits the recorded paths into the record and the paths whose record
// lives elsewhere, deduplicated in the order they were touched. Splitting at the
// end rather than in Droppable keeps the two orders equivalent — a path can be
// declared droppable before or after the op that touches it — and a path
// declared droppable but never touched is not part of the unit of work at all.
func (t *Tx) pathspec() (paths, droppable []string) {
	drop := make(map[string]bool, len(t.droppable))
	for _, p := range t.droppable {
		drop[p] = true
	}
	seen := make(map[string]bool, len(t.paths))
	for _, p := range t.paths {
		if seen[p] {
			continue
		}
		seen[p] = true
		if drop[p] {
			droppable = append(droppable, p)
			continue
		}
		paths = append(paths, p)
	}
	return paths, droppable
}

// Apply performs one unit of work against the knowledge store at
// knowledge.Root() and commits every path it touched as one record, subject
// message. Committing is Apply's, not the caller's.
//
// The commit happens even when fn returned an error: the writes that landed
// before it are on disk either way, and a store that carries them without the
// record is the state this package exists to prevent. Nothing recorded means
// nothing to commit, and so does a recorded path that turns out to hold no
// change: declaring a path is not the same as altering it.
//
// warn is "" when the commit landed and the short reason otherwise — the whole
// failure is already in ~/.loom/knowledge-git.log. err is fn's error verbatim,
// likewise logged in full first, so a caller can decide what its own failure
// means without also owning the record of it; or the store's own, when the root
// could not be opened and fn therefore never ran.
func Apply(message string, fn func(*Tx) error) (string, error) {
	return ApplyIn(knowledge.Root(), message, fn)
}

// ApplyIn is Apply against a named store root, for the caller that does not
// resolve the store the way knowledge.Root() does: internal/extract takes it
// from the extractor's persisted tunables, which may name a store the process
// environment does not. The root travels with the unit of work rather than being
// re-resolved per rule, so the containment check, the writes and the commit are
// all held against the same store.
func ApplyIn(root, message string, fn func(*Tx) error) (string, error) {
	message = SanitizeRecord(message)
	dir, err := os.OpenRoot(root)
	if err != nil {
		// A store that cannot be opened — absent, or not a directory — is a
		// misconfiguration to report, not a tree to create: a store is a git repo
		// with a SCHEMA.md and a log.md that no writer here produces, and one that
		// materialized a wrong root would scatter a half-store across it, the same
		// reason Append never creates the file it appends to.
		err = fmt.Errorf("knowledge store %s: %w", shortenPath(root), err)
		logFailure(message, err)
		return "", err
	}
	defer dir.Close()
	tx := &Tx{root: root, rootAbs: abs(root), dir: dir}
	err = fn(tx)
	if err != nil {
		logFailure(message, err)
	}
	paths, droppable := tx.pathspec()
	if len(paths)+len(droppable) == 0 {
		return "", err
	}
	return recordKnowledgeCommit(root, paths, droppable, message), err
}

// ShortReason collapses an error Apply handed back to the head of its first
// line, bounded, for a caller with one line to report it on. Apply has already
// written the whole text to ~/.loom/knowledge-git.log.
func ShortReason(err error) string {
	return shortGit(err.Error())
}
