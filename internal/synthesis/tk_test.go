package synthesis

import (
	"strings"
	"testing"
)

// ticketDoc renders a `tk show --metadata` document for loom/x-1 whose
// description is desc, followed by real Design and Acceptance Criteria
// sections.
func ticketDoc(desc ...string) string {
	lines := []string{"---", "id: x-1", "status: open", "---", "# Title", ""}
	lines = append(lines, desc...)
	lines = append(lines,
		"",
		"## Design",
		"Real design.",
		"",
		"## Acceptance Criteria",
		"- Real criterion.",
		"",
	)
	return strings.Join(lines, "\n")
}

func assertRealSections(t *testing.T, doc string, wantDesc []string) {
	t.Helper()
	body, err := parseBody("loom/x-1", doc)
	if err != nil {
		t.Fatalf("parseBody: %v", err)
	}
	if want := strings.Join(wantDesc, "\n"); body.Description != want {
		t.Errorf("Description = %q, want %q", body.Description, want)
	}
	if body.Design != "Real design." {
		t.Errorf("Design = %q, want %q", body.Design, "Real design.")
	}
	if body.AcceptanceCriteria != "- Real criterion." {
		t.Errorf("AcceptanceCriteria = %q, want %q", body.AcceptanceCriteria, "- Real criterion.")
	}
}

func TestParseBodyTildeFence(t *testing.T) {
	desc := []string{
		"Example ticket body:",
		"",
		"~~~markdown",
		"## Acceptance Criteria",
		"- Example criterion.",
		"~~~",
	}
	assertRealSections(t, ticketDoc(desc...), desc)
}

func TestParseBodyLongBacktickFence(t *testing.T) {
	desc := []string{
		"Example nested fence:",
		"",
		"````markdown",
		"A code block closes on:",
		"```",
		"## Design",
		"Example design.",
		"````",
	}
	assertRealSections(t, ticketDoc(desc...), desc)
}
