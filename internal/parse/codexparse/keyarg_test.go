package codexparse

import (
	"encoding/json"
	"strings"
	"testing"
)

// A shell command invoking the lens router survives whole past 200 chars in
// both the string and the array form, so the flags naming its lens, round
// and attempt reach the attempt model; any other command is still cut at
// 200 with the mark.
func TestRouterCommandKeepsItsFlagsPastTheCut(t *testing.T) {
	router := "S=/private/tmp/codex/-Users-steve-code-loom/e377ca19-58fa-43ea-bd4c-94f2a6eb0c3d/scratchpad\nmkdir -p $S/lens-logs $S/verdicts\n/Users/steve/.codex/work-policy/d0ad82d1d69e/codex-lens.sh --runtime codex --lens security --round 1 --attempt 1 --payload $S/security-payload.md > $S/verdicts/security.md"
	if len(router) <= keyArgLimit {
		t.Fatalf("fixture command is %d chars, want more than %d", len(router), keyArgLimit)
	}
	raw, _ := json.Marshal(router)
	if got := commandKeyArg(raw); got != router {
		t.Errorf("string router key arg = %q, want the whole command", got)
	}
	raw, _ = json.Marshal([]string{"bash", "-lc", router})
	if got, want := commandKeyArg(raw), "bash -lc "+router; got != want {
		t.Errorf("array router key arg = %q, want %q", got, want)
	}

	other := "cat " + strings.Repeat("/a-very-long-path", 20) + "/README.md"
	raw, _ = json.Marshal([]string{"bash", "-lc", other})
	if got, want := commandKeyArg(raw), ("bash -lc " + other)[:keyArgLimit]+"…"; got != want {
		t.Errorf("ordinary key arg = %q, want %q", got, want)
	}

	long := strings.Repeat("x", routerKeyArgLimit+1) + " codex-lens.sh --lens security"
	raw, _ = json.Marshal(long)
	if got, want := commandKeyArg(raw), long[:routerKeyArgLimit]+"…"; got != want {
		t.Errorf("oversized router key arg = %d chars, want cut at %d", len(got), routerKeyArgLimit)
	}

	// The function_call `arguments` path the normal transcript shape takes,
	// in both command forms; a non-command key is still cut at 200.
	args, _ := json.Marshal(map[string]any{"command": router})
	if got := extractFunctionKeyArg("shell", string(args)); got != router {
		t.Errorf("function_call string router key arg = %q, want the whole command", got)
	}
	args, _ = json.Marshal(map[string]any{"command": []string{"bash", "-lc", router}})
	if got, want := extractFunctionKeyArg("shell", string(args)), "bash -lc "+router; got != want {
		t.Errorf("function_call array router key arg = %q, want %q", got, want)
	}
	args, _ = json.Marshal(map[string]any{"prompt": router})
	if got, want := extractFunctionKeyArg("shell", string(args)), router[:keyArgLimit]+"…"; got != want {
		t.Errorf("function_call prompt key arg = %q, want %q", got, want)
	}
}

// The full parser keeps a router command whole on the tool call a
// function_call `shell` item opened: the exec_command_end that follows
// keeps the key argument the function_call set.
func TestParsedRouterCallKeepsItsFlags(t *testing.T) {
	router := "S=/private/tmp/codex/-Users-steve-code-loom/e377ca19-58fa-43ea-bd4c-94f2a6eb0c3d/scratchpad\nmkdir -p $S/lens-logs $S/verdicts\n/Users/steve/.codex/work-policy/d0ad82d1d69e/codex-lens.sh --runtime codex --lens security --round 1 --attempt 1 --payload $S/security-payload.md > $S/verdicts/security.md"
	cmd, _ := json.Marshal([]string{"bash", "-lc", router})
	args, _ := json.Marshal(`{"command": ` + string(cmd) + `}`)
	stream := `{"timestamp": "2026-09-03T09:00:01.000Z", "type": "turn_context", "payload": {"turn_id": "t1", "cwd": "/tmp/c", "model": "gpt-5.4"}}
{"timestamp": "2026-09-03T09:00:02.000Z", "type": "response_item", "payload": {"type": "message", "role": "user", "content": [{"type": "input_text", "text": "#work loom/keyarg-1111"}]}}
{"timestamp": "2026-09-03T09:00:03.000Z", "type": "response_item", "payload": {"type": "function_call", "name": "shell", "call_id": "call_sec", "arguments": ` + string(args) + `}}
{"timestamp": "2026-09-03T09:00:04.000Z", "type": "event_msg", "payload": {"type": "exec_command_end", "call_id": "call_sec", "command": ` + string(cmd) + `, "stdout": "", "stderr": "", "exit_code": 0, "duration": {"secs": 8, "nanos": 0}}}
{"timestamp": "2026-09-03T09:00:05.000Z", "type": "event_msg", "payload": {"type": "task_complete", "turn_id": "t1", "last_agent_message": "done"}}
`
	s, err := Parse(strings.NewReader(stream))
	if err != nil {
		t.Fatal(err)
	}
	if len(s.ToolCalls) != 1 {
		t.Fatalf("ToolCalls = %+v, want the one router call", s.ToolCalls)
	}
	if got, want := s.ToolCalls[0].KeyArg, "bash -lc "+router; got != want {
		t.Errorf("router key arg = %q, want %q", got, want)
	}
	for _, flag := range []string{"--lens security", "--round 1", "--attempt 1"} {
		if !strings.Contains(s.ToolCalls[0].KeyArg, flag) {
			t.Errorf("router key arg lost %q", flag)
		}
	}
}
