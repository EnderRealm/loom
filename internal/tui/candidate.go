package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"loom/internal/knowledge/store"
)

// frontmatterKey matches a top-level "key: value" line inside a `---` block.
// Indented lines (sub-fields under sources:, evidence:) are ignored.
var frontmatterKey = regexp.MustCompile(`^([a-z_]+):\s*(.*)$`)

// pluralType maps a singular artifact type to the directory segment used in the
// knowledge store (truth → truths, decision → decisions).
func pluralType(t string) string {
	switch t {
	case "truth":
		return "truths"
	case "decision":
		return "decisions"
	}
	return ""
}

// candidateKebab derives the validated filename stem from a candidate id by
// stripping the leading "<scope>-" namespace. The validated tree keeps the full
// id inside frontmatter but drops the scope prefix from the filename.
func candidateKebab(id, scope string) string {
	if scope != "" && strings.HasPrefix(id, scope+"-") {
		return strings.TrimPrefix(id, scope+"-")
	}
	return id
}

// promoteCandidate moves a candidate from _candidates/<type>s/<scope>/ to its
// validated home <type>s/<scope>/, cleaning candidate-only frontmatter (drops
// extracted_at/extracted_by, flips status to validated, bumps verified_at), and
// commits the written destination and the removed source — those paths only, so
// unrelated working-tree dirt stays out of the record. Returns the destination
// path and, when the commit did not happen or was not published, the reason. It
// never overwrites an existing validated file.
func promoteCandidate(a Artifact) (string, store.Warn, error) {
	plural := pluralType(a.Type)
	if plural == "" {
		return "", store.Warn{}, fmt.Errorf("unknown type %q", a.Type)
	}
	if a.Status != "candidate" {
		return "", store.Warn{}, fmt.Errorf("not a candidate")
	}
	// Resolved once and handed to the store: KnowledgeRoot() twice — once to
	// build the paths, once inside Apply — is two answers waiting to differ.
	root := KnowledgeRoot()
	dest := filepath.Join(root, plural, a.Scope, candidateKebab(a.ID, a.Scope)+".md")
	if _, err := os.Stat(dest); err == nil {
		return "", store.Warn{}, fmt.Errorf("%s already exists", shortenPath(dest))
	}
	// The files move inside the closure, so a failed commit is reported rather
	// than rolled back: undoing the move to keep history tidy would throw away the
	// human's review decision, which is the expensive part of the gesture.
	// Nothing here is droppable: the promoted file is the record, so a store
	// whose ignore rules cover the validated tree fails the commit loudly
	// rather than recording the candidate's removal alone.
	written := false
	warn, err := store.ApplyIn(root, gestureMessage("promote", a), func(tx *store.Tx) error {
		if err := tx.WriteFile(dest, []byte(promoteFrontmatter(a.Body))); err != nil {
			return err
		}
		written = true
		// Write succeeded; drop the candidate. A leftover source would resurface
		// as a duplicate candidate on the next refresh.
		return tx.Remove(a.Path)
	})
	if err != nil {
		// Once the destination exists the promote has landed, and the store has
		// already committed it: reporting "promote failed" would leave the
		// candidate listed and the next attempt refused for a destination that is
		// now there. Only a failure before the write leaves nothing behind.
		if !written {
			return "", store.Warn{}, err
		}
		// The store still committed and pushed what the write left behind, so its
		// own outcome is kept and only the record's reason is overridden: a commit
		// that landed unpublished has to reach the status line too.
		w := warn
		w.NotCommitted = store.ShortReason(err)
		return dest, w, nil
	}
	return dest, warn, nil
}

// rejectCandidate moves a candidate into the _rejected/ archive, preserving its
// contents and timestamped filename for extractor tuning, and commits the
// archive together with the log.md entry that records the decision — the
// archive is the store's only corpus of what the extractor got wrong, so it
// belongs in history rather than in untracked working-tree state. The record is
// still the log.md entry alone and stays independent of the archive, which is
// passed as droppable: a store that gitignores the archive gets its record
// anyway. Returns the destination path and, when the record did not land or was
// not published, the reason.
func rejectCandidate(a Artifact) (string, store.Warn, error) {
	plural := pluralType(a.Type)
	if plural == "" {
		return "", store.Warn{}, fmt.Errorf("unknown type %q", a.Type)
	}
	if a.Status != "candidate" {
		return "", store.Warn{}, fmt.Errorf("not a candidate")
	}
	root := KnowledgeRoot()
	dest := filepath.Join(root, "_candidates", "_rejected", plural, a.Scope, filepath.Base(a.Path))
	if _, err := os.Stat(dest); err == nil {
		return "", store.Warn{}, fmt.Errorf("%s already exists", shortenPath(dest))
	}
	// The source stays in the pathspec — its removal is a real change to the
	// candidates tree — and the store drops it when it was never tracked. The
	// archive is declared droppable: an ignored one leaves the pathspec instead
	// of sinking the record, which is what keeps the decision independent of the
	// archive's storage policy.
	// The pathspec is file-granular, so a log.md entry nobody has committed is
	// absorbed into this commit. Every writer of that file commits its own entry
	// now, so what is left pending is an entry whose commit failed — the
	// extractor's, appendRetrospectLog's — which this then carries. Accepted: the
	// decision record and the extractor share one append-only file, and keeping
	// the decision in log.md rather than in the archived file is what makes it
	// durable.
	logPath := filepath.Join(root, "log.md")
	archived := false
	warn, err := store.ApplyIn(root, gestureMessage("reject", a), func(tx *store.Tx) error {
		if err := tx.Rename(a.Path, dest); err != nil {
			return err
		}
		archived = true
		tx.Droppable(dest)
		return tx.Append(logPath, rejectLogEntry(a))
	})
	if err != nil {
		// As with promote: once the file has moved, a failed record surfaces as
		// a warning instead of undoing the archive. Only a failure before the
		// move — the store has no log.md to append the decision to is the one
		// after it — leaves the gesture itself unlanded.
		if !archived {
			return "", store.Warn{}, err
		}
		// As in promote: the store's own outcome is kept, since the commit the
		// archive produced may have landed unpublished.
		w := warn
		w.NotCommitted = store.ShortReason(err)
		return dest, w, nil
	}
	return dest, warn, nil
}

// rejectLogFormat is the store's log.md convention for a reject decision.
const rejectLogFormat = "## [%s] reject %s | %s | %s candidate %s archived"

// logEntryFixedRunes is the entry's scaffolding — the date plus the literal
// text around the four fields — measured from the format itself, so editing the
// format cannot silently invalidate the bounds below.
var logEntryFixedRunes = len([]rune(fmt.Sprintf(rejectLogFormat, "2006-01-02", "", "", "", "")))

// Per-field rune bounds for a reject log entry. Bounding each field before
// composition is what keeps truncation per-field: a bound on the composed line
// alone would cut the tail, and the tail is the scope, the type and the
// basename. The basename's bound is derived rather than chosen, so the
// worst-case entry — every field at its bound — still fits store.MessageMax
// however the other three or the format change.
const (
	logFieldIDMax    = 50
	logFieldScopeMax = 20
	logFieldTypeMax  = 12
)

var logFieldBaseMax = store.MessageMax - logEntryFixedRunes - logFieldIDMax - logFieldScopeMax - logFieldTypeMax

// logFieldDisallowed matches every rune outside the store's own name grammar
// (SCHEMA.md § "Scope = project"; ticketIDPattern in
// internal/extract/retrospect.go), which every legitimate id, scope, type and
// candidate filename already satisfies.
var logFieldDisallowed = regexp.MustCompile(`[^A-Za-z0-9._-]`)

// logFieldRunes allow-lists one interpolated field of a log.md entry. An
// allow-list because log.md is rendered markdown — the store is also an Obsidian
// vault — and a denylist of markdown and HTML constructs never closes: one
// unterminated HTML comment from an extractor-written id hides every entry below
// it, and an injected " | " misstates the entry's own field boundaries.
// Disallowed runes become "?" rather than vanishing, so a rewritten field still
// reads as an honest record of an odd value instead of a well-formed one.
func logFieldRunes(s string) []rune {
	return []rune(logFieldDisallowed.ReplaceAllString(s, "?"))
}

// logField bounds a field by runes, cutting the tail — the shape that suits a
// value whose distinguishing part comes first.
func logField(s string, max int) string {
	field := logFieldRunes(s)
	if len(field) <= max {
		return string(field)
	}
	return string(field[:max-1]) + "…"
}

// logFieldTail bounds a field by runes, cutting the head. Used for the candidate
// basename, which is "<id>--YYYYMMDD-HHMMSS.md": the id already has its own
// field, so the timestamp suffix is the basename's whole marginal value, and a
// tail cut would drop exactly the discriminator the basename is in the entry for.
func logFieldTail(s string, max int) string {
	field := logFieldRunes(s)
	if len(field) <= max {
		return string(field)
	}
	return "…" + string(field[len(field)-(max-1):])
}

// rejectLogEntry renders the reject decision in the store's log.md convention:
// one "## [YYYY-MM-DD] <verb> <subject> | <scope> | <summary>" entry per event.
// The basename is load-bearing — re-runs of the extractor emit siblings sharing
// one id, so id and scope alone don't say which candidate was rejected. Fields
// are sanitized and bounded before composition; store.SanitizeRecord then
// backstops the composed line, as it does the commit subject.
func rejectLogEntry(a Artifact) string {
	entry := fmt.Sprintf(rejectLogFormat,
		time.Now().Format("2006-01-02"),
		logField(a.ID, logFieldIDMax),
		logField(a.Scope, logFieldScopeMax),
		logField(a.Type, logFieldTypeMax),
		logFieldTail(filepath.Base(a.Path), logFieldBaseMax))
	return "\n" + store.SanitizeRecord(entry) + "\n"
}

// gestureMessage is the one-line commit subject for a gesture — promote, reject
// or edit — naming the artifact's type, scope and id so the history reads as the
// review decisions it records.
func gestureMessage(gesture string, a Artifact) string {
	return gesture + " " + a.Type + " " + a.Scope + "/" + a.ID
}

// promoteFrontmatter rewrites a candidate's frontmatter for the validated tree:
// status→validated (deduped — the extractor appends a second status line),
// verified_at bumped to today, extracted_at/extracted_by dropped. The body and
// indented sub-fields (evidence/sources children) pass through untouched.
func promoteFrontmatter(body string) string {
	lines := strings.Split(body, "\n")
	if len(lines) < 2 || strings.TrimSpace(lines[0]) != "---" {
		return body
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return body
	}

	today := time.Now().Format("2006-01-02")
	var fm []string
	statusDone, verifiedDone := false, false
	for _, line := range lines[1:end] {
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") || strings.HasPrefix(line, "-") {
			fm = append(fm, line)
			continue
		}
		m := frontmatterKey.FindStringSubmatch(line)
		if m == nil {
			fm = append(fm, line)
			continue
		}
		switch m[1] {
		case "extracted_at", "extracted_by":
			// drop candidate-only provenance
		case "status":
			if !statusDone {
				fm = append(fm, "status: validated")
				statusDone = true
			}
		case "verified_at":
			fm = append(fm, "verified_at: "+today)
			verifiedDone = true
		default:
			fm = append(fm, line)
		}
	}
	if !statusDone {
		fm = append(fm, "status: validated")
	}
	if !verifiedDone {
		fm = append(fm, "verified_at: "+today)
	}

	out := make([]string, 0, len(lines)+2)
	out = append(out, "---")
	out = append(out, fm...)
	out = append(out, "---")
	out = append(out, lines[end+1:]...)
	return strings.Join(out, "\n")
}
