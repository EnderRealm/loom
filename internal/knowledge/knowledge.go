// Package knowledge loads the durable truth/decision corpus under
// ~/.loom/knowledge/ and ranks artifacts by relevance to a ticket. The
// loader is shared by the TUI candidate-review screen and the `loom
// relevant` command; both read the same on-disk shape.
package knowledge

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"loom/internal/config"
)

// Artifact is one truth or decision file under ~/.loom/knowledge/. Both
// validated artifacts (truths/<scope>/, decisions/<scope>/) and candidates
// (_candidates/<type>/<scope>/) are loaded into this same shape; Status
// distinguishes them.
type Artifact struct {
	ID       string
	Title    string
	Scope    string
	Type     string // "truth" | "decision"
	Status   string // "validated" | "candidate"
	Path     string
	Body     string // full file contents (eager-loaded; corpus is small)
	Modified time.Time

	// FirstNoticed is the earliest parseable `sources[].date` in the
	// frontmatter — when the fact was learned, as opposed to when the
	// pipeline wrote the file. Zero when no source carries a usable date.
	FirstNoticed time.Time

	// EvidencePaths are project-relative paths from the `evidence:` block,
	// used by the ranker for file-overlap scoring.
	EvidencePaths []string
	// Claim is the text of the `## Claim` section, used for keyword overlap.
	Claim string

	// Contradicts holds the artifact ids named by the `contradicts:` field.
	// Entries that are not an id — a `- file:` mapping (the doc-override shape
	// the store's truths/_schema.md documents), a `- path:` entry, an
	// unterminated flow list, block content with no list dash — are kept
	// verbatim in badContradicts so linkContradictions can warn about them
	// rather than drop them.
	Contradicts    []string
	badContradicts []string

	// Set by Load on candidates: the validated ids this candidate contradicts,
	// and one line per contradicts entry that resolved to no validated artifact.
	Conflicts           []string
	ContradictsWarnings []string
	// Set by Load on validated artifacts: the ids of candidates contradicting it.
	ContradictedBy []string
}

// AgeBasis returns the timestamp an artifact's age is measured from:
// FirstNoticed when the frontmatter carried a usable source date, else the
// file mtime. Both the list's AGE column and Load's ordering use it so the
// sort key cannot drift from the displayed value.
func (a Artifact) AgeBasis() time.Time {
	if !a.FirstNoticed.IsZero() {
		return a.FirstNoticed
	}
	return a.Modified
}

// Root returns the durable knowledge store path, honoring
// LOOM_KNOWLEDGE_ROOT for parity with the extractors. Defaults to
// $LOOM_HOME/knowledge.
func Root() string {
	if v := os.Getenv("LOOM_KNOWLEDGE_ROOT"); v != "" {
		return v
	}
	return filepath.Join(config.Home(), "knowledge")
}

// Load walks the store and returns every artifact: candidates first
// (status=candidate), then validated (status=validated). Within each
// status group, results are sorted oldest-first by AgeBasis (first-noticed
// date, falling back to file mtime) so the stalest claims — the ones most
// likely to need re-verifying — surface at the top of the review list.
func Load() ([]Artifact, error) {
	root := Root()
	var out []Artifact

	// Validated: <type>s/<scope>/*.md
	for _, t := range []string{"truths", "decisions"} {
		base := filepath.Join(root, t)
		more, err := walkArtifacts(base, t, "validated")
		if err != nil {
			return nil, err
		}
		out = append(out, more...)
	}

	// Candidates: _candidates/<type>s/<scope>/*.md (skip _rejected/)
	for _, t := range []string{"truths", "decisions"} {
		base := filepath.Join(root, "_candidates", t)
		more, err := walkArtifacts(base, t, "candidate")
		if err != nil {
			return nil, err
		}
		out = append(out, more...)
	}

	sort.SliceStable(out, func(i, j int) bool {
		// candidates first (most actionable), then validated
		if out[i].Status != out[j].Status {
			return out[i].Status == "candidate"
		}
		return out[i].AgeBasis().Before(out[j].AgeBasis())
	})
	linkContradictions(out)
	return out, nil
}

// linkContradictions resolves each candidate's contradicts entries against the
// validated artifacts, recording the link on both sides so a reviewer landing
// on either sees the other. An entry that names no validated artifact, or is
// not an id at all, becomes a warning on the candidate. Only candidates are
// linked: the store's validated artifacts use contradicts: for the
// doc-override entries truths/_schema.md documents, which name files rather
// than artifacts. A candidate id is linked to a target once, however many
// times it is named or however many files carry it.
func linkContradictions(arts []Artifact) {
	validated := map[string][]int{}
	for i, a := range arts {
		if a.Status == "validated" {
			validated[a.ID] = append(validated[a.ID], i)
		}
	}
	for i := range arts {
		c := &arts[i]
		if c.Status != "candidate" {
			continue
		}
		for _, raw := range c.badContradicts {
			c.ContradictsWarnings = append(c.ContradictsWarnings,
				fmt.Sprintf("unreadable contradicts entry %q (expected a list of artifact ids)", raw))
		}
		for _, id := range c.Contradicts {
			targets := validated[id]
			if len(targets) == 0 {
				c.ContradictsWarnings = append(c.ContradictsWarnings,
					fmt.Sprintf("contradicts %s, but no validated artifact has that id", id))
				continue
			}
			if slices.Contains(c.Conflicts, id) {
				continue
			}
			c.Conflicts = append(c.Conflicts, id)
			for _, j := range targets {
				if !slices.Contains(arts[j].ContradictedBy, c.ID) {
					arts[j].ContradictedBy = append(arts[j].ContradictedBy, c.ID)
				}
			}
		}
	}
}

func walkArtifacts(base, plural, status string) ([]Artifact, error) {
	if _, err := os.Stat(base); os.IsNotExist(err) {
		return nil, nil
	}
	var out []Artifact
	entries, err := os.ReadDir(base)
	if err != nil {
		return nil, err
	}
	for _, scopeEntry := range entries {
		if !scopeEntry.IsDir() {
			continue
		}
		// Skip _rejected/ archive (sibling of <scope> dirs under _candidates/)
		if strings.HasPrefix(scopeEntry.Name(), "_") {
			continue
		}
		scope := scopeEntry.Name()
		scopeDir := filepath.Join(base, scope)
		files, err := os.ReadDir(scopeDir)
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".md") {
				continue
			}
			if strings.HasPrefix(f.Name(), "_") || f.Name() == "README.md" {
				continue
			}
			path := filepath.Join(scopeDir, f.Name())
			body, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			info, _ := f.Info()
			a := parseArtifact(string(body), path, scope, plural, status)
			if a.ID == "" {
				continue
			}
			if info != nil {
				a.Modified = info.ModTime()
			}
			out = append(out, a)
		}
	}
	return out, nil
}

// frontmatterKey matches a top-level "key: value" line inside a `---` block.
// Indented lines (sub-fields under sources:, evidence:) are ignored.
var frontmatterKey = regexp.MustCompile(`^([a-z_]+):\s*(.*)$`)

// evidencePath matches an indented "- path: <value>" sub-line inside the
// `evidence:` block.
var evidencePath = regexp.MustCompile(`^\s*-\s*path:\s*(.*)$`)

// sourceDate matches an indented "date: <value>" sub-line inside the
// `sources:` block, with or without the entry's leading dash.
var sourceDate = regexp.MustCompile(`^\s*-?\s*date:\s*(.*)$`)

// listItem matches a "- <value>" entry of a block list.
var listItem = regexp.MustCompile(`^\s*-\s*(.*)$`)

// artifactID is the shape of an artifact id (`<scope>-<kebab-slug>`), used to
// tell a contradicts entry naming an artifact from any other value.
var artifactID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// sourceDateLayout is the date layout for `sources[].date`.
const sourceDateLayout = "2006-01-02"

// parseSourceDate reads a `sources[].date` value, reporting false when it
// carries no date at all (a `<YYYY-MM-DD>` placeholder); the artifact then
// falls back to its file mtime.
//
// A leading date is read even when text follows it: the extractor emits a
// range (`2026-03-19 to 2026-03-22`, `2026-03-09/2026-03-10`) for a session
// spanning several days, and the start of the range is when the fact was
// first noticed. The whole-value attempt cannot yield a different answer than
// the prefix one — the layout consumes exactly 10 bytes and time.Parse
// rejects trailing text, so a value parsing whole is 10 bytes and is its own
// prefix — and is kept only to state the single-date case explicitly.
func parseSourceDate(v string) (time.Time, bool) {
	if d, err := time.Parse(sourceDateLayout, v); err == nil {
		return d, true
	}
	if len(v) < len(sourceDateLayout) {
		return time.Time{}, false
	}
	if d, err := time.Parse(sourceDateLayout, v[:len(sourceDateLayout)]); err == nil {
		return d, true
	}
	return time.Time{}, false
}

// addContradicts records one contradicts entry: as an id when it reads as one
// (quotes stripped), verbatim in badContradicts otherwise. Reports whether it
// was an id.
func (a *Artifact) addContradicts(v string) bool {
	v = strings.TrimSpace(v)
	id := strings.Trim(v, `"'`)
	if artifactID.MatchString(id) {
		a.Contradicts = append(a.Contradicts, id)
		return true
	}
	a.badContradicts = append(a.badContradicts, v)
	return false
}

// parseContradictsValue reads the inline value of a `contradicts:` line. Empty
// (or a YAML null) means nothing or a block list follows; `[...]` is a flow
// list of ids; anything else is read as a single entry.
func (a *Artifact) parseContradictsValue(v string) {
	v = strings.TrimSpace(v)
	switch v {
	case "", "null", "~":
		return
	}
	if !strings.HasPrefix(v, "[") || !strings.HasSuffix(v, "]") {
		a.addContradicts(v)
		return
	}
	for _, item := range strings.Split(v[1:len(v)-1], ",") {
		if strings.TrimSpace(item) != "" {
			a.addContradicts(item)
		}
	}
}

func parseArtifact(body, path, scope, plural, status string) Artifact {
	a := Artifact{Path: path, Scope: scope, Body: body, Status: status}
	switch plural {
	case "truths":
		a.Type = "truth"
	case "decisions":
		a.Type = "decision"
	}

	// Carve out frontmatter (between the first two `---` lines).
	lines := strings.Split(body, "\n")
	if len(lines) < 2 || strings.TrimSpace(lines[0]) != "---" {
		return a
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return a
	}
	var curKey string
	// Indent of the contradicts: block line that opened a non-id entry, or -1.
	// Lines indented deeper than it continue that entry (a mapping's claim:,
	// status:) and are not entries of their own.
	openEntryIndent := -1
	for _, line := range lines[1:end] {
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") || strings.HasPrefix(line, "-") {
			// Sub-line of the current top-level key. Collect evidence paths
			// and source dates; other sub-fields (note:, line:, session:) are
			// ignored.
			if curKey == "evidence" {
				if m := evidencePath.FindStringSubmatch(line); m != nil {
					a.EvidencePaths = append(a.EvidencePaths, strings.TrimSpace(m[1]))
				}
			}
			if curKey == "sources" {
				if m := sourceDate.FindStringSubmatch(line); m != nil {
					if d, ok := parseSourceDate(strings.TrimSpace(m[1])); ok {
						if a.FirstNoticed.IsZero() || d.Before(a.FirstNoticed) {
							a.FirstNoticed = d
						}
					}
				}
			}
			if curKey == "contradicts" && strings.TrimSpace(line) != "" {
				indent := len(line) - len(strings.TrimLeft(line, " \t"))
				if m := listItem.FindStringSubmatch(line); m != nil {
					openEntryIndent = -1
					if !a.addContradicts(m[1]) {
						openEntryIndent = indent
					}
				} else if openEntryIndent < 0 || indent <= openEntryIndent {
					// Block content with no list dash (an indented scalar, a
					// mapping) is not a list of ids, even when it reads as one
					// id; kept so it warns.
					a.badContradicts = append(a.badContradicts, strings.TrimSpace(line))
					openEntryIndent = indent
				}
			}
			continue
		}
		m := frontmatterKey.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		curKey = m[1]
		// Last-write-wins matches the extractor's inject_frontmatter behavior.
		switch m[1] {
		case "id":
			a.ID = strings.TrimSpace(m[2])
		case "title":
			a.Title = strings.TrimSpace(m[2])
		case "status":
			if v := strings.TrimSpace(m[2]); v != "" {
				a.Status = v
			}
		case "contradicts":
			a.Contradicts, a.badContradicts = nil, nil
			openEntryIndent = -1
			a.parseContradictsValue(m[2])
		}
	}

	a.Claim = claimSection(lines[end+1:])
	return a
}

// claimSection returns the text under the `## Claim` heading, up to the next
// `## ` heading or end of file.
func claimSection(lines []string) string {
	start := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == "## Claim" {
			start = i + 1
			break
		}
	}
	if start < 0 {
		return ""
	}
	var b strings.Builder
	for _, line := range lines[start:] {
		if strings.HasPrefix(line, "## ") {
			break
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String())
}
