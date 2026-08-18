package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
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
// path and, when the commit did not happen, the reason. It never overwrites an
// existing validated file.
func promoteCandidate(a Artifact) (string, string, error) {
	plural := pluralType(a.Type)
	if plural == "" {
		return "", "", fmt.Errorf("unknown type %q", a.Type)
	}
	if a.Status != "candidate" {
		return "", "", fmt.Errorf("not a candidate")
	}
	destDir := filepath.Join(KnowledgeRoot(), plural, a.Scope)
	dest := filepath.Join(destDir, candidateKebab(a.ID, a.Scope)+".md")
	if _, err := os.Stat(dest); err == nil {
		return "", "", fmt.Errorf("%s already exists", shortenPath(dest))
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", "", err
	}
	if err := os.WriteFile(dest, []byte(promoteFrontmatter(a.Body)), 0o644); err != nil {
		return "", "", err
	}
	// Write succeeded; drop the candidate. A leftover source would resurface
	// as a duplicate candidate on the next refresh.
	if err := os.Remove(a.Path); err != nil {
		return "", "", err
	}
	// The files have already moved, so a failed commit is reported rather than
	// rolled back: undoing the move to keep history tidy would throw away the
	// human's review decision, which is the expensive part of the gesture.
	warn := recordKnowledgeCommit([]string{dest, a.Path}, gestureMessage("promote", a))
	return dest, warn, nil
}

// rejectCandidate moves a candidate into the _rejected/ archive, preserving its
// contents and timestamped filename for extractor tuning, and records the
// decision as a committed log.md entry — the archive itself is deliberately not
// in the pathspec, so whether it is tracked, gitignored, moved or pruned cannot
// degrade the audit record. Returns the destination path and, when the record
// did not land, the reason.
func rejectCandidate(a Artifact) (string, string, error) {
	plural := pluralType(a.Type)
	if plural == "" {
		return "", "", fmt.Errorf("unknown type %q", a.Type)
	}
	if a.Status != "candidate" {
		return "", "", fmt.Errorf("not a candidate")
	}
	destDir := filepath.Join(KnowledgeRoot(), "_candidates", "_rejected", plural, a.Scope)
	dest := filepath.Join(destDir, filepath.Base(a.Path))
	if _, err := os.Stat(dest); err == nil {
		return "", "", fmt.Errorf("%s already exists", shortenPath(dest))
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", "", err
	}
	if err := os.Rename(a.Path, dest); err != nil {
		return "", "", err
	}
	// As with promote: the file has moved, so a failed record surfaces as a
	// warning instead of undoing the archive.
	logPath := filepath.Join(KnowledgeRoot(), "log.md")
	if err := appendRejectLog(logPath, rejectLogEntry(a)); err != nil {
		return dest, recordKnowledgeFailure(gestureMessage("reject", a), err), nil
	}
	// The source stays in the pathspec — its removal is a real change to the
	// candidates tree — and commitKnowledge drops it when it was never tracked.
	// The pathspec is file-granular, so entries the extractor has already
	// appended to log.md without committing (extract.py, appendRetrospectLog)
	// are absorbed into this commit. Accepted: the decision record and the
	// extractor share one append-only file, and keeping the decision out of the
	// archive is what makes it durable.
	warn := recordKnowledgeCommit([]string{logPath, a.Path}, gestureMessage("reject", a))
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
// worst-case entry — every field at its bound — still fits gestureMessageMax
// however the other three or the format change.
const (
	logFieldIDMax    = 50
	logFieldScopeMax = 20
	logFieldTypeMax  = 12
)

var logFieldBaseMax = gestureMessageMax - logEntryFixedRunes - logFieldIDMax - logFieldScopeMax - logFieldTypeMax

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
// are sanitized and bounded before composition; sanitizeRecord then backstops
// the composed line, as it does the commit subject.
func rejectLogEntry(a Artifact) string {
	entry := fmt.Sprintf(rejectLogFormat,
		time.Now().Format("2006-01-02"),
		logField(a.ID, logFieldIDMax),
		logField(a.Scope, logFieldScopeMax),
		logField(a.Type, logFieldTypeMax),
		logFieldTail(filepath.Base(a.Path), logFieldBaseMax))
	return "\n" + sanitizeRecord(entry) + "\n"
}

// appendRejectLog appends one entry to the store's log.md. Opened without
// O_CREATE — log.md is bootstrapped at store init, and a TUI pointed at a wrong
// root must not scatter one — so the perm argument is 0: it applies to nothing,
// and a mode here would be the wrong one the day O_CREATE were added. Unlike the
// extractor, which skips silently when the file is absent, the caller reports
// the failure — here the entry is the record.
func appendRejectLog(path, entry string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return fmt.Errorf("log.md: %w", err)
	}
	defer f.Close()
	if _, err := f.WriteString(entry); err != nil {
		return fmt.Errorf("log.md: %w", err)
	}
	return nil
}

// gestureMessage is the one-line commit subject for a promote or reject,
// naming the artifact's type, scope and id so the history reads as the review
// decisions it records.
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
