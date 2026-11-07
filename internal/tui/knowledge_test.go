package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

// TestKnowledgeOverlaySurfacesContradictions renders the overlay over a store
// holding a validated truth, a candidate contradicting it and a candidate
// naming no known artifact, and checks each side names the other.
func TestKnowledgeOverlaySurfacesContradictions(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LOOM_KNOWLEDGE_ROOT", root)

	write := func(rel, id, status, contradicts string) {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		body := "---\nid: " + id + "\ntitle: Title of " + id + "\nstatus: " + status + "\n" +
			contradicts + "---\n\n## Claim\n\nyes\n"
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("truths/loom/target.md", "loom-target", "validated", "contradicts: []\n")
	write("_candidates/truths/loom/against--1.md", "loom-against", "candidate", "contradicts:\n  - loom-target\n")
	write("_candidates/truths/loom/dangling--1.md", "loom-dangling", "candidate", "contradicts: [loom-nowhere]\n")

	arts, err := LoadKnowledge()
	if err != nil {
		t.Fatalf("LoadKnowledge: %v", err)
	}
	m := knowledgeModel{}
	m.setSize(160, 20)
	m.setArtifacts(arts)

	list := m.view()
	for _, want := range []string{
		"! Title of loom-against",
		"? Title of loom-dangling",
		"! Title of loom-target",
		"1 contradiction(s)",
		"1 unresolved contradicts",
	} {
		if !strings.Contains(list, want) {
			t.Errorf("list view missing %q:\n%s", want, list)
		}
	}

	detail := func(id string) string {
		t.Helper()
		for i, a := range m.artifacts {
			if a.ID == id {
				m.cursor = i
				m.showDetail = true
				return m.view()
			}
		}
		t.Fatalf("no artifact %s", id)
		return ""
	}
	if d := detail("loom-against"); !strings.Contains(d, "contradicts validated loom-target") {
		t.Errorf("candidate detail does not name its target:\n%s", d)
	}
	if d := detail("loom-target"); !strings.Contains(d, "contradicted by candidate loom-against") {
		t.Errorf("validated detail does not name its contradicting candidate:\n%s", d)
	}
	if d := detail("loom-dangling"); !strings.Contains(d, "loom-nowhere") {
		t.Errorf("dangling candidate detail does not carry its warning:\n%s", d)
	}
}

// TestKnowledgeListShowsDeprecatedStatus renders the list over a store holding a
// truth demoted in place, as the extractors' sweep leaves one: still under truths/,
// with status: deprecated. It reads as deprecated, in a style of its own, and
// stays out of the footer's validated count.
func TestKnowledgeListShowsDeprecatedStatus(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LOOM_KNOWLEDGE_ROOT", root)

	// lipgloss emits no colour off a terminal, which would leave every status
	// cell styled alike.
	profile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(profile) })

	write := func(rel, id, status string) {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		body := "---\nid: " + id + "\ntitle: Title of " + id + "\nstatus: " + status + "\n---\n\n## Claim\n\nyes\n"
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("truths/loom/kept.md", "loom-kept", "validated")
	write("truths/loom/also-kept.md", "loom-also-kept", "validated")
	write("truths/loom/demoted.md", "loom-demoted", "deprecated")
	write("_candidates/truths/loom/pending--1.md", "loom-pending", "candidate")

	arts, err := LoadKnowledge()
	if err != nil {
		t.Fatalf("LoadKnowledge: %v", err)
	}
	m := knowledgeModel{}
	m.setSize(160, 20)
	m.setArtifacts(arts)

	list := m.view()

	// statusCell returns the status word of id's row and the escape sequence
	// styling it.
	statusCell := func(id string) (word, style string) {
		t.Helper()
		for _, line := range strings.Split(list, "\n") {
			if strings.Contains(line, id) {
				word = strings.Fields(ansi.Strip(line))[0]
				return word, line[:strings.Index(line, word)]
			}
		}
		t.Fatalf("no row for %s:\n%s", id, list)
		return "", ""
	}
	validated, validatedStyle := statusCell("loom-kept")
	if validated != "validated" {
		t.Errorf("validated truth reads %q", validated)
	}
	if candidate, _ := statusCell("loom-pending"); candidate != "candidate" {
		t.Errorf("candidate reads %q", candidate)
	}
	deprecated, deprecatedStyle := statusCell("loom-demoted")
	if deprecated != "deprecated" {
		t.Errorf("deprecated truth reads %q", deprecated)
	}
	if deprecatedStyle == validatedStyle {
		t.Errorf("deprecated and validated cells share the style %q", deprecatedStyle)
	}

	if want := "2 validated · 1 candidate(s)"; !strings.Contains(ansi.Strip(list), want) {
		t.Errorf("footer missing %q:\n%s", want, ansi.Strip(list))
	}
}
