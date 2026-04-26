package tui

import (
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
