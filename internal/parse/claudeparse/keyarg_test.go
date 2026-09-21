package claudeparse

import (
	"encoding/json"
	"strings"
	"testing"
)

// A Bash command invoking the lens router survives whole past 200 chars, so
// the flags naming its lens, round and attempt reach the attempt model; any
// other command is still cut at 200 with the mark.
func TestRouterCommandKeepsItsFlagsPastTheCut(t *testing.T) {
	router := "S=/private/tmp/claude-501/-Users-steve-code-loom/e377ca19-58fa-43ea-bd4c-94f2a6eb0c3d/scratchpad\nmkdir -p $S/lens-logs $S/verdicts\n/Users/steve/.claude/work-policy/d0ad82d1d69e/codex-lens.sh --runtime claude --lens security --round 1 --attempt 1 --payload $S/security-payload.md > $S/verdicts/security.md"
	if len(router) <= keyArgLimit {
		t.Fatalf("fixture command is %d chars, want more than %d", len(router), keyArgLimit)
	}
	input, _ := json.Marshal(map[string]string{"command": router})
	if got := extractKeyArg("Bash", input); got != router {
		t.Errorf("router key arg = %q, want the whole command", got)
	}

	other := "cat " + strings.Repeat("/a-very-long-path", 20) + "/README.md"
	input, _ = json.Marshal(map[string]string{"command": other})
	if got, want := extractKeyArg("Bash", input), other[:keyArgLimit]+"…"; got != want {
		t.Errorf("ordinary key arg = %q, want %q", got, want)
	}

	long := strings.Repeat("x", routerKeyArgLimit+1) + " codex-lens.sh --lens security"
	input, _ = json.Marshal(map[string]string{"command": long})
	if got, want := extractKeyArg("Bash", input), long[:routerKeyArgLimit]+"…"; got != want {
		t.Errorf("oversized router key arg = %d chars, want cut at %d", len(got), routerKeyArgLimit)
	}
}
