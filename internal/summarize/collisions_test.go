package summarize

import (
	"bytes"
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loom/internal/summaries"
)

const extraRecord = `{"type":"assistant","uuid":"v4","sessionId":"sess-plain","timestamp":"2026-09-01T11:00:03.000Z","message":{"role":"assistant","content":[{"type":"text","text":"read it"}],"stop_reason":"end_turn"}}` + "\n"

// collisionTree holds the two shapes that resolve two files to one
// (agent, session_id) row. Each pair sorts the way the live pairs did, so the
// second file of a pair holds the row when a sweep ends.
type collisionTree struct {
	received string
	// The same filename under two project directories, sizes differing.
	firstCopy, secondCopy string
	// A file whose records carry carrier's session id, and carrier itself,
	// which has a subagent transcript.
	alias, carrier, transcript string
}

func writeTreeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func appendToFile(t *testing.T, path, content string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, content...), 0o644); err != nil {
		t.Fatal(err)
	}
}

func buildCollisionTree(t *testing.T) collisionTree {
	t.Helper()
	received := t.TempDir()
	claude := filepath.Join(received, "claude-code")
	warp := filepath.Join(claude, "-Users-steve-code-warp")
	tree := collisionTree{
		received:   received,
		firstCopy:  filepath.Join(claude, "-Users-smacbeth-code-forge", "sess-plain.jsonl"),
		secondCopy: filepath.Join(claude, "-Users-steve-code-forge", "sess-plain.jsonl"),
		alias:      filepath.Join(warp, "sess-alias.jsonl"),
		carrier:    filepath.Join(warp, "sess-parent.jsonl"),
		transcript: filepath.Join(warp, "sess-parent", "subagents", "agent-aaa.jsonl"),
	}
	writeTreeFile(t, tree.firstCopy, plainSession)
	writeTreeFile(t, tree.secondCopy, plainSession+extraRecord)
	writeTreeFile(t, tree.alias, strings.ReplaceAll(plainSession, "sess-plain", "sess-parent"))
	writeTreeFile(t, tree.carrier, parentSession)
	writeTreeFile(t, tree.transcript, reviewerSidechain)
	writeTreeFile(t, strings.TrimSuffix(tree.transcript, ".jsonl")+".subagent.json",
		`{"parent_session_id":"sess-parent","agent_type":"reviewer","description":"Quality lens round 2","tool_use_id":"toolu_A","spawn_depth":1}`)
	return tree
}

func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var logs bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(prev) })
	return &logs
}

// foldStamps is every session row's fold time: a re-fold moves it even when
// the fold writes identical content.
func foldStamps(t *testing.T, st *summaries.Store) string {
	t.Helper()
	var stamps string
	if err := st.DB().QueryRow(`
		SELECT group_concat(session_id || '=' || summarized_at, ' ')
		FROM (SELECT session_id, summarized_at FROM sessions ORDER BY session_id)`).Scan(&stamps); err != nil {
		t.Fatalf("read fold stamps: %v", err)
	}
	return stamps
}

func rowSource(t *testing.T, st *summaries.Store, sessionID string) (string, int64) {
	t.Helper()
	var path string
	var size int64
	if err := st.DB().QueryRow(`
		SELECT source_path, source_size FROM sessions
		WHERE agent = 'claude-code' AND session_id = ?`, sessionID).Scan(&path, &size); err != nil {
		t.Fatalf("row for %s: %v", sessionID, err)
	}
	return path, size
}

// Two files on one row each overwrite the source_size and source_mtime the
// other is compared against, so without a per-file record neither is ever
// current and every sweep re-folds both, subagent children included. Once each
// has folded the pair has to settle, and still react to either file changing.
func TestCollidingSourcesSettleAndStillFoldChanges(t *testing.T) {
	tree := buildCollisionTree(t)
	logs := captureLog(t)
	st, err := summaries.Open(filepath.Join(t.TempDir(), "summaries.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()

	// foldsOnce sweeps, requiring want folds, then sweeps again requiring
	// none: a change is folded exactly once and the tree is quiet after it.
	foldsOnce := func(step string, force bool, want int) {
		t.Helper()
		if r := sweep(ctx, st, tree.received, force, false); r.parsed != want || r.errored != 0 {
			t.Fatalf("%s: sweep = %+v, want parsed=%d", step, r, want)
		}
		stamps := foldStamps(t, st)
		for range 3 {
			logs.Reset()
			if r := sweep(ctx, st, tree.received, false, false); r.parsed != 0 || r.skipped != 4 || r.errored != 0 {
				t.Fatalf("%s: repeat sweep = %+v, want parsed=0 skipped=4", step, r)
			}
			if got := logs.String(); got != "" {
				t.Errorf("%s: repeat sweep logged %q, want nothing", step, got)
			}
		}
		if got := foldStamps(t, st); got != stamps {
			t.Errorf("%s: repeat sweeps rewrote rows:\n before %s\n after  %s", step, stamps, got)
		}
	}
	assertRow := func(step, sessionID, wantPath string) {
		t.Helper()
		info, err := os.Stat(wantPath)
		if err != nil {
			t.Fatal(err)
		}
		if path, size := rowSource(t, st, sessionID); path != wantPath || size != info.Size() {
			t.Errorf("%s: %s row = %s size %d, want %s size %d", step, sessionID, path, size, wantPath, info.Size())
		}
	}
	assertCollisionLogged := func(step, folded, replaced, logged string) {
		t.Helper()
		for _, line := range strings.Split(logged, "\n") {
			if strings.Contains(line, "collision") && strings.Contains(line, folded) && strings.Contains(line, replaced) {
				return
			}
		}
		t.Errorf("%s: no line naming %s and %s in:\n%s", step, folded, replaced, logged)
	}
	sweepLogged := func(step string, want int) string {
		t.Helper()
		logs.Reset()
		if r := sweep(ctx, st, tree.received, false, false); r.parsed != want || r.errored != 0 {
			t.Fatalf("%s: sweep = %+v, want parsed=%d", step, r, want)
		}
		return logs.String()
	}

	first := sweepLogged("cold start", 4)
	assertCollisionLogged("cold start", tree.secondCopy, tree.firstCopy, first)
	assertCollisionLogged("cold start", tree.carrier, tree.alias, first)
	assertRow("cold start", "sess-plain", tree.secondCopy)
	assertRow("cold start", "sess-parent", tree.carrier)
	assertRow("cold start", "agent-aaa", tree.transcript)
	foldsOnce("settled", false, 0)

	// Neither file of a pair holds a special place: the copy that lost the row
	// and the file named for another session both fold when they change.
	appendToFile(t, tree.firstCopy, extraRecord+extraRecord)
	changed := sweepLogged("same filename", 1)
	assertCollisionLogged("same filename", tree.firstCopy, tree.secondCopy, changed)
	assertRow("same filename", "sess-plain", tree.firstCopy)
	foldsOnce("same filename settled", false, 0)

	appendToFile(t, tree.alias, strings.ReplaceAll(extraRecord, "sess-plain", "sess-parent"))
	changed = sweepLogged("foreign session id", 1)
	assertCollisionLogged("foreign session id", tree.alias, tree.carrier, changed)
	assertRow("foreign session id", "sess-parent", tree.alias)
	foldsOnce("foreign session id settled", false, 0)

	// A newer transcript leaves the carrier's own size and mtime alone; the
	// memo has to compare the effective mtime or this change is lost.
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(tree.transcript, later, later); err != nil {
		t.Fatal(err)
	}
	changed = sweepLogged("subagent transcript", 1)
	assertCollisionLogged("subagent transcript", tree.carrier, tree.alias, changed)
	assertRow("subagent transcript", "sess-parent", tree.carrier)
	foldsOnce("subagent transcript settled", false, 0)

	foldsOnce("force", true, 4)
}

// A file that failed to fold has to be tried again even though nothing about
// it changed. The symlink stands in for one: the sweep stats the link, so the
// target appearing leaves the size and mtime it compares untouched.
func TestFailedFoldIsRetriedOnTheNextSweep(t *testing.T) {
	received := strictTree(t, true)
	project := filepath.Join(received, "claude-code", "loom")
	st, err := summaries.Open(filepath.Join(t.TempDir(), "summaries.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()

	if r := sweep(ctx, st, received, false, false); r.parsed != 1 || r.errored != 1 {
		t.Fatalf("dangling link: sweep = %+v, want parsed=1 errored=1", r)
	}
	writeTreeFile(t, filepath.Join(project, "missing"), strings.ReplaceAll(plainSession, "sess-plain", "sess-gone"))
	if r := sweep(ctx, st, received, false, false); r.parsed != 1 || r.errored != 0 {
		t.Fatalf("after the target appeared: sweep = %+v, want parsed=1 errored=0", r)
	}
	if path, _ := rowSource(t, st, "sess-gone"); path != filepath.Join(project, "sess-gone.jsonl") {
		t.Errorf("sess-gone row source = %s", path)
	}
}

// The ticks of one --watch run share what the first sweep folded: the last
// fold stays at that sweep while the sweep marker moves on past it.
func TestWatchTicksLeaveCollidingRowsAlone(t *testing.T) {
	tree := buildCollisionTree(t)
	dbPath := filepath.Join(t.TempDir(), "summaries.db")
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if err := run(ctx, Options{ReceivedDir: tree.received, DBPath: dbPath, Watch: true, Interval: 10 * time.Millisecond}); err != nil {
		t.Fatalf("run: %v", err)
	}

	st, err := summaries.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var stamp string
	if err := st.DB().QueryRow(`SELECT MAX(summarized_at) FROM sessions`).Scan(&stamp); err != nil {
		t.Fatal(err)
	}
	lastFold, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil {
		t.Fatal(err)
	}
	lastSweep, ok, err := summaries.LastSweep(st.DB())
	if err != nil || !ok {
		t.Fatalf("LastSweep = %v, %v", ok, err)
	}
	if gap := lastSweep.Sub(lastFold); gap < 200*time.Millisecond {
		t.Errorf("last fold %s before the last sweep, want the ticks after the first sweep to fold nothing", gap)
	}
}
