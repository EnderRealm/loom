package knowledge

import (
	"slices"
	"testing"
)

func art(id, scope, title, claim, body string, evidence ...string) Artifact {
	return Artifact{
		ID:            id,
		Scope:         scope,
		Title:         title,
		Claim:         claim,
		Body:          body,
		Status:        "validated",
		EvidencePaths: evidence,
	}
}

// TestRankSignalPrecedence pins the tiering: a citation outranks any
// file-overlap match, which outranks any keyword match, regardless of how
// many keywords or evidence paths the weaker matches accumulate.
func TestRankSignalPrecedence(t *testing.T) {
	ref := TicketRef{
		ID:      "loom/add-relevant-83f2",
		BareID:  "add-relevant-83f2",
		Project: "loom",
		Title:   "Add relevant command for knowledge ranking",
		Body:    "Scans corpus at internal/knowledge/relevant.go and ranks artifacts by overlap signal score precedence ordering",
	}

	cited := art("a-citation", "other", "unrelated title here", "",
		"sources:\n  - session: x\n    ticket: loom/add-relevant-83f2\n")
	fileOverlap := art("b-file", "other", "unrelated", "", "no citation here",
		"internal/knowledge/relevant.go")
	// keyword artifact shares many distinct words to inflate its score.
	keyword := art("c-keyword", "loom",
		"relevant command knowledge ranking overlap signal score precedence ordering",
		"relevant command knowledge ranking overlap signal score precedence ordering corpus artifacts",
		"plain body")

	got := Rank(ref, []Artifact{keyword, fileOverlap, cited})
	if len(got) != 3 {
		t.Fatalf("got %d matches, want 3", len(got))
	}
	wantOrder := []struct {
		id     string
		signal string
	}{
		{"a-citation", "citation"},
		{"b-file", "file-overlap"},
		{"c-keyword", "keyword"},
	}
	for i, w := range wantOrder {
		if got[i].Artifact.ID != w.id || got[i].Signal != w.signal {
			t.Errorf("position %d = %s/%s, want %s/%s", i,
				got[i].Artifact.ID, got[i].Signal, w.id, w.signal)
		}
	}
	if got[2].Score <= got[0].Score {
		t.Logf("keyword score=%d citation score=%d (precedence must override score)",
			got[2].Score, got[0].Score)
	}
}

// TestRankScopeTieBreak: two artifacts with identical signal tier and score
// are ordered by scope match (ticket project, then universal) before ID.
func TestRankScopeTieBreak(t *testing.T) {
	ref := TicketRef{
		ID:      "loom/x",
		BareID:  "x",
		Project: "loom",
		Title:   "alpha beta gamma delta",
		Body:    "",
	}
	offScope := art("z-off", "forge", "alpha beta", "", "b")
	inScope := art("a-in", "loom", "alpha beta", "", "b")
	universal := art("m-univ", "universal", "alpha beta", "", "b")

	got := Rank(ref, []Artifact{offScope, inScope, universal})
	if len(got) != 3 {
		t.Fatalf("got %d, want 3", len(got))
	}
	// in-scope and universal both rank as scope match; ID breaks their tie.
	// off-scope ranks last.
	if got[len(got)-1].Artifact.ID != "z-off" {
		t.Errorf("off-scope artifact should rank last, got order %v", ids(got))
	}
	if !slices.Contains(ids(got)[:2], "a-in") || !slices.Contains(ids(got)[:2], "m-univ") {
		t.Errorf("scope-matching artifacts should rank first, got %v", ids(got))
	}
}

// TestRankParentCitation: a citation of the parent ID counts.
func TestRankParentCitation(t *testing.T) {
	ref := TicketRef{
		ID:      "loom/child-1234",
		BareID:  "child-1234",
		Parent:  "loom/parent-epic-99aa",
		Project: "loom",
		Title:   "child work",
		Body:    "",
	}
	cited := art("via-parent", "loom", "t", "", "sources:\n  - ticket: parent-epic-99aa\n")
	got := Rank(ref, []Artifact{cited})
	if len(got) != 1 || got[0].Signal != "citation" {
		t.Fatalf("parent-ID citation not detected: %+v", got)
	}
}

// TestRankZeroSignalExcluded: an artifact matching no signal (scope alone) is
// dropped entirely.
func TestRankZeroSignalExcluded(t *testing.T) {
	ref := TicketRef{ID: "loom/x", BareID: "x", Project: "loom", Title: "zzz", Body: ""}
	scopeOnly := art("scope-only", "loom", "completely different words", "", "no overlap")
	got := Rank(ref, []Artifact{scopeOnly})
	if len(got) != 0 {
		t.Errorf("zero-signal artifact should be excluded, got %v", ids(got))
	}
}

// TestRankKeywordMinimum: a single shared keyword is noise; two are required.
func TestRankKeywordMinimum(t *testing.T) {
	ref := TicketRef{
		ID:      "loom/x",
		BareID:  "x",
		Project: "loom",
		Title:   "shipper summarizer pipeline",
		Body:    "",
	}
	oneShared := art("one", "loom", "shipper unrelated other", "", "b")
	twoShared := art("two", "loom", "shipper summarizer", "", "b")

	got := Rank(ref, []Artifact{oneShared, twoShared})
	if len(got) != 1 {
		t.Fatalf("got %d matches, want 1 (single-keyword match is noise)", len(got))
	}
	if got[0].Artifact.ID != "two" {
		t.Errorf("expected the two-keyword artifact, got %s", got[0].Artifact.ID)
	}
}

// TestRankUniversalCountsAsScopeMatch: universal scope ranks as a scope match
// even though it doesn't equal the ticket's project.
func TestRankUniversalCountsAsScopeMatch(t *testing.T) {
	ref := TicketRef{ID: "loom/x", BareID: "x", Project: "loom", Title: "alpha beta", Body: ""}
	univ := art("u", "universal", "alpha beta", "", "b")
	off := art("o", "forge", "alpha beta", "", "b")
	got := Rank(ref, []Artifact{off, univ})
	if len(got) != 2 {
		t.Fatalf("got %d, want 2", len(got))
	}
	if got[0].Artifact.ID != "u" {
		t.Errorf("universal scope should outrank off-scope on tie, got %v", ids(got))
	}
}

// TestRankFileOverlapSuffixMatch: evidence path matches a ticket path token by
// path-component suffix and by basename.
func TestRankFileOverlapSuffixMatch(t *testing.T) {
	ref := TicketRef{
		ID:      "loom/x",
		BareID:  "x",
		Project: "loom",
		Title:   "fix the loader",
		Body:    "the bug is in `cmd/loom/main.go` and also touches config.go",
	}
	suffix := art("suffix", "loom", "no shared words at", "", "b", "loom/cmd/loom/main.go")
	basename := art("base", "loom", "no shared words at", "", "b", "internal/x/config.go")
	got := Rank(ref, []Artifact{suffix, basename})
	if len(got) != 2 {
		t.Fatalf("got %d, want 2: %v", len(got), ids(got))
	}
	for _, m := range got {
		if m.Signal != "file-overlap" {
			t.Errorf("%s signal = %s, want file-overlap", m.Artifact.ID, m.Signal)
		}
	}
}

func ids(ms []Match) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.Artifact.ID
	}
	return out
}
