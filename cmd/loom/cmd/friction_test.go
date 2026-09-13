package cmd

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loom/internal/parse/summary"
	"loom/internal/summaries"
)

// seedFrictionFixture writes one session carrying two rm-gate asks on
// different days and one ripgrep tool error, and returns the DB path.
func seedFrictionFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "summaries.db")
	st, err := summaries.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	at := time.Date(2026, 9, 7, 23, 0, 0, 0, time.UTC)
	sum := &summary.SessionSummary{
		SessionID: "1230a905-bd8e-4808-8057-38855c3e3434",
		Agent:     summary.AgentClaude,
		Cwd:       "/Users/steve/code/warp",
		StartTime: at,
		EndTime:   at.Add(2 * time.Hour),
		Friction: []summary.FrictionEvent{
			{Time: at, Kind: "hook.ask", Signature: "PreToolUse:Bash: rm-gate: variable target", Tool: "Bash"},
			{Time: at.Add(90 * time.Minute), Kind: "hook.ask", Signature: "PreToolUse:Bash: rm-gate: variable target", Tool: "Bash"},
			{Time: at.Add(time.Minute), Kind: "tool.error", Signature: "Grep: ripgrep not found on PATH", Tool: "Grep", AgentType: "reviewer"},
		},
	}
	if err := st.WriteSummary(context.Background(), sum, summaries.SourceInfo{Project: "warp"}); err != nil {
		t.Fatal(err)
	}
	return path
}

func runFriction(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newFrictionCmd()
	cmd.SetArgs(args)
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	err := cmd.Execute()
	return out.String(), err
}

func TestFrictionRanksAndNamesSessions(t *testing.T) {
	db := seedFrictionFixture(t)

	out, err := runFriction(t, "--db", db)
	if err != nil {
		t.Fatalf("friction: %v\n%s", err, out)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("output = %d lines, want header plus two rows:\n%s", len(lines), out)
	}
	// Two events over two UTC days is 1.0/day, the same as one over one; the
	// larger count sorts first.
	if !strings.Contains(lines[1], "rm-gate: variable target") || !strings.Contains(lines[1], "hook.ask") {
		t.Errorf("row 1 is not rm-gate:\n%s", out)
	}
	if !strings.Contains(lines[1], " 2 ") || !strings.Contains(lines[1], "2026-09-07") || !strings.Contains(lines[1], "2026-09-08") {
		t.Errorf("row 1 lacks the event count or dates:\n%s", out)
	}
	if !strings.Contains(lines[2], "ripgrep not found on PATH") || !strings.Contains(lines[2], "tool.error") {
		t.Errorf("row 2 is not ripgrep:\n%s", out)
	}
	if strings.Contains(out, "1230a905") {
		t.Errorf("session id printed without --sessions:\n%s", out)
	}

	out, err = runFriction(t, "--db", db, "--sessions", "--top", "1")
	if err != nil {
		t.Fatalf("friction --sessions: %v\n%s", err, out)
	}
	if !strings.Contains(out, "1230a905-bd8e-4808-8057-38855c3e3434") || strings.Contains(out, "ripgrep") {
		t.Errorf("--sessions --top 1 output:\n%s", out)
	}
}

func TestFrictionRefusesMissingDB(t *testing.T) {
	_, err := runFriction(t, "--db", filepath.Join(t.TempDir(), "none.db"))
	if err == nil || !strings.Contains(err.Error(), "loom summarize") {
		t.Fatalf("err = %v, want the summarize hint", err)
	}
}

func TestFrictionSinceBound(t *testing.T) {
	db := seedFrictionFixture(t)

	// Only the second rm-gate ask, at 00:30 UTC on the 8th, is at or after
	// the bound; the RFC3339 form keeps the test independent of local time.
	out, err := runFriction(t, "--db", db, "--since", "2026-09-08T00:00:00Z")
	if err != nil {
		t.Fatalf("friction --since: %v\n%s", err, out)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("output = %d lines, want header plus one row:\n%s", len(lines), out)
	}
	if !strings.Contains(lines[1], "rm-gate: variable target") || !strings.Contains(lines[1], " 1 ") || strings.Contains(out, "2026-09-07") {
		t.Errorf("row is not the single later rm-gate event:\n%s", out)
	}

	_, err = runFriction(t, "--db", db, "--since", "336h")
	if err == nil || !strings.Contains(err.Error(), "--since") {
		t.Fatalf("err = %v, want the --since parse error", err)
	}
}
