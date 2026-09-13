package claudeparse

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"loom/internal/parse/friction"
)

// frictionSession is one turn carrying every friction shape the parser
// classifies, plus an allow hook that is not one: a hook ask, a hook deny,
// an exit-2 hook with its reason on stderr, a classifier denial, a user
// rejection with its interrupt beside it, a plain tool error and an
// interrupt on a bare prompt. Real ripgrep and rm-gate text, verbatim.
const frictionSession = `{"type":"user","uuid":"u1","sessionId":"sess","promptId":"p1","cwd":"/Users/steve/code/pencil-war","timestamp":"2026-09-08T00:49:00.000Z","message":{"role":"user","content":"go"}}
{"type":"assistant","uuid":"a1","sessionId":"sess","timestamp":"2026-09-08T00:49:01.000Z","message":{"role":"assistant","model":"claude-opus-5","content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"rm -rf $DIR"}},{"type":"tool_use","id":"toolu_2","name":"Grep","input":{"pattern":"x"}},{"type":"tool_use","id":"toolu_3","name":"Read","input":{"file_path":"/tmp/x"}},{"type":"tool_use","id":"toolu_4","name":"Edit","input":{"file_path":"/tmp/y"}}],"stop_reason":"tool_use"}}
{"type":"attachment","uuid":"h1","sessionId":"sess","timestamp":"2026-09-08T00:49:02.000Z","attachment":{"type":"hook_success","hookName":"PreToolUse:Bash","toolUseID":"toolu_1","hookEvent":"PreToolUse","content":"","stdout":"{\n  \"hookSpecificOutput\": {\n    \"hookEventName\": \"PreToolUse\",\n    \"permissionDecision\": \"ask\",\n    \"permissionDecisionReason\": \"rm-gate: at least one target comes from a variable or a substitution, so it cannot be resolved before the command runs\"\n  }\n}\n","stderr":"","exitCode":0,"command":"~/.claude/rm-gate.sh","durationMs":34}}
{"type":"attachment","uuid":"h2","sessionId":"sess","timestamp":"2026-09-08T00:49:03.000Z","attachment":{"type":"hook_success","hookName":"PreToolUse:Bash","toolUseID":"toolu_1","hookEvent":"PreToolUse","stdout":"{\"hookSpecificOutput\":{\"hookEventName\":\"PreToolUse\",\"permissionDecision\":\"deny\",\"permissionDecisionReason\":\"rm-gate: refusing rm on /Users/steve/code\"}}","stderr":"","exitCode":0,"command":"~/.claude/rm-gate.sh","durationMs":12}}
{"type":"attachment","uuid":"h3","sessionId":"sess","timestamp":"2026-09-08T00:49:04.000Z","attachment":{"type":"hook_success","hookName":"PreToolUse:Edit","toolUseID":"toolu_4","hookEvent":"PreToolUse","stdout":"","stderr":"edit-guard: generated file, edit the source in warp (pid 4242)\nsecond line","exitCode":2,"command":"~/.claude/edit-guard.sh","durationMs":9}}
{"type":"attachment","uuid":"h4","sessionId":"sess","timestamp":"2026-09-08T00:49:05.000Z","attachment":{"type":"hook_success","hookName":"PreToolUse:Grep","toolUseID":"toolu_2","hookEvent":"PreToolUse","stdout":"{\"hookSpecificOutput\":{\"hookEventName\":\"PreToolUse\",\"permissionDecision\":\"allow\"}}","stderr":"","exitCode":0,"command":"~/.claude/allow.sh","durationMs":3}}
{"type":"attachment","uuid":"h5","sessionId":"sess","isSidechain":true,"timestamp":"2026-09-08T00:49:05.500Z","attachment":{"type":"hook_success","hookName":"PreToolUse:Bash","toolUseID":"toolu_9","hookEvent":"PreToolUse","stdout":"{\"hookSpecificOutput\":{\"hookEventName\":\"PreToolUse\",\"permissionDecision\":\"ask\",\"permissionDecisionReason\":\"inline sidechain copy\"}}","stderr":"","exitCode":0}}
{"type":"user","uuid":"r1","sessionId":"sess","timestamp":"2026-09-08T00:49:06.000Z","toolUseResult":"x","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","is_error":true,"content":"This command was denied by the Claude Code auto mode classifier.\nReason: the target directory 3c2d6f1e-0000-4000-8000-000000000001 is outside the workspace at /Users/steve/code/pencil-war"}]}}
{"type":"user","uuid":"r2","sessionId":"sess","timestamp":"2026-09-08T00:49:07.000Z","toolUseResult":"x","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_3","is_error":true,"content":[{"type":"text","text":"The user doesn't want to proceed with this tool use. The tool use was rejected (eg. if it was a file edit, the new_string was NOT written to the file). STOP what you are doing and wait for the user to tell you how to proceed."}]},{"type":"text","text":"[Request interrupted by user for tool use]"}]}}
{"type":"user","uuid":"r3","sessionId":"sess","timestamp":"2026-09-08T00:49:08.000Z","toolUseResult":"x","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_2","is_error":true,"content":"ripgrep not found on PATH. Install it (brew install ripgrep / apt install ripgrep) and retry, pid 31337, cwd /Users/steve/code/pencil-war"}]}}
{"type":"user","uuid":"u2","sessionId":"sess","timestamp":"2026-09-08T00:49:09.000Z","message":{"role":"user","content":[{"type":"text","text":"[Request interrupted by user]"}]}}
{"type":"user","uuid":"u3","sessionId":"sess","isMeta":true,"timestamp":"2026-09-08T00:49:10.000Z","message":{"role":"user","content":[{"type":"text","text":"[Request interrupted by user]"}]}}
`

type frictionKey struct{ kind, signature, tool string }

func TestFrictionEventsAreClassified(t *testing.T) {
	s, err := Parse(strings.NewReader(frictionSession))
	if err != nil {
		t.Fatal(err)
	}
	var got []frictionKey
	for _, f := range s.Friction {
		got = append(got, frictionKey{f.Kind, f.Signature, f.Tool})
		if f.AgentType != "" {
			t.Errorf("%+v: AgentType = %q, want empty on the parent's own events", f, f.AgentType)
		}
		if f.Detail == "" || f.Time.IsZero() {
			t.Errorf("%+v: Detail and Time must both be set", f)
		}
	}
	want := []frictionKey{
		{friction.KindHookAsk, "PreToolUse:Bash: rm-gate: at least one target comes from a variable or a substitution, so it cannot be resolved before the command runs", "Bash"},
		{friction.KindHookDeny, "PreToolUse:Bash: rm-gate: refusing rm on <path>", "Bash"},
		{friction.KindHookDeny, "PreToolUse:Edit: edit-guard: generated file, edit the source in warp (pid <pid>)", "Edit"},
		{friction.KindClassifierDenied, "the target directory <uuid> is outside the workspace at <path>", "Bash"},
		{friction.KindDeniedByUser, "Read", "Read"},
		{friction.KindUserInterrupt, "[Request interrupted by user for tool use]", ""},
		{friction.KindToolError, "Grep: ripgrep not found on PATH. Install it (brew install ripgrep / apt install ripgrep) and retry, pid <pid>, cwd <path>", "Grep"},
		{friction.KindUserInterrupt, "[Request interrupted by user]", ""},
	}
	if len(got) != len(want) {
		t.Fatalf("Friction:\n got %d %+v\nwant %d %+v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Friction[%d]:\n got %+v\nwant %+v", i, got[i], want[i])
		}
	}
	// The existing error stream is untouched by the new one.
	if len(s.Errors) != 3 {
		t.Errorf("Errors = %d, want 3 tool_error rows as before", len(s.Errors))
	}
}

// subagentFrictionTranscript is a sidechain whose Grep failed the way the
// spawn path does: every record is isSidechain, and the failure is the
// transcript's own.
const subagentFrictionTranscript = `{"type":"user","uuid":"s1","sessionId":"sess","isSidechain":true,"promptId":"sp1","timestamp":"2026-09-01T10:00:02.000Z","message":{"role":"user","content":"review"}}
{"type":"assistant","uuid":"s2","sessionId":"sess","isSidechain":true,"timestamp":"2026-09-01T10:00:03.000Z","message":{"role":"assistant","model":"claude-haiku-4-5","content":[{"type":"tool_use","id":"toolu_S","name":"Grep","input":{"pattern":"x"}}],"stop_reason":"tool_use"}}
{"type":"user","uuid":"s3","sessionId":"sess","isSidechain":true,"timestamp":"2026-09-01T10:00:04.000Z","toolUseResult":"x","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_S","is_error":true,"content":"ripgrep not found on PATH. Install it (brew install ripgrep)"}]}}
{"type":"attachment","uuid":"s4","sessionId":"sess","isSidechain":true,"timestamp":"2026-09-01T10:00:05.000Z","attachment":{"type":"hook_success","hookName":"PreToolUse:Bash","stdout":"{\"hookSpecificOutput\":{\"permissionDecision\":\"ask\",\"permissionDecisionReason\":\"rm-gate: variable target\"}}","exitCode":0}}
`

func TestSubagentFrictionLandsOnParent(t *testing.T) {
	s, err := ParseWithSubagents(strings.NewReader(dispatchingSession), []SubagentInput{
		{AgentType: "reviewer", ToolUseID: "toolu_A", Open: openString(subagentFrictionTranscript)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Friction) != 2 {
		t.Fatalf("Friction = %+v, want the subagent's tool error and hook ask", s.Friction)
	}
	for _, f := range s.Friction {
		if f.AgentType != "reviewer" || f.TurnIdx != 0 {
			t.Errorf("%+v: want AgentType reviewer on the dispatching turn 0", f)
		}
	}
	if s.Friction[0].Kind != friction.KindToolError || s.Friction[0].Signature != "Grep: ripgrep not found on PATH. Install it (brew install ripgrep)" {
		t.Errorf("Friction[0] = %+v", s.Friction[0])
	}
	if s.Friction[1].Kind != friction.KindHookAsk || s.Friction[1].Tool != "Bash" {
		t.Errorf("Friction[1] = %+v", s.Friction[1])
	}
}

// TestRmGateFixtureCollapsesToOneSignature generates the 96 rm-gate asks the
// ticket was written from — spread over 4 days, 5 repos and many sessions,
// every id and duration distinct — and pins that they group as one.
func TestRmGateFixtureCollapsesToOneSignature(t *testing.T) {
	const reason = "rm-gate: at least one target comes from a variable or a substitution, so it cannot be resolved before the command runs"
	repos := []string{"shuttle", "ticket", "pencil-war", "warp", "weft"}
	stdout, _ := json.Marshal(map[string]any{"hookSpecificOutput": map[string]string{
		"hookEventName": "PreToolUse", "permissionDecision": "ask", "permissionDecisionReason": reason,
	}})
	var b strings.Builder
	for i := 0; i < 96; i++ {
		rec := map[string]any{
			"type":      "attachment",
			"uuid":      fmt.Sprintf("5714e881-b7c5-4d46-acfa-%012d", i),
			"sessionId": fmt.Sprintf("1230a905-bd8e-4808-8057-%012d", i/3),
			"cwd":       "/Users/steve/code/" + repos[i%len(repos)],
			"version":   fmt.Sprintf("2.1.%d", 80+i%12),
			"timestamp": fmt.Sprintf("2026-09-%02dT%02d:%02d:49.2Z", 5+i%4, i%24, i%60),
			"attachment": map[string]any{
				"type": "hook_success", "hookName": "PreToolUse:Bash", "hookEvent": "PreToolUse",
				"toolUseID": fmt.Sprintf("toolu_%024d", i), "stdout": string(stdout), "stderr": "",
				"exitCode": 0, "command": "~/.claude/rm-gate.sh", "durationMs": 20 + i,
			},
		}
		line, err := json.Marshal(rec)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	s, err := Parse(strings.NewReader(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Friction) != 96 {
		t.Fatalf("Friction = %d events, want 96", len(s.Friction))
	}
	distinct := map[string]bool{}
	for _, f := range s.Friction {
		distinct[f.Kind+"\x00"+f.Signature] = true
	}
	if len(distinct) != 1 {
		t.Fatalf("distinct signatures = %d, want 1: %v", len(distinct), distinct)
	}
	if want := "PreToolUse:Bash: " + reason; s.Friction[0].Signature != want {
		t.Errorf("signature = %q, want %q", s.Friction[0].Signature, want)
	}
}
