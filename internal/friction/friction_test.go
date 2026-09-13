package friction

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	parsefriction "loom/internal/parse/friction"
	"loom/internal/parse/summary"
	"loom/internal/summaries"
)

// TestRank pins the ranking key and the fields behind it: a spike of few
// days outranks a chronic signature with more events, days are distinct UTC
// dates, and Sessions names every id once, sorted.
func TestRank(t *testing.T) {
	day := func(d int, h int) time.Time {
		return time.Date(2026, 9, d, h, 0, 0, 0, time.UTC)
	}
	var events []Event
	// spike: 8 events over 2 days from 2 sessions.
	for i := 0; i < 8; i++ {
		events = append(events, Event{
			SessionID: []string{"s-b", "s-a"}[i%2], Kind: parsefriction.KindHookAsk,
			Signature: "PreToolUse:Bash: rm-gate", Time: day(7+i%2, i),
		})
	}
	// chronic: 10 events over 5 days from 1 session.
	for i := 0; i < 10; i++ {
		events = append(events, Event{
			SessionID: "s-c", Kind: parsefriction.KindToolError,
			Signature: "Grep: ripgrep not found on PATH", Time: day(1+i%5, 12),
		})
	}
	// one-off with the same per-day as chronic; fewer events sorts after.
	events = append(events, Event{SessionID: "s-d", Kind: parsefriction.KindUserInterrupt,
		Signature: "[Request interrupted by user]", Time: day(3, 1)})
	// a local-time stamp on the far side of midnight UTC is another day.
	events = append(events, Event{SessionID: "s-d", Kind: parsefriction.KindUserInterrupt,
		Signature: "[Request interrupted by user]",
		Time:      time.Date(2026, 9, 3, 20, 0, 0, 0, time.FixedZone("PDT", -7*3600)),
	})

	rows := Rank(events)
	if len(rows) != 3 {
		t.Fatalf("Rank rows = %d, want 3", len(rows))
	}
	spike, chronic, oneoff := rows[0], rows[1], rows[2]
	if spike.Signature != "PreToolUse:Bash: rm-gate" || spike.Events != 8 || spike.ActiveDays != 2 || spike.PerActiveDay() != 4 {
		t.Errorf("row 0 = %+v, want the 8-event 2-day spike", spike)
	}
	if !spike.FirstSeen.Equal(day(7, 0)) || !spike.LastSeen.Equal(day(8, 7)) {
		t.Errorf("spike first/last = %s/%s, want %s/%s", spike.FirstSeen, spike.LastSeen, day(7, 0), day(8, 7))
	}
	if !reflect.DeepEqual(spike.Sessions, []string{"s-a", "s-b"}) {
		t.Errorf("spike Sessions = %v, want [s-a s-b]", spike.Sessions)
	}
	if chronic.Signature != "Grep: ripgrep not found on PATH" || chronic.Events != 10 || chronic.ActiveDays != 5 {
		t.Errorf("row 1 = %+v, want the 10-event 5-day chronic signature", chronic)
	}
	if !reflect.DeepEqual(chronic.Sessions, []string{"s-c"}) {
		t.Errorf("chronic Sessions = %v, want [s-c]", chronic.Sessions)
	}
	if oneoff.Kind != parsefriction.KindUserInterrupt || oneoff.Events != 2 || oneoff.ActiveDays != 2 {
		t.Errorf("row 2 = %+v, want the 2-event 2-day interrupt", oneoff)
	}
	if !oneoff.LastSeen.Equal(time.Date(2026, 9, 4, 3, 0, 0, 0, time.UTC)) {
		t.Errorf("oneoff LastSeen = %s, want the UTC instant", oneoff.LastSeen)
	}
}

func TestRenderListsSessionsOnRequest(t *testing.T) {
	rows := Rank([]Event{
		{SessionID: "sess-1", Kind: parsefriction.KindHookAsk, Signature: "PreToolUse:Bash: rm-gate", Time: time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC)},
		{SessionID: "sess-2", Kind: parsefriction.KindHookAsk, Signature: "PreToolUse:Bash: rm-gate", Time: time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)},
		{SessionID: "sess-1", Kind: parsefriction.KindToolError, Signature: "Grep: ripgrep not found", Time: time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)},
	})
	var out bytes.Buffer
	Render(&out, rows, 0, false)
	if strings.Contains(out.String(), "sess-1") {
		t.Errorf("Render without --sessions names a session:\n%s", out.String())
	}
	out.Reset()
	Render(&out, rows, 1, true)
	got := out.String()
	if !strings.Contains(got, "sess-1, sess-2") || strings.Contains(got, "ripgrep") {
		t.Errorf("Render top=1 with sessions:\n%s", got)
	}
	if !strings.Contains(got, "2026-09-08") || !strings.Contains(got, "2026-09-09") {
		t.Errorf("Render dates:\n%s", got)
	}
}

// TestRenderNeutralizesTerminalControls passes an OSC 52 clipboard write and
// a CSI erase through normalization and rendering, in the signature, the
// kind and a session id, and checks no control byte reaches the writer.
func TestRenderNeutralizesTerminalControls(t *testing.T) {
	osc := "Bash: failed \x1b]52;c;aGVsbG8=\x07 see log"
	csi := "\x1b[2J\x1b[H\u009bHWiped"
	rows := Rank([]Event{
		{SessionID: "sess-\x1b[31m1", Kind: parsefriction.KindToolError, Signature: parsefriction.Normalize(osc)},
		{SessionID: "sess-2", Kind: "tool.\x1b[2Kerror", Signature: parsefriction.Normalize(csi)},
	})
	var out bytes.Buffer
	Render(&out, rows, 0, true)
	got := out.String()
	for _, r := range got {
		if r < 0x20 && r != '\n' || r >= 0x7f && r <= 0x9f {
			t.Fatalf("Render emitted control %U:\n%q", r, got)
		}
	}
	for _, want := range []string{`\x1b]52;c;aGVsbG8=\x07`, `\x1b[2J`, `\x9bH`, `sess-\x1b[31m1`, `tool.\x1b[2Kerror`} {
		if !strings.Contains(got, want) {
			t.Errorf("Render lacks the escaped form %q:\n%s", want, got)
		}
	}
}

// seedFixture writes one session carrying two rm-gate asks and one ripgrep
// tool error, and returns the DB path.
func seedFixture(t *testing.T) string {
	t.Helper()
	return seedFixtureAt(t, filepath.Join(t.TempDir(), "summaries.db"))
}

func seedFixtureAt(t *testing.T, path string) string {
	t.Helper()
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
			{Time: at, Kind: parsefriction.KindHookAsk, Signature: "PreToolUse:Bash: rm-gate: variable target", Tool: "Bash"},
			{Time: at.Add(90 * time.Minute), Kind: parsefriction.KindHookAsk, Signature: "PreToolUse:Bash: rm-gate: variable target", Tool: "Bash"},
			{Time: at.Add(time.Minute), Kind: parsefriction.KindToolError, Signature: "Grep: ripgrep not found on PATH", Tool: "Grep"},
		},
	}
	if err := st.WriteSummary(context.Background(), sum, summaries.SourceInfo{Project: "warp"}); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadReadsRowsAndHonoursSince(t *testing.T) {
	path := seedFixture(t)
	events, err := Load(path, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("Load = %d events, want 3: %+v", len(events), events)
	}
	if events[0].Kind != parsefriction.KindHookAsk || events[0].SessionID != "1230a905-bd8e-4808-8057-38855c3e3434" || events[0].Time.IsZero() {
		t.Errorf("Load first event = %+v", events[0])
	}
	events, err = Load(path, time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Kind != parsefriction.KindHookAsk {
		t.Errorf("Load since midnight = %+v, want the one later ask", events)
	}
}

// TestLoadEscapesURIDelimiters pins that a path carrying '#' or '?' is read
// as the file it names: no query is lost and no database appears at the
// prefix a file: URI would otherwise cut the path down to.
func TestLoadEscapesURIDelimiters(t *testing.T) {
	dir := t.TempDir()
	path := seedFixtureAt(t, filepath.Join(dir, "friction#a?b.db"))
	events, err := Load(path, time.Time{})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("Load = %d events, want 3: %+v", len(events), events)
	}
	truncated := filepath.Join(dir, "friction")
	if _, err := os.Stat(truncated); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stat %s = %v, want not exist", truncated, err)
	}
}

func TestLoadRefusesMissingDB(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "none.db"), time.Time{})
	if err == nil || !strings.Contains(err.Error(), "loom summarize") {
		t.Fatalf("err = %v, want the summarize hint", err)
	}
}

// TestLoadRefusesOutdatedSchema turns the fixture into the v9 shape every
// pre-friction database has — no table, schema marker at 9 — and expects the
// rebuild hint rather than a missing-table error.
func TestLoadRefusesOutdatedSchema(t *testing.T) {
	path := seedFixture(t)
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`DROP TABLE friction`,
		`UPDATE schema_meta SET value = '9' WHERE key = 'schema_version'`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()

	_, err = Load(path, time.Time{})
	if err == nil || !strings.Contains(err.Error(), "--rebuild") || !strings.Contains(err.Error(), "schema 9") {
		t.Fatalf("err = %v, want the rebuild hint naming schema 9", err)
	}
}
