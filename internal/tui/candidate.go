package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

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
// extracted_at/extracted_by, flips status to validated, bumps verified_at).
// Returns the destination path. It never overwrites an existing validated file.
func promoteCandidate(a Artifact) (string, error) {
	plural := pluralType(a.Type)
	if plural == "" {
		return "", fmt.Errorf("unknown type %q", a.Type)
	}
	if a.Status != "candidate" {
		return "", fmt.Errorf("not a candidate")
	}
	destDir := filepath.Join(KnowledgeRoot(), plural, a.Scope)
	dest := filepath.Join(destDir, candidateKebab(a.ID, a.Scope)+".md")
	if _, err := os.Stat(dest); err == nil {
		return "", fmt.Errorf("%s already exists", shortenPath(dest))
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(dest, []byte(promoteFrontmatter(a.Body)), 0o644); err != nil {
		return "", err
	}
	// Write succeeded; drop the candidate. A leftover source would resurface
	// as a duplicate candidate on the next refresh.
	if err := os.Remove(a.Path); err != nil {
		return "", err
	}
	return dest, nil
}

// rejectCandidate moves a candidate into the _rejected/ archive, preserving its
// contents and timestamped filename for extractor tuning. Returns the
// destination path.
func rejectCandidate(a Artifact) (string, error) {
	plural := pluralType(a.Type)
	if plural == "" {
		return "", fmt.Errorf("unknown type %q", a.Type)
	}
	if a.Status != "candidate" {
		return "", fmt.Errorf("not a candidate")
	}
	destDir := filepath.Join(KnowledgeRoot(), "_candidates", "_rejected", plural, a.Scope)
	dest := filepath.Join(destDir, filepath.Base(a.Path))
	if _, err := os.Stat(dest); err == nil {
		return "", fmt.Errorf("%s already exists", shortenPath(dest))
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", err
	}
	if err := os.Rename(a.Path, dest); err != nil {
		return "", err
	}
	return dest, nil
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
