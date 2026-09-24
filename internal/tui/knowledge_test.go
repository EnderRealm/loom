package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
