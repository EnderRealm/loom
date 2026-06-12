// Package knowledge loads the durable truth/decision corpus under
// ~/.loom/knowledge/ and ranks artifacts by relevance to a ticket. The
// loader is shared by the TUI candidate-review screen and the `loom
// relevant` command; both read the same on-disk shape.
package knowledge

import (
	"os"
	"path/filepath"
	"regexp"
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

	// EvidencePaths are project-relative paths from the `evidence:` block,
	// used by the ranker for file-overlap scoring.
	EvidencePaths []string
	// Claim is the text of the `## Claim` section, used for keyword overlap.
	Claim string
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

// Load walks the store and returns every artifact: validated first
// (status=validated), then candidates (status=candidate). Within each
// status group, results are sorted newest-first by file mtime so recent
// extractions surface at the top of the review list.
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
		return out[i].Modified.After(out[j].Modified)
	})
	return out, nil
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
	for _, line := range lines[1:end] {
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") || strings.HasPrefix(line, "-") {
			// Sub-line of the current top-level key. Collect evidence paths;
			// other sub-fields (note:, line:, sources: entries) are ignored.
			if curKey == "evidence" {
				if m := evidencePath.FindStringSubmatch(line); m != nil {
					a.EvidencePaths = append(a.EvidencePaths, strings.TrimSpace(m[1]))
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
