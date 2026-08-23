package extract

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

const retroTicket = "loom/add-loom-retrospect-e222"

// newRetroEnv is newEnv with the extraction backend reported present: whether
// /opt/homebrew/bin/claude exists on the host running the tests is not what
// these cases are about, and requireBackend is exercised on its own below.
func newRetroEnv(t *testing.T, scopes ...string) *env {
	t.Helper()
	e := newEnv(t, scopes...)
	orig := statBackend
	statBackend = func(string) (os.FileInfo, error) { return nil, nil }
	t.Cleanup(func() { statBackend = orig })
	return e
}

// writeKnowledgeLog bootstraps log.md the way store init does; the extractor
// only ever appends to it.
func writeKnowledgeLog(t *testing.T) string {
	t.Helper()
	p := filepath.Join(knowledgeRoot(), "log.md")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("# Knowledge log\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRetrospectExtractsEverySessionOfTheTicketForBothTypes(t *testing.T) {
	e := newRetroEnv(t, "loom")
	first := e.addSessionWithCommits("s1", loomRemote, "["+retroTicket+"] Add the command")
	second := e.addSessionWithCommits("s2", loomRemote,
		"[loom/unrelated-0001] Something else",
		"["+retroTicket+"] Cover it with tests")

	if err := Retrospect(RetrospectOptions{TicketID: retroTicket}); err != nil {
		t.Fatalf("Retrospect: %v", err)
	}

	wantRuns := []string{"loom " + first, "loom " + first, "loom " + second, "loom " + second}
	if !reflect.DeepEqual(e.runs, wantRuns) {
		t.Fatalf("runs = %v, want %v (every session of the ticket, once per type)", e.runs, wantRuns)
	}
	wantKinds := []string{extractTypeTruth, extractTypeDecision, extractTypeTruth, extractTypeDecision}
	if !reflect.DeepEqual(e.kinds, wantKinds) {
		t.Fatalf("extract types = %v, want %v", e.kinds, wantKinds)
	}
}

func TestRetrospectReportsATicketWithNoSessions(t *testing.T) {
	e := newRetroEnv(t, "loom")
	e.addSessionWithCommits("s1", loomRemote, "[loom/unrelated-0001] Something else")

	if err := Retrospect(RetrospectOptions{TicketID: retroTicket}); err != nil {
		t.Fatalf("Retrospect = %v, want nil (a ticket with no commits is not a failure)", err)
	}
	if len(e.runs) != 0 {
		t.Fatalf("runs = %v, want none", e.runs)
	}
	if !strings.Contains(e.logs.String(), "no summarized session landed a commit marked ["+retroTicket+"]") {
		t.Fatalf("log doesn't say why nothing ran:\n%s", e.logs.String())
	}
}

// The marker is the `[<id>]` prefix of the commit subject; a subject that
// merely names the ticket is a mention, not the commit that closed it.
func TestRetrospectIgnoresAMentionWithoutTheMarker(t *testing.T) {
	e := newRetroEnv(t, "loom")
	e.addSessionWithCommits("s1", loomRemote, "Follow-up to "+retroTicket+" raised in review")

	if err := Retrospect(RetrospectOptions{TicketID: retroTicket}); err != nil {
		t.Fatalf("Retrospect: %v", err)
	}
	if len(e.runs) != 0 {
		t.Fatalf("runs = %v, want none (the subject carries no [%s] marker)", e.runs, retroTicket)
	}
}

// A tk id may legally contain `_`, which SQL LIKE reads as a single-character
// wildcard — the reason the marker is matched in Go rather than by the query.
func TestRetrospectMatchesAnUnderscoreIDLiterally(t *testing.T) {
	e := newRetroEnv(t, "loom")
	input := e.addSessionWithCommits("s1", loomRemote, "[loom/with_underscore] The real one")
	e.addSessionWithCommits("s2", loomRemote, "[loom/withXunderscore] A wildcard match")

	if err := Retrospect(RetrospectOptions{TicketID: "loom/with_underscore"}); err != nil {
		t.Fatalf("Retrospect: %v", err)
	}
	wantRuns := []string{"loom " + input, "loom " + input}
	if !reflect.DeepEqual(e.runs, wantRuns) {
		t.Fatalf("runs = %v, want %v (`_` must match itself, not any character)", e.runs, wantRuns)
	}
}

func TestRetrospectSkipsSessionsWithoutAResolvableScope(t *testing.T) {
	e := newRetroEnv(t, "loom")
	e.addSessionWithCommits("no-remote", "", "["+retroTicket+"] Landed from an unidentified checkout")

	if err := Retrospect(RetrospectOptions{TicketID: retroTicket}); err != nil {
		t.Fatalf("Retrospect = %v, want nil (a skip is not a failure)", err)
	}
	if len(e.runs) != 0 {
		t.Fatalf("runs = %v, want none (the session has nowhere to file candidates)", e.runs)
	}
	if !strings.Contains(e.logs.String(), "skip claude-code/no-remote: no git remote") {
		t.Fatalf("log missing the skip reason:\n%s", e.logs.String())
	}
}

func TestRetrospectAppendsOneLogEntryPerRun(t *testing.T) {
	e := newRetroEnv(t, "loom")
	logPath := writeKnowledgeLog(t)
	e.addSessionWithCommits("s1", loomRemote, "["+retroTicket+"] Add the command")

	if err := Retrospect(RetrospectOptions{TicketID: retroTicket}); err != nil {
		t.Fatalf("Retrospect: %v", err)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log.md: %v", err)
	}
	// Two candidates per invocation from the stub, one invocation per type.
	want := fmt.Sprintf("## [%s] retrospect %s | loom | 2 truth candidates, 2 decision candidates",
		time.Now().Format("2006-01-02"), retroTicket)
	if !strings.Contains(string(data), want) {
		t.Fatalf("log.md missing %q; got:\n%s", want, data)
	}
	if got := strings.Count(string(data), "] retrospect "); got != 1 {
		t.Fatalf("log.md has %d entries, want exactly one per run:\n%s", got, data)
	}
}

// TestRetrospectCommitsItsLogEntry: the entry is written and committed by the
// same code as every other write to the store — internal/knowledge/store — so
// the run's record reaches the store's history rather than sitting in its
// working tree. ApplyIn is what makes that possible here: the run resolves the
// store through the extractor's tunables, and the commit has to be held against
// that same root.
func TestRetrospectCommitsItsLogEntry(t *testing.T) {
	e := newRetroEnv(t, "loom")
	logPath := writeKnowledgeLog(t)
	root := initKnowledgeRepo(t)
	e.addSessionWithCommits("s1", loomRemote, "["+retroTicket+"] Add the command")

	if err := Retrospect(RetrospectOptions{TicketID: retroTicket}); err != nil {
		t.Fatalf("Retrospect: %v", err)
	}

	want := fmt.Sprintf("retrospect %s | loom | 2 truth candidates, 2 decision candidates", retroTicket)
	if subject := testGit(t, root, "log", "-1", "--format=%s"); subject != want {
		t.Fatalf("commit subject = %q, want %q", subject, want)
	}
	if names := testGit(t, root, "show", "--name-only", "--format=", "HEAD"); !strings.Contains(names, "log.md") {
		t.Fatalf("commit does not record the entry:\n%s", names)
	}
	if st := testGit(t, root, "status", "--porcelain", "--", logPath); st != "" {
		t.Fatalf("log.md still dirty after the run:\n%s", st)
	}
}

// initKnowledgeRepo makes the knowledge root a git repo with its bootstrapped
// log.md committed, as the live store is. Global git config is isolated so the
// fixture does not inherit the developer's identity, hooks or templates.
func initKnowledgeRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	root := knowledgeRoot()
	testGit(t, root, "init")
	testGit(t, root, "config", "user.email", "test@example.com")
	testGit(t, root, "config", "user.name", "loom test")
	testGit(t, root, "add", "-A")
	testGit(t, root, "commit", "-m", "seed store")
	return root
}

func testGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	full := append([]string{"-C", root, "-c", "commit.gpgsign=false"}, args...)
	out, err := exec.Command("git", full...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// One type failing must cost neither the other type nor the sessions after it,
// and what did land is still recorded — the run reports the failure by exiting
// non-zero.
func TestRetrospectRecordsARunWhereOneTypeFailed(t *testing.T) {
	e := newRetroEnv(t, "loom")
	logPath := writeKnowledgeLog(t)
	first := e.addSessionWithCommits("s1", loomRemote, "["+retroTicket+"] Add the command")
	second := e.addSessionWithCommits("s2", loomRemote, "["+retroTicket+"] Cover it with tests")

	orig := runExtractor
	runExtractor = func(_ context.Context, script, input, scope, kind string) (extractRun, error) {
		e.runs = append(e.runs, scope+" "+input)
		if kind == extractTypeTruth {
			return extractRun{}, errExtractorFailed
		}
		return extractRun{Candidates: 2, Score: 0.13}, nil
	}
	t.Cleanup(func() { runExtractor = orig })

	runErr := Retrospect(RetrospectOptions{TicketID: retroTicket})
	if runErr == nil {
		t.Fatal("Retrospect = nil error, want a failure when an extraction failed")
	}
	if !strings.Contains(runErr.Error(), LogPath()) {
		t.Fatalf("error %v doesn't point at %s", runErr, LogPath())
	}

	wantRuns := []string{"loom " + first, "loom " + first, "loom " + second, "loom " + second}
	if !reflect.DeepEqual(e.runs, wantRuns) {
		t.Fatalf("runs = %v, want %v (a failed type costs neither the other type nor the sessions after it)", e.runs, wantRuns)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log.md: %v", err)
	}
	// Two sessions through the decision extractor at two candidates each; the
	// truth pass produced none.
	want := fmt.Sprintf("## [%s] retrospect %s | loom | 0 truth candidates, 4 decision candidates",
		time.Now().Format("2006-01-02"), retroTicket)
	if !strings.Contains(string(data), want) {
		t.Fatalf("log.md missing %q; got:\n%s", want, data)
	}
}

// An interrupt stops the run, but a type that already completed filed its
// candidates in the store — so the session counts as extracted and log.md
// states what landed. Otherwise the run's only history denies producing them.
func TestRetrospectRecordsTheTypesThatLandedBeforeAnInterrupt(t *testing.T) {
	e := newRetroEnv(t, "loom")
	logPath := writeKnowledgeLog(t)
	first := e.addSessionWithCommits("s1", loomRemote, "["+retroTicket+"] Add the command")
	e.addSessionWithCommits("s2", loomRemote, "["+retroTicket+"] Cover it with tests")

	orig := runExtractor
	runExtractor = func(ctx context.Context, script, input, scope, kind string) (extractRun, error) {
		e.runs = append(e.runs, scope+" "+input)
		if kind == extractTypeTruth {
			return extractRun{Candidates: 3, Score: 0.42}, nil
		}
		// Retrospect owns its context (signal.NotifyContext), so the interrupt
		// is the real signal; awaiting the cancellation here keeps the caller's
		// ctx.Err() check deterministic.
		if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
			t.Fatalf("raise SIGINT: %v", err)
		}
		<-ctx.Done()
		return extractRun{}, ctx.Err()
	}
	t.Cleanup(func() { runExtractor = orig })

	if err := Retrospect(RetrospectOptions{TicketID: retroTicket}); err != nil {
		t.Fatalf("Retrospect = %v, want nil (an interrupt is not an extraction failure)", err)
	}

	wantRuns := []string{"loom " + first, "loom " + first}
	if !reflect.DeepEqual(e.runs, wantRuns) {
		t.Fatalf("runs = %v, want %v (the interrupt stops the run at the first session)", e.runs, wantRuns)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log.md: %v", err)
	}
	want := fmt.Sprintf("## [%s] retrospect %s | loom | 3 truth candidates, 0 decision candidates",
		time.Now().Format("2006-01-02"), retroTicket)
	if !strings.Contains(string(data), want) {
		t.Fatalf("log.md missing %q; got:\n%s", want, data)
	}
}

// log.md is bootstrapped at store init, not by the extractor — but the skip is
// logged, so the omission is auditable rather than invisible.
func TestRetrospectSkipsTheLogEntryWhenLogMdIsAbsent(t *testing.T) {
	e := newRetroEnv(t, "loom")
	e.addSessionWithCommits("s1", loomRemote, "["+retroTicket+"] Add the command")

	if err := Retrospect(RetrospectOptions{TicketID: retroTicket}); err != nil {
		t.Fatalf("Retrospect: %v", err)
	}
	if _, err := os.Stat(filepath.Join(knowledgeRoot(), "log.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stat log.md = %v, want it left uncreated", err)
	}
	if !strings.Contains(e.logs.String(), "does not exist — entry not recorded") {
		t.Fatalf("log doesn't record the skipped entry:\n%s", e.logs.String())
	}
}

// Promotion is human-gated: a run may only add to _candidates/ and log.md.
func TestRetrospectLeavesTheValidatedTreesUntouched(t *testing.T) {
	e := newRetroEnv(t, "loom")
	writeKnowledgeLog(t)
	truth := filepath.Join(knowledgeRoot(), "truths", "loom", "t-0001.md")
	if err := os.WriteFile(truth, []byte("# a validated truth\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	decision := filepath.Join(knowledgeRoot(), "decisions", "loom", "d-0001.md")
	if err := os.MkdirAll(filepath.Dir(decision), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(decision, []byte("# a validated decision\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := snapshotTrees(t)

	e.addSessionWithCommits("s1", loomRemote, "["+retroTicket+"] Add the command")
	if err := Retrospect(RetrospectOptions{TicketID: retroTicket}); err != nil {
		t.Fatalf("Retrospect: %v", err)
	}

	if after := snapshotTrees(t); !reflect.DeepEqual(before, after) {
		t.Fatalf("truths/ and decisions/ changed:\nbefore %v\nafter  %v", before, after)
	}
}

// snapshotTrees maps every file under truths/ and decisions/ to its content.
func snapshotTrees(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, tree := range []string{"truths", "decisions"} {
		root := filepath.Join(knowledgeRoot(), tree)
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			out[path] = string(data)
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}
	return out
}

// The id is interpolated into a log.md entry, so it is validated before the run
// spends — or logs — anything.
func TestRetrospectRejectsAMalformedTicketID(t *testing.T) {
	for _, id := range []string{
		"loom/forged\n## [2026-01-01] retrospect loom/x | loom | 9 truth candidates",
		"not-namespaced",
		"loom/bad id",
		"",
	} {
		t.Run(id, func(t *testing.T) {
			e := newRetroEnv(t, "loom")
			logPath := writeKnowledgeLog(t)
			e.addSessionWithCommits("s1", loomRemote, "["+retroTicket+"] Add the command")

			if err := Retrospect(RetrospectOptions{TicketID: id}); err == nil {
				t.Fatal("Retrospect = nil error, want a rejected ticket id")
			}
			if len(e.runs) != 0 {
				t.Fatalf("runs = %v, want none", e.runs)
			}
			data, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatalf("read log.md: %v", err)
			}
			if strings.Contains(string(data), "retrospect") {
				t.Fatalf("log.md carries an entry for a rejected run:\n%s", data)
			}
			// Rejected before the log is teed: the run spent nothing, so it has
			// no trail to leave in extractor.log.
			if _, err := os.Stat(LogPath()); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("stat extractor.log = %v, want it left uncreated", err)
			}
		})
	}
}

func TestRetrospectRequiresTheExtractionBackend(t *testing.T) {
	e := newRetroEnv(t, "loom")
	e.addSessionWithCommits("s1", loomRemote, "["+retroTicket+"] Add the command")
	statBackend = func(string) (os.FileInfo, error) { return nil, os.ErrNotExist }

	err := Retrospect(RetrospectOptions{TicketID: retroTicket})
	if err == nil {
		t.Fatal("Retrospect = nil error, want a failure when the backend is absent")
	}
	if !strings.Contains(err.Error(), claudeBin) {
		t.Fatalf("error %v doesn't name the path extract.py invokes", err)
	}
	if len(e.runs) != 0 {
		t.Fatalf("runs = %v, want none", e.runs)
	}
}

func TestRetrospectRequiresTheExtractorScript(t *testing.T) {
	e := newRetroEnv(t, "loom")
	e.addSessionWithCommits("s1", loomRemote, "["+retroTicket+"] Add the command")
	t.Setenv("LOOM_EXTRACTORS_DIR", filepath.Join(t.TempDir(), "absent"))

	if err := Retrospect(RetrospectOptions{TicketID: retroTicket}); err == nil {
		t.Fatal("Retrospect = nil error, want a failure when extract.py is missing")
	}
	if len(e.runs) != 0 {
		t.Fatalf("runs = %v, want none", e.runs)
	}
}

// The at-most-once ledger is bypassed by design: the installed sweep has
// usually already visited the sessions of a ticket that just closed, and a
// human naming the ticket is asking for the re-run.
func TestRetrospectIgnoresTheLedger(t *testing.T) {
	e := newRetroEnv(t, "loom")
	e.addSessionWithCommits("s1", loomRemote, "["+retroTicket+"] Add the command")

	sweep(context.Background(), Options{})
	if len(e.runs) != 1 {
		t.Fatalf("runs = %v, want the sweep's single extraction", e.runs)
	}
	for i := 0; i < 2; i++ {
		if err := Retrospect(RetrospectOptions{TicketID: retroTicket}); err != nil {
			t.Fatalf("Retrospect: %v", err)
		}
	}

	if len(e.runs) != 5 {
		t.Fatalf("runs = %v, want the sweep's plus two runs of both types", e.runs)
	}
	st, err := loadState()
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	rec := st.Sessions[sessionKey("claude-code", "s1")]
	if rec.Candidates != 2 {
		t.Fatalf("ledger record = %+v, want the sweep's own, unrewritten by the retrospect", rec)
	}
}
