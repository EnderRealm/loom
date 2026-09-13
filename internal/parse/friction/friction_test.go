package friction

import (
	"reflect"
	"strings"
	"testing"
)

// TestKindsAreExactlySix pins the kind list: the six the ticket names, in
// order, and no bash.repeat.
func TestKindsAreExactlySix(t *testing.T) {
	want := []string{
		"hook.ask", "hook.deny", "permission.denied_by_user",
		"permission.classifier_denied", "tool.error", "user.interrupt",
	}
	if !reflect.DeepEqual(Kinds, want) {
		t.Fatalf("Kinds = %v, want %v", Kinds, want)
	}
	for _, k := range Kinds {
		if k == "bash.repeat" {
			t.Fatal("Kinds names bash.repeat, which is excluded on purpose")
		}
	}
}

func TestNormalize(t *testing.T) {
	cases := []struct{ in, want string }{
		{"session 1230a905-bd8e-4808-8057-38855c3e3434 ended", "session <uuid> ended"},
		{"session 1230A905-BD8E-4808-8057-38855C3E3434 ended", "session <uuid> ended"},
		{"killed pid 12345", "killed pid <pid>"},
		{"killed PID=42", "killed pid <pid>"},
		{"killed pid: 7", "killed pid <pid>"},
		{"File does not exist. Note: your current working directory is /Users/steve/code/loom", "File does not exist. Note: your current working directory is <path>"},
		{"ran ~/.claude/rm-gate.sh and failed", "ran <path> and failed"},
		{"/tmp/x.jsonl not found", "<path> not found"},
		{"input/output mismatch", "input/output mismatch"},
		{"Exit code 1", "Exit code 1"},
		{"took 12345 ms after 42 tries", "took <n> ms after 42 tries"},
		{"  spaced   \t out  ", "spaced out"},
		{"first line\nsecond line", "first line"},
		{"brew install ripgrep / apt install ripgrep", "brew install ripgrep / apt install ripgrep"},
	}
	for _, c := range cases {
		if got := Normalize(c.in); got != c.want {
			t.Errorf("Normalize(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if got := Normalize(strings.Repeat("x", 400)); len(got) != 300 {
		t.Errorf("Normalize cap: got %d chars, want 300", len(got))
	}
}
