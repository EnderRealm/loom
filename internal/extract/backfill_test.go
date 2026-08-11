package extract

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"loom/internal/parse/summary"
)

const (
	loomRemote  = "https://github.com/EnderRealm/loom.git"
	forgeRemote = "https://github.com/EnderRealm/forge.git"
)

// historical pushes the watermark ahead of every session the test adds, so the
// fixture is exactly the backlog sweep refuses to touch.
func (e *env) historical() {
	e.t.Helper()
	e.setWatermark(time.Now().Add(time.Hour))
}

// snapshot hashes every file under dir, so a dry run can be shown to have left
// the knowledge store and the ledger byte-identical.
func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		out[path] = hex.EncodeToString(sum[:])
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestBackfillReachesSessionsBeforeTheWatermark(t *testing.T) {
	e := newEnv(t, "loom")
	e.historical()
	input := e.addSession("historical", loomRemote)

	// The same fixture is invisible to the forward trigger — which is the
	// whole reason the backfill exists.
	sweep(context.Background(), Options{})
	if len(e.runs) != 0 {
		t.Fatalf("runs = %v, want none from a sweep (the session predates the watermark)", e.runs)
	}

	backfill(context.Background(), Options{Backfill: true})

	want := "loom " + input
	if len(e.runs) != 1 || e.runs[0] != want {
		t.Fatalf("runs = %v, want exactly [%q]", e.runs, want)
	}
}

func TestBackfillDryRunSpendsNothing(t *testing.T) {
	e := newEnv(t, "loom")
	e.historical()
	e.addSession("s1", loomRemote)

	knowledgeBefore := snapshot(t, knowledgeRoot())
	ledgerBefore, err := os.ReadFile(statePath())
	if err != nil {
		t.Fatal(err)
	}

	backfill(context.Background(), Options{Backfill: true, DryRun: true})

	if len(e.runs) != 0 {
		t.Fatalf("runs = %v, want none from a dry run", e.runs)
	}
	if after := snapshot(t, knowledgeRoot()); !reflect.DeepEqual(knowledgeBefore, after) {
		t.Fatalf("dry run wrote to the knowledge store:\nbefore=%v\nafter=%v", knowledgeBefore, after)
	}
	ledgerAfter, err := os.ReadFile(statePath())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(ledgerBefore, ledgerAfter) {
		t.Fatalf("dry run changed the ledger:\nbefore=%s\nafter=%s", ledgerBefore, ledgerAfter)
	}
	if !strings.Contains(e.logs.String(), "backfill dry run: scanned 1/1 sessions, 1 to extract (loom=1)") {
		t.Fatalf("log missing the dry run report:\n%s", e.logs.String())
	}
}

// The dry run must not stamp a watermark either: doing so on a host where the
// trigger has never run would silently decide what the trigger later sweeps.
func TestBackfillDryRunDoesNotCreateTheLedger(t *testing.T) {
	e := newEnv(t, "loom")
	e.addSession("s1", loomRemote)
	if err := os.Remove(statePath()); err != nil {
		t.Fatal(err)
	}

	backfill(context.Background(), Options{Backfill: true, DryRun: true})

	if _, err := os.Stat(statePath()); !os.IsNotExist(err) {
		t.Fatalf("stat %s = %v, want the ledger to still be absent", statePath(), err)
	}
	if len(e.runs) != 0 {
		t.Fatalf("runs = %v, want none from a dry run", e.runs)
	}
}

func TestBackfillDryRunReportsEveryExclusion(t *testing.T) {
	e := newEnv(t, "loom", "forge")
	e.historical()
	e.addSession("l1", loomRemote)
	e.addSession("l2", loomRemote)
	e.addSession("f1", forgeRemote)
	e.addSession("ghost", "https://github.com/EnderRealm/ghostwheel.git")
	e.addSession("bare", "")
	e.addSessionAs(summary.AgentCodex, "rollout", loomRemote)

	st, err := loadState()
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	st.mark("claude-code", "l2", record{Outcome: outcomeExtracted, Scope: "loom"})

	backfill(context.Background(), Options{Backfill: true, DryRun: true})

	logs := e.logs.String()
	for _, want := range []string{
		"backfill dry run: scanned 6/6 sessions, 2 to extract (forge=1, loom=1)",
		"backfill dry run: 4 excluded (already visited=1, no git remote=1, unknown scope=1, unsupported agent=1)",
	} {
		if !strings.Contains(logs, want) {
			t.Fatalf("log missing %q; got:\n%s", want, logs)
		}
	}
	// Reported, never recorded: creating truths/ghostwheel/ later must be able
	// to rescue the sessions it was created for.
	reloaded, err := loadState()
	if err != nil {
		t.Fatalf("reload state: %v", err)
	}
	if len(reloaded.Sessions) != 1 {
		t.Fatalf("ledger = %v, want only the pre-seeded record", reloaded.Sessions)
	}
}

func TestBackfillRestrictsToRequestedScopes(t *testing.T) {
	e := newEnv(t, "loom", "forge")
	e.historical()
	input := e.addSession("l1", loomRemote)
	e.addSession("f1", forgeRemote)

	backfill(context.Background(), Options{Backfill: true, Scopes: []string{"loom"}})

	want := "loom " + input
	if len(e.runs) != 1 || e.runs[0] != want {
		t.Fatalf("runs = %v, want exactly [%q]", e.runs, want)
	}
	// The forge session stays unvisited, so a later run can still pick it up.
	st, err := loadState()
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if st.visited("claude-code", "f1") {
		t.Fatal("an out-of-scope session was recorded; a later --scope forge run would skip it")
	}
}

func TestBackfillStopsAtTheLimit(t *testing.T) {
	e := newEnv(t, "loom")
	e.historical()
	for _, id := range []string{"s1", "s2", "s3", "s4"} {
		e.addSession(id, loomRemote)
	}

	backfill(context.Background(), Options{Backfill: true, Limit: 2})
	if len(e.runs) != 2 {
		t.Fatalf("runs = %v, want 2 (the limit)", e.runs)
	}

	// And a second run resumes rather than repeating the first two.
	backfill(context.Background(), Options{Backfill: true})
	if len(e.runs) != 4 {
		t.Fatalf("runs = %v, want 4 across both runs", e.runs)
	}
	seen := map[string]bool{}
	for _, r := range e.runs {
		if seen[r] {
			t.Fatalf("runs = %v, want no session extracted twice", e.runs)
		}
		seen[r] = true
	}
}

// Pacing is the point: maxPerSweep exists to spread an unattended sweep, and a
// backfill capped at 4 would take a day to clear the backlog.
func TestBackfillIsNotCappedByMaxPerSweep(t *testing.T) {
	e := newEnv(t, "loom")
	e.historical()
	for i := 0; i < maxPerSweep+2; i++ {
		e.addSession("s"+string(rune('a'+i)), loomRemote)
	}

	backfill(context.Background(), Options{Backfill: true})

	if len(e.runs) != maxPerSweep+2 {
		t.Fatalf("runs = %v, want %d in a single pass", e.runs, maxPerSweep+2)
	}
}

// One ledger, so the two directions can't double-spend on the same session.
func TestBackfillAndSweepShareTheLedger(t *testing.T) {
	e := newEnv(t, "loom")
	e.addSession("swept", loomRemote)

	sweep(context.Background(), Options{})
	if len(e.runs) != 1 {
		t.Fatalf("runs = %v, want the sweep to have extracted one session", e.runs)
	}

	// What the trigger recorded, the backfill leaves alone.
	backfill(context.Background(), Options{Backfill: true})
	if len(e.runs) != 1 {
		t.Fatalf("runs = %v, want the backfill to skip the session the sweep recorded", e.runs)
	}

	// And the reverse: a session the backfill records is not swept again.
	e.addSession("backfilled", loomRemote)
	backfill(context.Background(), Options{Backfill: true})
	if len(e.runs) != 2 {
		t.Fatalf("runs = %v, want the backfill to have extracted the new session", e.runs)
	}
	sweep(context.Background(), Options{})
	if len(e.runs) != 2 {
		t.Fatalf("runs = %v, want the sweep to skip the session the backfill recorded", e.runs)
	}
}

// The ticket's own workflow — install the trigger, then backfill the backlog —
// puts two processes on the ledger at once: a backfill running for hours off
// one snapshot, the agent sweeping every 15 minutes off its own. A whole-file
// rewrite from either would erase what the other recorded, and an erased
// session reads as unvisited, so it is extracted again at real LLM cost.
func TestConcurrentWritersKeepEachOthersRecords(t *testing.T) {
	newEnv(t)

	// Two independently loaded snapshots, as the two processes each hold.
	backfiller, err := loadState()
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	sweeper, err := loadState()
	if err != nil {
		t.Fatalf("load state: %v", err)
	}

	const n = 20
	var wg sync.WaitGroup
	for _, w := range []struct {
		st     *state
		prefix string
	}{{backfiller, "backfilled"}, {sweeper, "swept"}} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < n; i++ {
				w.st.mark("claude-code", fmt.Sprintf("%s-%d", w.prefix, i), record{Outcome: outcomeExtracted, Scope: "loom"})
			}
		}()
	}
	wg.Wait()

	got, err := loadState()
	if err != nil {
		t.Fatalf("reload state: %v", err)
	}
	for i := 0; i < n; i++ {
		for _, prefix := range []string{"backfilled", "swept"} {
			if id := fmt.Sprintf("%s-%d", prefix, i); !got.visited("claude-code", id) {
				t.Fatalf("%s is missing from the ledger; the other writer erased it", id)
			}
		}
	}
	if len(got.Sessions) != 2*n {
		t.Fatalf("ledger holds %d records, want %d", len(got.Sessions), 2*n)
	}
	if got.Watermark.IsZero() {
		t.Fatal("the watermark was lost; the trigger would re-sweep the whole backlog")
	}
}

// planBackfill snapshots eligibility once, but a backfill is unbounded by the
// watermark, so its selection overlaps the sessions the installed trigger
// sweeps during the same hours. A session the trigger claims mid-run must be
// dropped, not extracted a second time at real LLM cost.
func TestBackfillSkipsSessionsClaimedMidRun(t *testing.T) {
	e := newEnv(t, "loom")
	e.historical()
	ids := []string{"s1", "s2", "s3"}
	for _, id := range ids {
		e.addSession(id, loomRemote)
	}

	// A second snapshot of the ledger, as the trigger's own process holds.
	trigger, err := loadState()
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	orig := runExtractor
	runExtractor = func(_ context.Context, script, input, scope string) (extractRun, error) {
		e.runs = append(e.runs, scope+" "+input)
		// While the first extraction is in flight, the trigger claims
		// everything the backfill has not reached yet.
		if len(e.runs) == 1 {
			inflight := strings.TrimSuffix(filepath.Base(input), ".jsonl")
			for _, id := range ids {
				if id != inflight {
					trigger.mark("claude-code", id, record{Outcome: outcomeExtracted, Scope: "loom"})
				}
			}
		}
		return extractRun{Candidates: 2, Score: 0.13}, nil
	}
	t.Cleanup(func() { runExtractor = orig })

	backfill(context.Background(), Options{Backfill: true})

	if len(e.runs) != 1 {
		t.Fatalf("runs = %v, want only the one in flight when the trigger claimed the rest", e.runs)
	}
	logs := e.logs.String()
	if got := strings.Count(logs, "claimed by another run since the plan was computed"); got != 2 {
		t.Fatalf("log reports %d claimed sessions, want 2:\n%s", got, logs)
	}
	if !strings.Contains(logs, "backfill done: extracted=1 failed=0 skipped=2 of 3 selected") {
		t.Fatalf("log missing the skipped tally:\n%s", logs)
	}
	st, err := loadState()
	if err != nil {
		t.Fatalf("reload state: %v", err)
	}
	if len(st.Sessions) != len(ids) {
		t.Fatalf("ledger = %v, want one record per session", st.Sessions)
	}
}

// Resolved scopes are lowercase by construction, so an unnormalized "Loom"
// would match nothing: the whole backlog would land in the "outside --scope"
// bucket while the run reported "0 to extract" and exited 0.
func TestBackfillNormalizesRequestedScopes(t *testing.T) {
	e := newEnv(t, "loom", "forge")
	e.historical()
	input := e.addSession("l1", loomRemote)
	e.addSession("f1", forgeRemote)

	if err := Run(Options{Backfill: true, Scopes: []string{" Loom "}}); err != nil {
		t.Fatalf("run: %v", err)
	}

	want := "loom " + input
	if len(e.runs) != 1 || e.runs[0] != want {
		t.Fatalf("runs = %v, want exactly [%q] — --scope Loom names the loom scope", e.runs, want)
	}
}

// An empty entry must not become a filter nothing can satisfy.
func TestBackfillIgnoresEmptyRequestedScopes(t *testing.T) {
	e := newEnv(t, "loom", "forge")
	e.historical()
	e.addSession("l1", loomRemote)
	e.addSession("f1", forgeRemote)

	if err := Run(Options{Backfill: true, Scopes: []string{""}}); err != nil {
		t.Fatalf("run: %v", err)
	}

	if len(e.runs) != 2 {
		t.Fatalf("runs = %v, want both sessions — an empty --scope restricts nothing", e.runs)
	}
}

// A scope the store can't produce can only be a typo, and a run that selects
// nothing reports "0 to extract" — indistinguishable from a cleared backlog.
func TestBackfillRejectsAnUnknownScope(t *testing.T) {
	e := newEnv(t, "loom")
	e.historical()
	e.addSession("l1", loomRemote)

	err := Run(Options{Backfill: true, Scopes: []string{"lom"}})
	if err == nil {
		t.Fatal("Run = nil, want an error naming the scope that can never match")
	}
	if !strings.Contains(err.Error(), `unknown scope "lom"`) {
		t.Fatalf("error %v does not name the unmatched scope", err)
	}
	if len(e.runs) != 0 {
		t.Fatalf("runs = %v, want none from a rejected run", e.runs)
	}
}

// Extraction is minutes per session, so a run of hundreds gets interrupted;
// restarting must resume rather than re-spend on what already completed.
func TestBackfillResumesAfterInterruption(t *testing.T) {
	e := newEnv(t, "loom")
	e.historical()
	for _, id := range []string{"s1", "s2", "s3", "s4"} {
		e.addSession(id, loomRemote)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var interrupted string
	orig := runExtractor
	runExtractor = func(_ context.Context, script, input, scope string) (extractRun, error) {
		e.runs = append(e.runs, scope+" "+input)
		if len(e.runs) == 2 {
			interrupted = scope + " " + input
			cancel()
			return extractRun{}, context.Canceled
		}
		return extractRun{Candidates: 2, Score: 0.13}, nil
	}
	backfill(ctx, Options{Backfill: true})
	runExtractor = orig

	if len(e.runs) != 2 {
		t.Fatalf("runs = %v, want the run to stop at the interrupt", e.runs)
	}
	if !strings.Contains(e.logs.String(), "interrupted") {
		t.Fatalf("log missing the interruption:\n%s", e.logs.String())
	}

	backfill(context.Background(), Options{Backfill: true})

	// The one that completed is not re-extracted; the interrupted one stayed
	// unvisited, so it is retried alongside the two never reached.
	counts := map[string]int{}
	for _, r := range e.runs {
		counts[r]++
	}
	if len(counts) != 4 {
		t.Fatalf("runs = %v, want all four sessions covered", e.runs)
	}
	for r, n := range counts {
		want := 1
		if r == interrupted {
			want = 2
		}
		if n != want {
			t.Fatalf("%q extracted %d times, want %d (runs = %v)", r, n, want, e.runs)
		}
	}
}

// The trigger's log lands in extractor.log because launchd redirects the
// agent's stdout; a backfill is always a foreground run, so it has to persist
// its own trail — an operator who tails the file during a multi-hour run that
// spends real LLM quota must see it there.
func TestBackfillPersistsItsLog(t *testing.T) {
	e := newEnv(t, "loom")
	e.historical()
	e.addSession("s1", loomRemote)

	if err := Run(Options{Backfill: true}); err != nil {
		t.Fatalf("run: %v", err)
	}

	data, err := os.ReadFile(LogPath())
	if err != nil {
		t.Fatalf("read %s: %v", LogPath(), err)
	}
	for _, want := range []string{
		"backfill: scanned 1/1 sessions, 1 to extract (loom=1)",
		"extract claude-code/s1 scope=loom",
		"backfill done: extracted=1 failed=0 skipped=0 of 1 selected",
	} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("%s missing %q; got:\n%s", LogPath(), want, data)
		}
	}
	// And still on stderr, so the run stays watchable where it was started.
	if !strings.Contains(e.logs.String(), "backfill done:") {
		t.Fatalf("the run's own output stopped reaching stderr:\n%s", e.logs.String())
	}
}

func TestBackfillLogsEachOutcome(t *testing.T) {
	e := newEnv(t, "loom")
	e.historical()
	ok := e.addSession("ok", loomRemote)
	fail := e.addSession("fail", loomRemote)

	orig := runExtractor
	runExtractor = func(ctx context.Context, script, input, scope string) (extractRun, error) {
		e.runs = append(e.runs, scope+" "+input)
		if strings.HasPrefix(filepath.Base(input), "fail") {
			return extractRun{}, errExtractorFailed
		}
		return extractRun{Candidates: 2, Score: 0.13}, nil
	}
	t.Cleanup(func() { runExtractor = orig })

	backfill(context.Background(), Options{Backfill: true})

	logs := e.logs.String()
	for _, want := range []string{
		"extract claude-code/ok scope=loom input=" + ok,
		"extract claude-code/fail scope=loom input=" + fail,
		errExtractorFailed.Error(),
		"backfill done: extracted=1 failed=1 skipped=0 of 2 selected",
	} {
		if !strings.Contains(logs, want) {
			t.Fatalf("log missing %q; got:\n%s", want, logs)
		}
	}
}
