package escapes

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"regexp"
	"testing"

	"github.com/EnderRealm/ticket/v8/pkg/ticket"

	"loom/internal/config"
)

const fixturePath = "testdata/labels.jsonl"

var fullHash = regexp.MustCompile(`^[0-9a-f]{40}$`)

func loadFixture(t *testing.T) []Label {
	t.Helper()
	labels, err := LoadLabels(fixturePath)
	if err != nil {
		t.Fatalf("LoadLabels: %v", err)
	}
	return labels
}

// doneBugs reads every ticket in the central store through `tk query
// --all-projects` and returns the qualified ids of those that are done bugs.
func doneBugs(t *testing.T) map[string]bool {
	t.Helper()
	var stdout, stderr bytes.Buffer
	cmd := exec.Command("tk", "query", "--all-projects")
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("tk query --all-projects: %v: %s", err, stderr.String())
	}
	done := map[string]bool{}
	dec := json.NewDecoder(&stdout)
	for {
		var tk struct {
			ID     string `json:"id"`
			Status string `json:"status"`
			Type   string `json:"type"`
		}
		if err := dec.Decode(&tk); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatalf("tk query: decode: %v", err)
		}
		if tk.Status == string(ticket.StatusDone) && tk.Type == string(ticket.TypeBug) {
			done[tk.ID] = true
		}
	}
	return done
}

func TestFixtureWellFormed(t *testing.T) {
	labels := loadFixture(t)
	if len(labels) < 20 {
		t.Errorf("fixture holds %d labels, want at least 20", len(labels))
	}

	done := doneBugs(t)
	projects := map[string]bool{}
	seen := map[string]bool{}
	for i, l := range labels {
		project, bare := ticket.ParseNamespacedID(l.Ticket)
		if project == "" || bare == "" {
			t.Errorf("label %d: ticket %q is not a qualified project/id", i+1, l.Ticket)
			continue
		}
		projects[project] = true
		if seen[l.Ticket] {
			t.Errorf("label %d: duplicate ticket %s", i+1, l.Ticket)
		}
		seen[l.Ticket] = true
		if !done[l.Ticket] {
			t.Errorf("label %d: %s is not a done bug in the tk store", i+1, l.Ticket)
		}

		if len(l.Fix) == 0 {
			t.Errorf("%s: no fix commit", l.Ticket)
		}
		if (len(l.Introducing) > 0) == (l.Unattributable != "") {
			t.Errorf("%s: want exactly one of introducing and unattributable, got %d introducing and unattributable %q",
				l.Ticket, len(l.Introducing), l.Unattributable)
		}
		for _, h := range append(append([]string{}, l.Fix...), l.Introducing...) {
			if !fullHash.MatchString(h) {
				t.Errorf("%s: %q is not a full 40-hex commit hash", l.Ticket, h)
			}
		}
	}
	if len(projects) < 3 {
		t.Errorf("fixture spans %d projects, want at least 3", len(projects))
	}
}

func TestFixtureCommitsResolve(t *testing.T) {
	for _, l := range loadFixture(t) {
		project, _ := ticket.ParseNamespacedID(l.Ticket)
		repo, err := config.ProjectRepoPath(project)
		if err != nil {
			t.Errorf("%s: %v", l.Ticket, err)
			continue
		}
		for _, h := range append(append([]string{}, l.Fix...), l.Introducing...) {
			// Argv, never a shell: the hash and the path are fixture and
			// config data.
			out, err := exec.Command("git", "-C", repo, "cat-file", "-e", h+"^{commit}").CombinedOutput()
			if err != nil {
				t.Errorf("%s: %s does not resolve to a commit in %s: %v: %s", l.Ticket, h, repo, err, bytes.TrimSpace(out))
			}
		}
	}
}
