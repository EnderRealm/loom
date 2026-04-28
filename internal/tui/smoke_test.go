package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoadProjectsWithSummary confirms LoadProjects() merges in summary
// metrics from ~/.loom/summaries.db when the file exists.
func TestLoadProjectsWithSummary(t *testing.T) {
	projects, err := LoadProjects()
	if err != nil {
		t.Fatalf("LoadProjects: %v", err)
	}
	if len(projects) == 0 {
		t.Skip("no projects found on this host — TUI smoke test needs a populated ~/.loom")
	}
	var withSummary, withTools int
	for _, p := range projects {
		if p.TurnCount > 0 || p.ToolCallCount > 0 {
			withSummary++
		}
		if len(p.TopTools) > 0 {
			withTools++
		}
	}
	t.Logf("projects=%d withSummary=%d withTopTools=%d",
		len(projects), withSummary, withTools)

	// Pick the highest-activity project and render the patterns block to
	// confirm it doesn't crash and produces output.
	var pick *Project
	for i := range projects {
		if pick == nil || projects[i].ToolCallCount > pick.ToolCallCount {
			pick = &projects[i]
		}
	}
	if pick == nil {
		t.Skip("no project found")
	}
	d := newDetailModel(pick, 120, 50)
	out := d.renderPatterns(118)
	if out == "" && pick.ToolCallCount > 0 {
		t.Errorf("expected patterns output for project with %d tool calls",
			pick.ToolCallCount)
	}
	t.Logf("project=%q turns=%d tools=%d errors=%d compactions=%d topTools=%d",
		pick.Name, pick.TurnCount, pick.ToolCallCount,
		pick.ErrorCount, pick.Compactions, len(pick.TopTools))
	if len(pick.TopTools) > 0 {
		t.Logf("top tools (top 5):")
		max := 5
		if len(pick.TopTools) < max {
			max = len(pick.TopTools)
		}
		for i := 0; i < max; i++ {
			ts := pick.TopTools[i]
			t.Logf("  %-12s calls=%d errors=%d avgMs=%d",
				ts.Kind, ts.Calls, ts.Errors, ts.AvgMs)
		}
	}
}

// TestLoadProjectsIdentityGrouping pins the wire-level identity story
// against synthetic data: two slugs from different machines that share a
// git remote roll up into one project; a slug with no sidecar falls back
// to slug-only grouping and stays separate.
func TestLoadProjectsIdentityGrouping(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("LOOM_HOME", tmp)

	mustWrite := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	rec := filepath.Join(tmp, "received", "claude-code")

	// steve's clone of loom: full HTTPS remote.
	mustWrite(filepath.Join(rec, "-Users-steve-code-loom", "s1.jsonl"), "x\n")
	mustWrite(filepath.Join(rec, "-Users-steve-code-loom", "s1.meta.json"),
		`{"git_remote":"https://github.com/EnderRealm/loom.git","cwd":"/Users/steve/code/loom","root_slug":"-Users-steve-code-loom"}`)

	// smacbeth's clone, same repo via SSH — must collapse with steve's.
	mustWrite(filepath.Join(rec, "-Users-smacbeth-src-loom", "s2.jsonl"), "x\n")
	mustWrite(filepath.Join(rec, "-Users-smacbeth-src-loom", "s2.meta.json"),
		`{"git_remote":"git@github.com:EnderRealm/loom.git","cwd":"/Users/smacbeth/src/loom","root_slug":"-Users-smacbeth-src-loom"}`)

	// Forge worktree under steve's loom — same remote, different cwd. Must
	// still group with steve's loom (remote is canonical).
	mustWrite(filepath.Join(rec, "-Users-steve-code-loom--claude-worktrees-feature", "s3.jsonl"), "x\n")
	mustWrite(filepath.Join(rec, "-Users-steve-code-loom--claude-worktrees-feature", "s3.meta.json"),
		`{"git_remote":"https://github.com/EnderRealm/loom.git","cwd":"/Users/steve/code/loom/.claude/worktrees/feature","root_slug":"-Users-steve-code-loom--claude-worktrees-feature"}`)

	// Different repo, same basename — must NOT collapse with loom.
	mustWrite(filepath.Join(rec, "-Users-steve-code-other-loom", "s4.jsonl"), "x\n")
	mustWrite(filepath.Join(rec, "-Users-steve-code-other-loom", "s4.meta.json"),
		`{"git_remote":"https://example.com/elsewhere/loom.git","cwd":"/Users/steve/code/other/loom","root_slug":"-Users-steve-code-other-loom"}`)

	// Legacy session: no sidecar. Must show as its own project under the
	// slug-only fallback.
	mustWrite(filepath.Join(rec, "-Users-steve-code-legacy", "s5.jsonl"), "x\n")

	projects, err := LoadProjects()
	if err != nil {
		t.Fatalf("LoadProjects: %v", err)
	}

	// Bucket by normalized remote so the lookup is independent of which
	// URL variant was seen first.
	byRemote := map[string]*Project{}
	for i := range projects {
		p := &projects[i]
		if p.GitRemote != "" {
			byRemote[normalizeRemote(p.GitRemote)] = p
		}
	}

	loom := byRemote["github.com/enderrealm/loom"]
	if loom == nil {
		t.Fatalf("expected EnderRealm/loom group; got %d projects: %+v", len(projects), projectKeys(projects))
	}
	if loom.SessionCount != 3 {
		t.Errorf("EnderRealm/loom: SessionCount = %d, want 3 (steve + smacbeth + worktree)", loom.SessionCount)
	}
	if len(loom.Slugs) != 3 {
		t.Errorf("EnderRealm/loom Slugs = %d, want 3", len(loom.Slugs))
	}

	other := byRemote["example.com/elsewhere/loom"]
	if other == nil {
		t.Errorf("expected separate group for elsewhere/loom — basename collision must not collapse")
	} else if other.SessionCount != 1 {
		t.Errorf("elsewhere/loom SessionCount = %d, want 1", other.SessionCount)
	}

	// The legacy slug-only session should stand alone.
	var legacyFound bool
	for _, p := range projects {
		if p.GitRemote == "" && p.Cwd == "" {
			legacyFound = true
			if p.SessionCount != 1 {
				t.Errorf("legacy slug project SessionCount = %d, want 1", p.SessionCount)
			}
		}
	}
	if !legacyFound {
		t.Errorf("expected one legacy (no-identity) project, got none")
	}
}

func projectKeys(ps []Project) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.Name+"|"+p.GitRemote)
	}
	return out
}

// TestLoadKnowledge confirms the knowledge loader walks ~/.loom/knowledge/,
// classifies validated vs candidate artifacts, and that the list view
// renders without panicking.
func TestLoadKnowledge(t *testing.T) {
	arts, err := LoadKnowledge()
	if err != nil {
		t.Fatalf("LoadKnowledge: %v", err)
	}
	if len(arts) == 0 {
		t.Skipf("no artifacts under %s — skip", KnowledgeRoot())
	}

	var nValidated, nCandidate, nTruth, nDecision int
	scopes := map[string]int{}
	for _, a := range arts {
		switch a.Status {
		case "validated":
			nValidated++
		case "candidate":
			nCandidate++
		}
		switch a.Type {
		case "truth":
			nTruth++
		case "decision":
			nDecision++
		}
		scopes[a.Scope]++
		if a.ID == "" {
			t.Errorf("artifact at %s has empty ID", a.Path)
		}
	}
	t.Logf("loaded=%d validated=%d candidate=%d truths=%d decisions=%d scopes=%v",
		len(arts), nValidated, nCandidate, nTruth, nDecision, scopes)

	// Render the list view to confirm it doesn't panic on real data.
	m := knowledgeModel{}
	m.setSize(120, 30)
	m.setArtifacts(arts)
	out := m.view()
	if out == "" {
		t.Errorf("knowledge list view rendered empty for %d artifacts", len(arts))
	}
	if !strings.Contains(out, "STATUS") {
		t.Errorf("expected STATUS column header in list view")
	}

	// Render detail view for the selected artifact.
	m.showDetail = true
	det := m.view()
	if det == "" {
		t.Errorf("knowledge detail view rendered empty")
	}
}
