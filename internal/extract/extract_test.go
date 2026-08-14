package extract

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode"

	"loom/internal/config"
	"loom/internal/parse/summary"
	"loom/internal/summaries"
)

var errExtractorFailed = errors.New("extract.py: exit status 1: input not found")

// env wires an isolated LOOM_HOME + knowledge store + extractors checkout,
// captures the log, and returns the recorded extractor invocations.
type env struct {
	t     *testing.T
	logs  *bytes.Buffer
	runs  []string // "<scope> <input>" per invocation
	kinds []string // --extract-type per invocation, parallel to runs
}

func newEnv(t *testing.T, scopes ...string) *env {
	t.Helper()
	home := t.TempDir()
	t.Setenv("LOOM_HOME", home)
	t.Setenv("LOOM_KNOWLEDGE_ROOT", filepath.Join(home, "knowledge"))
	for _, scope := range scopes {
		if err := os.MkdirAll(filepath.Join(home, "knowledge", "truths", scope), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	extractors := t.TempDir()
	if err := os.WriteFile(filepath.Join(extractors, "extract.py"), []byte("#!/usr/bin/env python3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LOOM_EXTRACTORS_DIR", extractors)

	e := &env{t: t, logs: &bytes.Buffer{}}

	orig := runExtractor
	runExtractor = func(ctx context.Context, script, input, scope, kind string) (extractRun, error) {
		e.runs = append(e.runs, scope+" "+input)
		e.kinds = append(e.kinds, kind)
		// A fresh session rarely covers a scope's reference truths, so the
		// stub's default is a completed run that scored below --threshold.
		return extractRun{Candidates: 2, Score: 0.13}, nil
	}
	t.Cleanup(func() { runExtractor = orig })

	log.SetOutput(e.logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	// Sessions summarized before the trigger's watermark belong to the
	// backfill, so the ledger is seeded as though the agent has been
	// installed for a while; the tests' own sessions land after it.
	e.setWatermark(time.Now().Add(-time.Hour))
	return e
}

// setWatermark rewrites the ledger with the given watermark and no visited
// sessions.
func (e *env) setWatermark(at time.Time) {
	e.t.Helper()
	st := &state{Watermark: at.UTC(), Sessions: map[string]record{}}
	if err := st.save(); err != nil {
		e.t.Fatal(err)
	}
}

// addSession folds one Claude Code session into summaries.db the way the
// summarizer does, and writes the artifact the trigger feeds to the extractor.
func (e *env) addSession(sessionID, gitRemote string) string {
	e.t.Helper()
	return e.addSessionAs(summary.AgentClaude, sessionID, gitRemote, "")
}

// addSessionIn records the checkout the session ran in as well, which is what
// the marker derivation resolves against.
func (e *env) addSessionIn(sessionID, gitRemote, cwdRaw string) string {
	e.t.Helper()
	return e.addSessionAs(summary.AgentClaude, sessionID, gitRemote, cwdRaw)
}

// addSessionWithCommits records the commit subjects the session landed too,
// which is what a ticket id is resolved back to sessions through.
func (e *env) addSessionWithCommits(sessionID, gitRemote string, subjects ...string) string {
	e.t.Helper()
	return e.addSessionAs(summary.AgentClaude, sessionID, gitRemote, "", subjects...)
}

func (e *env) addSessionAs(agent summary.Agent, sessionID, gitRemote, cwdRaw string, subjects ...string) string {
	e.t.Helper()
	received := filepath.Join(config.Home(), "received", string(agent), "proj")
	if err := os.MkdirAll(received, 0o755); err != nil {
		e.t.Fatal(err)
	}
	path := filepath.Join(received, sessionID+".jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
		e.t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		e.t.Fatal(err)
	}

	st, err := summaries.Open(filepath.Join(config.Home(), "summaries.db"))
	if err != nil {
		e.t.Fatal(err)
	}
	defer st.Close()
	sum := &summary.SessionSummary{SessionID: sessionID, Agent: agent}
	// Commits reach summaries.db the way the summarizer puts them there: derived
	// from git's confirmation line in a bash result, one per call so each carries
	// its own committed_at.
	for i, subject := range subjects {
		sum.ToolCalls = append(sum.ToolCalls, summary.ToolCall{
			Kind:          summary.KindBash,
			StartedAt:     time.Now().Add(time.Duration(i) * time.Minute),
			ResultSummary: fmt.Sprintf("[main abc123%d] %s", i, subject),
		})
	}
	src := summaries.SourceInfo{
		Project:   "proj",
		Path:      path,
		Size:      info.Size(),
		Mtime:     info.ModTime(),
		GitRemote: gitRemote,
		CwdRaw:    cwdRaw,
	}
	if err := st.WriteSummary(context.Background(), sum, src); err != nil {
		e.t.Fatal(err)
	}
	return path
}

func TestSweepExtractsOnceAcrossRuns(t *testing.T) {
	e := newEnv(t, "loom")
	input := e.addSession("s1", "git@github.com:EnderRealm/loom.git")

	for i := 0; i < 2; i++ {
		sweep(context.Background(), Options{})
	}

	want := "loom " + input
	if len(e.runs) != 1 || e.runs[0] != want {
		t.Fatalf("runs = %v, want exactly [%q] (a session is extracted at most once)", e.runs, want)
	}
}

func TestSweepSkipsUnresolvableScopes(t *testing.T) {
	e := newEnv(t, "loom")
	e.addSession("no-remote", "")
	e.addSession("unknown-scope", "https://github.com/EnderRealm/ticket.git")

	sweep(context.Background(), Options{})

	if len(e.runs) != 0 {
		t.Fatalf("runs = %v, want none (neither session resolves to a known scope)", e.runs)
	}
	logs := e.logs.String()
	for _, want := range []string{
		"skip claude-code/no-remote: no git remote",
		"skip claude-code/unknown-scope: unknown scope \"ticket\"",
	} {
		if !strings.Contains(logs, want) {
			t.Fatalf("log missing %q; got:\n%s", want, logs)
		}
	}

	// A skipped session is recorded, so the skip is logged once rather than
	// on every sweep.
	e.logs.Reset()
	sweep(context.Background(), Options{})
	if strings.Contains(e.logs.String(), "skip ") {
		t.Fatalf("second sweep re-logged skips:\n%s", e.logs.String())
	}
}

func TestSweepDefersActiveSessions(t *testing.T) {
	e := newEnv(t, "loom")
	e.addSession("live", "https://github.com/EnderRealm/loom.git")

	sweep(context.Background(), Options{Idle: time.Hour})
	if len(e.runs) != 0 {
		t.Fatalf("runs = %v, want none (artifact is still being written)", e.runs)
	}

	// Deferring is not a decision: the next sweep past the idle window runs it.
	sweep(context.Background(), Options{})
	if len(e.runs) != 1 {
		t.Fatalf("runs = %v, want one after the artifact went quiet", e.runs)
	}
}

func TestSweepSkipsUnsupportedAgents(t *testing.T) {
	e := newEnv(t, "loom")
	e.addSessionAs(summary.AgentCodex, "rollout-1", "https://github.com/EnderRealm/loom.git", "")

	sweep(context.Background(), Options{})

	if len(e.runs) != 0 {
		t.Fatalf("runs = %v, want none (preprocess.py can't read a codex rollout)", e.runs)
	}
	if !strings.Contains(e.logs.String(), `skip codex-cli/rollout-1: unsupported agent "codex-cli"`) {
		t.Fatalf("log missing the unsupported-agent reason:\n%s", e.logs.String())
	}
	st, err := loadState()
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if got := st.Sessions[sessionKey("codex-cli", "rollout-1")].Outcome; got != outcomeSkipped {
		t.Fatalf("outcome = %q, want %q (the skip must be auditable)", got, outcomeSkipped)
	}
}

// extractor.log is the audit record for what the sweep declined to do, and
// agent/session id/source path all come from a remote shipper. A control
// character in one of them must not open a line of its own, or a shipper can
// forge an entry saying a session it controls was handled.
func TestSweepEscapesHostileIdentityInTheLog(t *testing.T) {
	const remote = "https://github.com/EnderRealm/loom.git"
	cases := []struct {
		name     string
		injected string
	}{
		{name: "newline", injected: "\nskip forged: extracted"},
		{name: "carriage return", injected: "\rskip forged: extracted"},
		{name: "ansi escape", injected: "\x1b[2Kskip forged: extracted"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, "loom")
			okID := "ok" + tc.injected
			failID := "fail" + tc.injected
			skipID := "skip" + tc.injected
			e.addSession(okID, remote)
			e.addSession(failID, remote)
			e.addSession(skipID, "")
			e.addSessionAs(summary.Agent("codex-cli"+tc.injected), "rollout", remote, "")

			orig := runExtractor
			runExtractor = func(ctx context.Context, script, input, scope, _ string) (extractRun, error) {
				if strings.HasPrefix(filepath.Base(input), "fail") {
					return extractRun{}, errExtractorFailed
				}
				return extractRun{Candidates: 2, Score: 0.13}, nil
			}
			t.Cleanup(func() { runExtractor = orig })

			sweep(context.Background(), Options{})

			// extract + ok, extract + FAILED, two skips, and the sweep summary.
			logs := e.logs.String()
			if got := strings.Count(logs, "\n"); got != 7 {
				t.Fatalf("log has %d lines, want 7 — one per statement:\n%q", got, logs)
			}
			if strings.ContainsFunc(strings.ReplaceAll(logs, "\n", ""), unicode.IsControl) {
				t.Fatalf("log carries a raw control character:\n%q", logs)
			}
			for _, want := range []string{
				strconv.Quote(okID),
				strconv.Quote(failID),
				strconv.Quote(skipID),
				strconv.Quote("codex-cli" + tc.injected),
			} {
				if !strings.Contains(logs, want) {
					t.Fatalf("log missing the escaped identity %q:\n%q", want, logs)
				}
			}

			// The ledger must name the real id too — json.MarshalIndent escapes
			// the key, so the file stays one unambiguous record per session.
			data, err := os.ReadFile(statePath())
			if err != nil {
				t.Fatalf("read state: %v", err)
			}
			if strings.ContainsFunc(strings.ReplaceAll(string(data), "\n", ""), unicode.IsControl) {
				t.Fatalf("ledger carries a raw control character:\n%s", data)
			}
			st, err := loadState()
			if err != nil {
				t.Fatalf("load state: %v", err)
			}
			if got := st.Sessions[sessionKey("claude-code", okID)].Outcome; got != outcomeExtracted {
				t.Fatalf("outcome = %q for %q, want %q", got, okID, outcomeExtracted)
			}
			if got := st.Sessions[sessionKey("claude-code", skipID)].Outcome; got != outcomeSkipped {
				t.Fatalf("outcome = %q for %q, want %q", got, skipID, outcomeSkipped)
			}
		})
	}
}

// The escaping above must stay invisible on the ordinary path: a log that
// quotes every routine line is a log operators stop reading.
func TestSweepLogsOrdinaryIdentityUnquoted(t *testing.T) {
	e := newEnv(t, "loom")
	const id = "195f819e-1e11-4e08-8c16-a340f512f892"
	input := e.addSession(id, "https://github.com/EnderRealm/loom.git")

	sweep(context.Background(), Options{})

	want := "extract claude-code/" + id + " scope=loom source=git-remote input=" + input
	if !strings.Contains(e.logs.String(), want) {
		t.Fatalf("log missing %q — a real session id must read unquoted:\n%s", want, e.logs.String())
	}
}

func TestSweepSkipsSessionsSummarizedBeforeTheWatermark(t *testing.T) {
	e := newEnv(t, "loom")
	// The watermark is stamped when the ledger is created, so a session
	// summarized before it predates the trigger: it belongs to the backfill
	// (loom/batch-runner-session-12da), not to an unattended sweep.
	e.setWatermark(time.Now().Add(time.Hour))
	e.addSession("historical", "https://github.com/EnderRealm/loom.git")

	sweep(context.Background(), Options{})

	if len(e.runs) != 0 {
		t.Fatalf("runs = %v, want none (the session predates the watermark)", e.runs)
	}
	st, err := loadState()
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	// Unvisited rather than skipped: the backfill still has to cover it.
	if len(st.Sessions) != 0 {
		t.Fatalf("ledger recorded %v, want nothing for a pre-watermark session", st.Sessions)
	}
}

func TestLoadStateStampsWatermarkOnFirstRun(t *testing.T) {
	newEnv(t)
	if err := os.Remove(statePath()); err != nil {
		t.Fatal(err)
	}

	before := time.Now().UTC()
	st, err := loadState()
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if st.Watermark.Before(before) {
		t.Fatalf("watermark = %s, want >= %s (stamped at first run)", st.Watermark, before)
	}
	// Persisted immediately, or the bound would drift forward each sweep.
	reloaded, err := loadState()
	if err != nil {
		t.Fatalf("reload state: %v", err)
	}
	if !reloaded.Watermark.Equal(st.Watermark) {
		t.Fatalf("watermark = %s after reload, want %s", reloaded.Watermark, st.Watermark)
	}
}

func TestSweepNoOpsWithoutExtractorScript(t *testing.T) {
	e := newEnv(t, "loom")
	e.addSession("s1", "https://github.com/EnderRealm/loom.git")
	t.Setenv("LOOM_EXTRACTORS_DIR", filepath.Join(t.TempDir(), "absent"))

	sweep(context.Background(), Options{})

	if len(e.runs) != 0 {
		t.Fatalf("runs = %v, want none when extract.py is missing", e.runs)
	}
	if !strings.Contains(e.logs.String(), "extractor unavailable") {
		t.Fatalf("log missing the missing-extractor reason:\n%s", e.logs.String())
	}
	st, err := loadState()
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if len(st.Sessions) != 0 {
		t.Fatalf("ledger recorded %v despite no extractor — sessions must stay unvisited", st.Sessions)
	}
}

// A below-threshold score is extract.py's normal outcome on a scope holding a
// handful of validated truths, and the script exits non-zero for it after
// emitting candidates. It must not be recorded as a failure, or a genuine
// failure is indistinguishable from noise.
func TestSweepRecordsCompletedRunRegardlessOfScore(t *testing.T) {
	e := newEnv(t, "loom")
	e.addSession("s1", "https://github.com/EnderRealm/loom.git")

	sweep(context.Background(), Options{})

	if strings.Contains(e.logs.String(), "FAILED") {
		t.Fatalf("a below-threshold run was logged as a failure:\n%s", e.logs.String())
	}
	st, err := loadState()
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	rec := st.Sessions[sessionKey("claude-code", "s1")]
	if rec.Outcome != outcomeExtracted {
		t.Fatalf("outcome = %q, want %q", rec.Outcome, outcomeExtracted)
	}
	if rec.Candidates != 2 || rec.Score != 0.13 {
		t.Fatalf("record = %+v, want the run's candidate count and score", rec)
	}
}

func TestSweepRecordsFailures(t *testing.T) {
	e := newEnv(t, "loom")
	e.addSession("s1", "https://github.com/EnderRealm/loom.git")

	orig := runExtractor
	runExtractor = func(ctx context.Context, script, input, scope, _ string) (extractRun, error) {
		return extractRun{}, errExtractorFailed
	}
	t.Cleanup(func() { runExtractor = orig })

	sweep(context.Background(), Options{})

	if !strings.Contains(e.logs.String(), "FAILED") ||
		!strings.Contains(e.logs.String(), errExtractorFailed.Error()) {
		t.Fatalf("log missing the failure reason:\n%s", e.logs.String())
	}
	data, err := os.ReadFile(statePath())
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	if !strings.Contains(string(data), outcomeFailed) {
		t.Fatalf("state missing the failed outcome:\n%s", data)
	}
}

// The updater re-runs `loom install extractor` from its own daemon
// environment, which carries none of the tunables; the persisted values must
// survive that or the rewritten plist silently drops them.
func TestPersistedTunablesSurviveAnEmptyEnvironment(t *testing.T) {
	t.Setenv("LOOM_HOME", t.TempDir())
	for _, k := range tunableKeys {
		t.Setenv(k, "")
	}
	t.Setenv(EnvExtractorsDir, "/opt/loom/extractors")
	t.Setenv(EnvModel, "opus")
	if _, err := PersistTunables(); err != nil {
		t.Fatalf("persist tunables: %v", err)
	}

	t.Setenv(EnvExtractorsDir, "")
	t.Setenv(EnvModel, "")
	got, err := PersistTunables()
	if err != nil {
		t.Fatalf("re-persist tunables: %v", err)
	}
	want := map[string]string{EnvExtractorsDir: "/opt/loom/extractors", EnvModel: "opus"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tunables = %v, want %v", got, want)
	}
	// And the agent resolves them the same way the plist does.
	if dir := ExtractorsDir(); dir != "/opt/loom/extractors" {
		t.Fatalf("ExtractorsDir() = %q, want the persisted value", dir)
	}
	if m := CurrentSettings().Model; m != "opus" {
		t.Fatalf("model = %q, want the persisted value", m)
	}
}

// The sweep tests stub runExtractor out, so these two drive the real one
// against a shell stand-in for extract.py — no LLM call, but the actual
// argv and the actual exit-code-vs-result-file semantics.
func TestRunExtractorSucceedsOnBelowThresholdExit(t *testing.T) {
	t.Setenv("LOOM_HOME", t.TempDir())
	dir := t.TempDir()
	argsPath := filepath.Join(dir, "args")
	resultPath := filepath.Join(dir, "result.json")
	if err := os.WriteFile(resultPath, []byte(`{"candidates_valid":3,"mean_score":0.21,"verdict":"FAIL"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// Mirrors extract.py: emit candidates, write --json-out, then exit 1
	// because the coverage score is below --threshold.
	script := writeScript(t, dir, fmt.Sprintf(`#!/bin/sh
echo "$@" > %s
while [ $# -gt 0 ]; do
	if [ "$1" = "--json-out" ]; then cp %s "$2"; fi
	shift
done
exit 1
`, argsPath, resultPath))

	run, err := runExtractor(context.Background(), script, "/tmp/session.jsonl", "loom", extractType)
	if err != nil {
		t.Fatalf("runExtractor: %v (a below-threshold verdict is not a failure)", err)
	}
	if run.Candidates != 3 || run.Score != 0.21 {
		t.Fatalf("run = %+v, want the result file's candidate count and score", run)
	}
	args, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatalf("read args: %v", err)
	}
	// Pinned rather than left to the script's defaults: the trigger runs
	// unattended, and the llm judge would cost a second model call.
	for _, want := range []string{"--extract-type truth", "--judge keyword"} {
		if !strings.Contains(string(args), want) {
			t.Fatalf("argv %q missing %q", args, want)
		}
	}
}

func TestRunExtractorFailsWhenNoResultIsWritten(t *testing.T) {
	t.Setenv("LOOM_HOME", t.TempDir())
	script := writeScript(t, t.TempDir(), "#!/bin/sh\necho 'claude CLI failed (exit 1)' >&2\nexit 2\n")

	if _, err := runExtractor(context.Background(), script, "/tmp/session.jsonl", "loom", extractType); err == nil {
		t.Fatal("runExtractor = nil error, want failure when the run wrote no result")
	} else if !strings.Contains(err.Error(), "claude CLI failed") {
		t.Fatalf("error %v drops the extractor's own message", err)
	}
}

func writeScript(t *testing.T, dir, body string) string {
	t.Helper()
	path := filepath.Join(dir, "extract.py")
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}
