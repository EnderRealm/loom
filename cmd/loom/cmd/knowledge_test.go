package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"unicode"
)

// seedStore points the knowledge store at a fresh git repo with one commit, as
// the live store is, and redirects LOOM_HOME so a degraded commit writes its log
// into the test's own state root. Global git config is isolated: init.templateDir
// would seed repos we did not ask for, and a global identity would mask a
// fixture that configures none.
func seedStore(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	root := t.TempDir()
	t.Setenv("LOOM_KNOWLEDGE_ROOT", root)
	t.Setenv("LOOM_HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	storeGit(t, root, "init")
	storeGit(t, root, "config", "user.email", "test@example.com")
	storeGit(t, root, "config", "user.name", "loom test")
	if err := os.WriteFile(filepath.Join(root, "log.md"), []byte("# Knowledge log\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	storeGit(t, root, "add", "-A")
	storeGit(t, root, "commit", "-m", "seed store")
	return root
}

func storeGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	full := append([]string{"-C", root, "-c", "commit.gpgsign=false"}, args...)
	out, err := exec.Command("git", full...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// writeResult is the subcommand's JSON response. The two outcomes are separate
// because they degrade differently: warn means no commit records the writes,
// push_warn that a commit that did land is still local — which a fixture store,
// having no upstream, reports for every plan that commits.
type writeResult struct {
	Warn     string `json:"warn"`
	PushWarn string `json:"push_warn"`
}

// parseWriteResult reads the response off the subcommand's stdout.
func parseWriteResult(t *testing.T, out string) writeResult {
	t.Helper()
	var result writeResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("stdout %q: %v", out, err)
	}
	return result
}

// runWrite drives one plan through a command built exactly like the registered
// one, returning its stdout and the error the process would exit on.
func runWrite(t *testing.T, plan string) (string, error) {
	t.Helper()
	cmd := newKnowledgeCmd()
	cmd.SetArgs([]string{"write"})
	cmd.SetIn(strings.NewReader(plan))
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	err := cmd.Execute()
	if errOut.Len() > 0 {
		t.Errorf("knowledge write wrote to stderr itself: %q", errOut.String())
	}
	return out.String(), err
}

// TestKnowledgeWriteAppliesAPlan is the cross-language entry point end to end: a
// plan of every op lands in the store and is committed as one record, with no
// commit code on the writing side.
func TestKnowledgeWriteAppliesAPlan(t *testing.T) {
	root := seedStore(t)
	archived := filepath.Join(root, "_candidates", "_rejected", "truths", "loom", "one.md")
	candidate := filepath.Join(root, "_candidates", "truths", "loom", "one.md")
	if err := os.MkdirAll(filepath.Dir(candidate), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(candidate, []byte("candidate\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	storeGit(t, root, "add", "-A")
	storeGit(t, root, "commit", "-m", "seed candidate")

	plan, err := json.Marshal(writePlan{
		Message: "extract abc | loom | 1 truth candidate(s)",
		Changes: []change{
			{Op: opWrite, Path: filepath.Join(root, "truths", "loom", "two.md"), Body: "two\n"},
			{Op: opRename, From: candidate, To: archived, Droppable: true},
			{Op: opAppend, Path: filepath.Join(root, "log.md"), Text: "\n## [2026-08-23] extract abc | loom | 1 truth candidate(s)\n"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	out, err := runWrite(t, string(plan))
	if err != nil {
		t.Fatalf("knowledge write: %v\n%s", err, out)
	}
	if warn := parseWriteResult(t, out).Warn; warn != "" {
		t.Errorf("warn = %q, want an empty warning", warn)
	}
	if subject := storeGit(t, root, "log", "-1", "--format=%s"); subject != "extract abc | loom | 1 truth candidate(s)" {
		t.Errorf("commit subject = %q", subject)
	}
	names := storeGit(t, root, "show", "--name-status", "--no-renames", "--format=", "HEAD")
	for _, want := range []string{
		"A\ttruths/loom/two.md",
		"D\t_candidates/truths/loom/one.md",
		"A\t_candidates/_rejected/truths/loom/one.md",
		"M\tlog.md",
	} {
		if !strings.Contains(names, want) {
			t.Errorf("commit missing %q:\n%s", want, names)
		}
	}
	if body, err := os.ReadFile(archived); err != nil || string(body) != "candidate\n" {
		t.Errorf("renamed file = %q, %v", string(body), err)
	}
}

// TestKnowledgeWriteRejectsBadPlans: nothing is written for a plan the store
// could not act on, so a writer's mistake cannot leave the store half changed.
// ROOT in a plan is replaced by the case's store root, for the cases whose
// change would leave a trace in the store if it were applied.
func TestKnowledgeWriteRejectsBadPlans(t *testing.T) {
	for _, tc := range []struct{ name, plan, want string }{
		{"unparseable", "not json", "parse plan"},
		{"no message", `{"changes":[{"op":"touch","path":"x"}]}`, "message is required"},
		{"no changes", `{"message":"m","changes":[]}`, "changes is empty"},
		{"unknown op", `{"message":"m","changes":[{"op":"chmod","path":"x"}]}`, `unknown op "chmod"`},
		{"missing path", `{"message":"m","changes":[{"op":"write","body":"x"}]}`, "write requires path"},
		{"missing rename field", `{"message":"m","changes":[{"op":"rename","from":"x"}]}`, "rename requires from and to"},
		{"droppable on a write", `{"message":"m","changes":[{"op":"write","path":"x","body":"y","droppable":true}]}`, "droppable applies to rename only"},
		// A field the op does not consume, one case per op: applied, each would
		// silently do something other than what the writer meant.
		{"text on a write", `{"message":"m","changes":[{"op":"write","path":"ROOT/log.md","text":"y"}]}`, "changes[0]: write does not take text"},
		{"body on an append", `{"message":"m","changes":[{"op":"append","path":"ROOT/log.md","body":"y"}]}`, "changes[0]: append does not take body"},
		{"body on a remove", `{"message":"m","changes":[{"op":"remove","path":"x","body":"y"}]}`, "changes[0]: remove does not take body"},
		{"text on a touch", `{"message":"m","changes":[{"op":"touch","path":"x","text":"y"}]}`, "changes[0]: touch does not take text"},
		{"to on a touch", `{"message":"m","changes":[{"op":"touch","path":"x","to":"y"}]}`, "changes[0]: touch does not take to"},
		{"path on a rename", `{"message":"m","changes":[{"op":"rename","from":"x","to":"y","path":"z"}]}`, "changes[0]: rename does not take path"},
		{"body on a rename", `{"message":"m","changes":[{"op":"rename","from":"x","to":"y","body":"z"}]}`, "changes[0]: rename does not take body"},
		{"text on a rename", `{"message":"m","changes":[{"op":"rename","from":"x","to":"y","text":"z"}]}`, "changes[0]: rename does not take text"},
		// Refused before the first change is applied, not when the loop reaches it:
		// the write would leave an untracked file behind.
		{"foreign field after a valid change", `{"message":"m","changes":[{"op":"write","path":"ROOT/truths/loom/one.md","body":"one\n"},{"op":"remove","path":"ROOT/log.md","from":"y"}]}`, "changes[1]: remove does not take from"},
		// A key no op takes is dropped by a plain decode, which would leave this
		// write truncating log.md and committing that as deliberate.
		{"unknown field on a write", `{"message":"m","changes":[{"op":"write","path":"ROOT/log.md","content":"y"}]}`, `unknown field "content"`},
		{"unknown top-level field", `{"message":"m","changes":[{"op":"touch","path":"ROOT/log.md"}],"dry_run":true}`, `unknown field "dry_run"`},
		{"data after the plan", `{"message":"m","changes":[{"op":"touch","path":"ROOT/log.md"}]} {}`, "unexpected data after the plan"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := seedStore(t)
			head := storeGit(t, root, "rev-parse", "HEAD")
			plan := strings.ReplaceAll(tc.plan, "ROOT", root)

			out, err := runWrite(t, plan)

			if err == nil {
				t.Fatalf("plan accepted: %s", plan)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %v does not name %q", err, tc.want)
			}
			if out != "" {
				t.Errorf("a refused plan printed a result: %q", out)
			}
			if now := storeGit(t, root, "rev-parse", "HEAD"); now != head {
				t.Errorf("HEAD moved for a refused plan: %s -> %s", head, now)
			}
			if st := storeGit(t, root, "status", "--porcelain", "-uall"); st != "" {
				t.Errorf("a refused plan touched the store:\n%s", st)
			}
		})
	}
}

// TestKnowledgeWriteFailedWriteStillCommits: a plan that fails part way exits
// non-zero, and the changes that landed before the failure are still recorded —
// the writes are on disk either way.
func TestKnowledgeWriteFailedWriteStillCommits(t *testing.T) {
	root := seedStore(t)
	written := filepath.Join(root, "truths", "loom", "one.md")
	plan := `{"message":"half a plan","changes":[` +
		`{"op":"write","path":"` + written + `","body":"one\n"},` +
		`{"op":"append","path":"` + filepath.Join(root, "absent", "log.md") + `","text":"entry\n"}]}`

	out, err := runWrite(t, plan)

	if err == nil {
		t.Fatal("a failed write exited zero")
	}
	if !strings.Contains(err.Error(), "log.md") {
		t.Errorf("error %v does not name the failed write", err)
	}
	if warn := parseWriteResult(t, out).Warn; warn != "" {
		t.Errorf("warn = %q, want the record's own outcome alongside the failure", warn)
	}
	if names := storeGit(t, root, "show", "--name-status", "--format=", "HEAD"); !strings.Contains(names, "A\ttruths/loom/one.md") {
		t.Errorf("the write that landed was not committed:\n%s", names)
	}
}

// TestKnowledgeWriteReportsAFailedCommit: a store that is not a repo takes the
// writes and reports the missing record as a warning, since the writes landed.
func TestKnowledgeWriteReportsAFailedCommit(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LOOM_KNOWLEDGE_ROOT", root)
	t.Setenv("LOOM_HOME", t.TempDir())
	written := filepath.Join(root, "truths", "loom", "one.md")
	plan := `{"message":"write one","changes":[{"op":"write","path":"` + written + `","body":"one\n"}]}`

	out, err := runWrite(t, plan)

	if err != nil {
		t.Fatalf("knowledge write: %v\n%s", err, out)
	}
	result := parseWriteResult(t, out)
	if !strings.Contains(result.Warn, "not a git repo") {
		t.Errorf("warn = %q, want a not-a-git-repo reason", result.Warn)
	}
	if _, err := os.Stat(written); err != nil {
		t.Errorf("written file missing: %v", err)
	}
}

// TestKnowledgeWriteRefusesAPathOutsideTheStore: the plan is a string a non-Go
// writer composed, and the subcommand runs with the user's own reach. A path
// that does not land in the store is refused by the store itself, before
// anything is written.
func TestKnowledgeWriteRefusesAPathOutsideTheStore(t *testing.T) {
	root := seedStore(t)
	outside := t.TempDir()
	external := filepath.Join(outside, "keep.md")
	if err := os.WriteFile(external, []byte("untouched\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	head := storeGit(t, root, "rev-parse", "HEAD")
	plan := `{"message":"write outside","changes":[{"op":"write","path":"` + external + `","body":"forged\n"}]}`

	out, err := runWrite(t, plan)

	if err == nil {
		t.Fatal("a path outside the store was accepted")
	}
	if !strings.Contains(err.Error(), "outside the knowledge store") {
		t.Errorf("error %v does not name the containment rule", err)
	}
	if body, _ := os.ReadFile(external); string(body) != "untouched\n" {
		t.Errorf("the external file was written: %q", string(body))
	}
	if now := storeGit(t, root, "rev-parse", "HEAD"); now != head {
		t.Errorf("HEAD moved for a refused write: %s -> %s", head, now)
	}
	if warn := parseWriteResult(t, out).Warn; warn != "" {
		t.Errorf("warn = %q, want the record's own outcome alongside the failure", warn)
	}
}

// runScopeAdd drives one `knowledge scope add` through a command built exactly
// like the registered one, returning its stdout and the error the process would
// exit on.
func runScopeAdd(t *testing.T, names ...string) (string, error) {
	t.Helper()
	cmd := newKnowledgeCmd()
	cmd.SetArgs(append([]string{"scope", "add"}, names...))
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	err := cmd.Execute()
	return out.String(), err
}

// TestKnowledgeScopeAddCreatesTheScope: extraction is gated on truths/<name>/
// existing, so the command's whole job is that directory — committed, since a
// directory git does not track is one the next clone does not have.
func TestKnowledgeScopeAddCreatesTheScope(t *testing.T) {
	root := seedStore(t)

	out, err := runScopeAdd(t, "warp")

	if err != nil {
		t.Fatalf("scope add: %v\n%s", err, out)
	}
	created := filepath.Join(root, "truths", "warp")
	if fi, err := os.Stat(created); err != nil || !fi.IsDir() {
		t.Fatalf("truths/warp/ = %v, %v", fi, err)
	}
	if !strings.Contains(out, created) {
		t.Errorf("output %q does not name what it created", out)
	}
	if subject := storeGit(t, root, "log", "-1", "--format=%s"); subject != "add knowledge scope warp" {
		t.Errorf("commit subject = %q", subject)
	}
	if names := storeGit(t, root, "show", "--name-status", "--format=", "HEAD"); !strings.Contains(names, "A\ttruths/warp/.gitkeep") {
		t.Errorf("commit does not carry the placeholder:\n%s", names)
	}

	// Onboarding a project twice is what an operator does when they cannot
	// remember whether they already did, so it reports rather than fails.
	head := storeGit(t, root, "rev-parse", "HEAD")
	out, err = runScopeAdd(t, "warp")
	if err != nil {
		t.Fatalf("scope add of an existing scope: %v\n%s", err, out)
	}
	if !strings.Contains(out, "already exists") {
		t.Errorf("output %q does not report the scope as already present", out)
	}
	if now := storeGit(t, root, "rev-parse", "HEAD"); now != head {
		t.Errorf("HEAD moved for a scope that already existed: %s -> %s", head, now)
	}
}

// TestKnowledgeScopeAddDedupesRepeatedNames: no stat finds a name that does not
// exist yet, so a name given twice would otherwise be written twice and reach
// the commit subject twice.
func TestKnowledgeScopeAddDedupesRepeatedNames(t *testing.T) {
	root := seedStore(t)

	out, err := runScopeAdd(t, "warp", "warp")

	if err != nil {
		t.Fatalf("scope add: %v\n%s", err, out)
	}
	if n := strings.Count(out, "created "); n != 1 {
		t.Errorf("output reports %d creations of one scope:\n%s", n, out)
	}
	if subject := storeGit(t, root, "log", "-1", "--format=%s"); subject != "add knowledge scope warp" {
		t.Errorf("commit subject = %q", subject)
	}
}

// TestKnowledgeScopeAddReportsNothingItCouldNotCreate: a name already taken by a
// regular file is not an existing scope, and the write for it fails — so the
// path must not be reported as created alongside the error.
func TestKnowledgeScopeAddReportsNothingItCouldNotCreate(t *testing.T) {
	root := seedStore(t)
	if err := os.MkdirAll(filepath.Join(root, "truths"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "truths", "warp"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runScopeAdd(t, "warp")

	if err == nil {
		t.Fatalf("scope add succeeded against a name taken by a regular file:\n%s", out)
	}
	if strings.Contains(out, "created ") {
		t.Errorf("output reports creating a path where nothing was created:\n%s", out)
	}
}

// TestKnowledgeScopeAddRefusesUnsafeNames: the name becomes a path under the
// store, and the whole invocation is refused rather than half applied, so an
// operator's next run doesn't have to work out which half landed.
func TestKnowledgeScopeAddRefusesUnsafeNames(t *testing.T) {
	for _, name := range []string{"../evil", "Loom", ".", ""} {
		t.Run(name, func(t *testing.T) {
			root := seedStore(t)
			head := storeGit(t, root, "rev-parse", "HEAD")

			if _, err := runScopeAdd(t, "warp", name); err == nil {
				t.Fatalf("scope add %q was accepted", name)
			}
			if _, err := os.Stat(filepath.Join(root, "truths")); err == nil {
				t.Error("a refused invocation created truths/ anyway")
			}
			if now := storeGit(t, root, "rev-parse", "HEAD"); now != head {
				t.Errorf("HEAD moved for a refused invocation: %s -> %s", head, now)
			}
		})
	}
}

// TestKnowledgeScopeAddWithoutAStore: a machine that has no knowledge store gets
// a reason, not the ENOENT of a path it never named.
func TestKnowledgeScopeAddWithoutAStore(t *testing.T) {
	root := filepath.Join(t.TempDir(), "knowledge")
	t.Setenv("LOOM_KNOWLEDGE_ROOT", root)
	t.Setenv("LOOM_HOME", t.TempDir())

	_, err := runScopeAdd(t, "warp")

	if err == nil {
		t.Fatal("scope add succeeded against a store that does not exist")
	}
	if !strings.Contains(err.Error(), "no knowledge store at "+root) {
		t.Errorf("error %v does not name the missing store", err)
	}
}

// TestKnowledgeConflictsListsContradictions drives the subcommand over a store
// with one candidate → validated contradiction and one dangling entry.
func TestKnowledgeConflictsListsContradictions(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LOOM_KNOWLEDGE_ROOT", root)
	files := map[string]string{
		"truths/loom/target.md":                 "---\nid: loom-target\ntitle: T\nstatus: validated\n---\n",
		"_candidates/truths/loom/against--1.md": "---\nid: loom-against\ntitle: A\nstatus: candidate\ncontradicts:\n  - loom-target\n---\n",
		"_candidates/truths/loom/dangle--1.md":  "---\nid: loom-dangle\ntitle: D\nstatus: candidate\ncontradicts: [loom-nowhere]\n---\n",
	}
	for rel, body := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	cmd := newKnowledgeCmd()
	cmd.SetArgs([]string{"conflicts"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("knowledge conflicts: %v", err)
	}
	for _, want := range []string{
		"conflict: loom-against contradicts loom-target (_candidates/truths/loom/against--1.md)",
		"warning: loom-dangle: contradicts loom-nowhere, but no validated artifact has that id",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q:\n%s", want, out.String())
		}
	}
}

// TestFakeJudgeProcess is the judge fakeJudge's wrapper script re-executes in
// place of claude. It records the cluster each call was asked about, one line
// per call, and partitions it: the ids LOOM_TEST_JUDGE_SAME names form one
// group, every other id a group of its own. A cluster holding the id in
// LOOM_TEST_JUDGE_BREAK gets a reply that is no partition, and one holding
// LOOM_TEST_JUDGE_FAIL a CLI that exits non-zero.
func TestFakeJudgeProcess(t *testing.T) {
	if os.Getenv("LOOM_TEST_JUDGE") != "1" {
		return
	}
	prompt, err := io.ReadAll(os.Stdin)
	if err != nil {
		t.Fatal(err)
	}
	_, rest, _ := strings.Cut(string(prompt), "<candidates>\n")
	body, _, _ := strings.Cut(rest, "\n</candidates>")
	var members []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(body), &members); err != nil {
		t.Fatalf("prompt carries no candidate list: %v", err)
	}
	var ids []string
	for _, m := range members {
		ids = append(ids, m.ID)
	}
	f, err := os.OpenFile(os.Getenv("LOOM_TEST_JUDGE_LOG"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(f, strings.Join(ids, ","))
	f.Close()

	if slices.Contains(ids, os.Getenv("LOOM_TEST_JUDGE_FAIL")) {
		fmt.Fprintln(os.Stderr, "fake judge: model unavailable")
		os.Exit(3)
	}
	type group struct {
		IDs    []string `json:"ids"`
		Reason string   `json:"reason"`
	}
	var groups []group
	if !slices.Contains(ids, os.Getenv("LOOM_TEST_JUDGE_BREAK")) {
		same := strings.Split(os.Getenv("LOOM_TEST_JUDGE_SAME"), ",")
		var together []string
		for _, id := range ids {
			if slices.Contains(same, id) {
				together = append(together, id)
				continue
			}
			groups = append(groups, group{IDs: []string{id}, Reason: "fake: distinct claim"})
		}
		if len(together) > 0 {
			groups = append(groups, group{IDs: together, Reason: "fake: one truth"})
		}
	}
	out, _ := json.Marshal(map[string]any{"groups": groups})
	fmt.Println("Partition follows.\n" + string(out))
	os.Exit(0)
}

// fakeJudge points the dupes judge at a script re-executing this test binary
// as TestFakeJudgeProcess, partitioning by the ids in same, and returns the
// path of the log its calls are recorded in.
func fakeJudge(t *testing.T, same ...string) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "claude")
	body := "#!/bin/sh\nLOOM_TEST_JUDGE=1 exec '" + strings.ReplaceAll(exe, "'", `'\''`) + "' -test.run='^TestFakeJudgeProcess$'\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(dir, "calls.log")
	t.Setenv("LOOM_DUPES_JUDGE_BIN", script)
	t.Setenv("LOOM_TEST_JUDGE_LOG", log)
	t.Setenv("LOOM_TEST_JUDGE_SAME", strings.Join(same, ","))
	t.Setenv("LOOM_TEST_JUDGE_BREAK", "")
	t.Setenv("LOOM_TEST_JUDGE_FAIL", "")
	return log
}

// judgeCalls reads the fake judge's log: one entry per call, the cluster's ids.
func judgeCalls(t *testing.T, log string) []string {
	t.Helper()
	b, err := os.ReadFile(log)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

// writeTruthCandidate files one truth candidate the way extract.py shapes it.
func writeTruthCandidate(t *testing.T, root, scope, file, id, title, claim, session string) string {
	t.Helper()
	rel := filepath.Join("_candidates", "truths", scope, file)
	body := "---\nid: " + id + "\ntitle: " + title + "\nscope: " + scope + "\ntype: truth\nstatus: candidate\n" +
		"sources:\n  - session: " + session + "\n    project: " + scope + "\n    date: 2026-08-15\n---\n\n## Claim\n\n" + claim + "\n"
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return rel
}

func runDupes(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	cmd := newKnowledgeCmd()
	cmd.SetArgs(append([]string{"dupes"}, args...))
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	err := cmd.Execute()
	return out.String(), errOut.String(), err
}

// reportedGroup is one partition group of a judged cluster as the report prints
// it.
type reportedGroup struct {
	cluster int
	same    bool
	ids     []string
}

// parseJudgedGroups reads the judged clusters' groups back off the report.
func parseJudgedGroups(t *testing.T, report string) []reportedGroup {
	t.Helper()
	var out []reportedGroup
	cluster, inJudged := 0, false
	for _, line := range strings.Split(report, "\n") {
		switch {
		case strings.HasPrefix(line, "judged cluster "):
			cluster++
			inJudged = true
		case strings.HasPrefix(line, "same-id cluster "), strings.HasPrefix(line, "blocked cluster "):
			inJudged = false
		case !inJudged:
		case strings.HasPrefix(line, "  same: "), strings.HasPrefix(line, "  distinct: "):
			out = append(out, reportedGroup{cluster: cluster, same: strings.HasPrefix(line, "  same: ")})
		case strings.HasPrefix(line, "    ") && len(out) > 0 && out[len(out)-1].cluster == cluster:
			_, rest, ok := strings.Cut(line, "  id=")
			if !ok {
				t.Fatalf("member line without an id: %q", line)
			}
			id, _, _ := strings.Cut(rest, "  ")
			g := &out[len(out)-1]
			if !slices.Contains(g.ids, id) {
				g.ids = append(g.ids, id)
			}
		}
	}
	return out
}

// TestKnowledgeDupesIsReadOnlyAndReportsMembers: every member of a judged
// cluster is printed with its path, id and sessions beside the judge's call
// and reason, and the store's git tree and file bytes are untouched.
func TestKnowledgeDupesIsReadOnlyAndReportsMembers(t *testing.T) {
	root := seedStore(t)
	fakeJudge(t, "loom-widget-cache-stale", "warp-widget-cache-goes-stale")
	a := writeTruthCandidate(t, root, "loom", "loom-widget-cache-stale--20260815-101010.md", "loom-widget-cache-stale",
		"Widget cache goes stale after rebuild", "The widget cache keeps stale entries after a rebuild.", "sess-a")
	b := writeTruthCandidate(t, root, "warp", "warp-widget-cache-goes-stale--20260815-111111.md", "warp-widget-cache-goes-stale",
		"Widget cache stale entries survive rebuild", "Stale widget cache entries survive a rebuild.", "sess-b")
	writeTruthCandidate(t, root, "loom", "loom-unrelated--20260815-121212.md", "loom-unrelated",
		"Receiver token rotation", "Rotating the bearer token restarts the receiver.", "sess-c")
	storeGit(t, root, "add", "-A")
	storeGit(t, root, "commit", "-m", "seed candidates")
	before := snapshotTree(t, root)

	out, _, err := runDupes(t)
	if err != nil {
		t.Fatalf("knowledge dupes: %v\n%s", err, out)
	}
	for _, want := range []string{
		"judged cluster 1 (2 ids)",
		"  same: fake: one truth",
		"    " + a + "  id=loom-widget-cache-stale  sessions=sess-a",
		"    " + b + "  id=warp-widget-cache-goes-stale  sessions=sess-b",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "loom-unrelated") {
		t.Errorf("a candidate with no overlap was clustered:\n%s", out)
	}

	// Bytes first: git status may refresh .git/index itself.
	if after := snapshotTree(t, root); !maps.Equal(before, after) {
		t.Errorf("store files changed: before %v, after %v", slices.Sorted(maps.Keys(before)), slices.Sorted(maps.Keys(after)))
	}
	if status := storeGit(t, root, "status", "--porcelain", "--ignored"); status != "" {
		t.Errorf("store working tree changed:\n%s", status)
	}
}

// snapshotTree maps every file under root, .git included, to its bytes.
func snapshotTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		out[p] = string(b)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestKnowledgeDupesSameIDNeedsNoJudge: files under one id once the run stamp
// is removed are one cluster, however differently each extraction worded the
// claim, and no judge is consulted for them.
func TestKnowledgeDupesSameIDNeedsNoJudge(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LOOM_KNOWLEDGE_ROOT", root)
	t.Setenv("LOOM_HOME", t.TempDir())
	log := fakeJudge(t)
	a := writeTruthCandidate(t, root, "loom", "loom-shipper-backoff--20260815-101010.md", "loom-shipper-backoff",
		"Shipper backs off exponentially", "Failed ships double the retry delay.", "sess-a")
	b := writeTruthCandidate(t, root, "warp", "loom-shipper-backoff--20260816-090909.md", "loom-shipper-backoff--20260816-090909",
		"Retry interval grows", "Each rejected upload waits twice as long.", "sess-b")

	out, _, err := runDupes(t)
	if err != nil {
		t.Fatalf("knowledge dupes: %v\n%s", err, out)
	}
	want := "same-id cluster loom-shipper-backoff (2 files)\n" +
		"  same: ids equal once the --<timestamp> suffix is removed\n" +
		"    " + a + "  id=loom-shipper-backoff  sessions=sess-a\n" +
		"    " + b + "  id=loom-shipper-backoff--20260816-090909  sessions=sess-b\n"
	if !strings.Contains(out, want) {
		t.Errorf("report missing\n%s\ngot:\n%s", want, out)
	}
	if calls := judgeCalls(t, log); len(calls) != 0 {
		t.Errorf("judge called %d time(s) for a same-id cluster: %v", len(calls), calls)
	}
}

// TestKnowledgeDupesJudgeCallsOncePerCluster: three blocked clusters cost three
// calls, each asked about its whole cluster; a same-id node is one member; the
// report carries each partition, and a cluster whose reply is no partition, or
// whose CLI fails, is reported without costing the others.
func TestKnowledgeDupesJudgeCallsOncePerCluster(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LOOM_KNOWLEDGE_ROOT", root)
	t.Setenv("LOOM_HOME", t.TempDir())
	log := fakeJudge(t, "loom-b1", "loom-b2")
	t.Setenv("LOOM_TEST_JUDGE_BREAK", "loom-c1")
	t.Setenv("LOOM_TEST_JUDGE_FAIL", "loom-d1")
	// Cluster B: b1 (filed twice), b2, b3 share the parser vocabulary.
	writeTruthCandidate(t, root, "loom", "loom-b1--20260815-010101.md", "loom-b1", "Parser drops trailing frontmatter keys", "parser frontmatter", "s1")
	writeTruthCandidate(t, root, "loom", "loom-b1--20260815-020202.md", "loom-b1", "Parser drops trailing frontmatter keys", "parser frontmatter", "s2")
	writeTruthCandidate(t, root, "warp", "loom-b2--20260815-030303.md", "loom-b2", "Frontmatter parser loses trailing keys", "drops", "s3")
	writeTruthCandidate(t, root, "loom", "loom-b3--20260815-040404.md", "loom-b3", "Parser frontmatter keys are lowercase", "trailing", "s4")
	// Cluster C and D: pairs on their own vocabularies.
	writeTruthCandidate(t, root, "loom", "loom-c1--20260815-050505.md", "loom-c1", "Launchd plist lint gate", "plutil bootstrap", "s5")
	writeTruthCandidate(t, root, "loom", "loom-c2--20260815-060606.md", "loom-c2", "Launchd plist lint before bootstrap", "plutil", "s6")
	writeTruthCandidate(t, root, "loom", "loom-d1--20260815-070707.md", "loom-d1", "Receiver bearer token persisted", "mode 0600", "s7")
	writeTruthCandidate(t, root, "loom", "loom-d2--20260815-080808.md", "loom-d2", "Receiver bearer token persisted 0600", "mode", "s8")

	out, errOut, err := runDupes(t)
	if err == nil || !strings.Contains(err.Error(), "2 of 3 blocked cluster(s) could not be judged") {
		t.Errorf("err = %v, want the two failed clusters counted", err)
	}
	calls := judgeCalls(t, log)
	if want := []string{"loom-b1,loom-b2,loom-b3", "loom-c1,loom-c2", "loom-d1,loom-d2"}; !slices.Equal(calls, want) {
		t.Errorf("judge calls = %v, want one per cluster %v", calls, want)
	}
	groups := parseJudgedGroups(t, out)
	want := []reportedGroup{
		{cluster: 1, same: true, ids: []string{"loom-b1", "loom-b2"}},
		{cluster: 1, same: false, ids: []string{"loom-b3"}},
	}
	if !reflect.DeepEqual(groups, want) {
		t.Errorf("groups = %+v, want %+v\n%s", groups, want, out)
	}
	for _, want := range []string{
		"judged cluster 2 (2 ids)\n  judge failed: judge reply: no group holds loom-c1, loom-c2\n",
		"judged cluster 3 (2 ids)\n  judge failed: ",
		"fake judge: model unavailable",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q:\n%s", want, out)
		}
	}
	for _, want := range []string{
		"cluster 2 (loom-c1, loom-c2): judge failed: judge reply: no group holds loom-c1, loom-c2",
		"cluster 3 (loom-d1, loom-d2): judge failed: ",
	} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr missing %q:\n%s", want, errOut)
		}
	}

	// Every call is on the judge log, under LOOM_HOME and not the store: the
	// cluster asked about, the prompt and the raw reply.
	judgeLog, err := os.ReadFile(filepath.Join(os.Getenv("LOOM_HOME"), "dupes-judge.log"))
	if err != nil {
		t.Fatalf("judge log: %v", err)
	}
	for _, want := range []string{
		"cluster: loom-b1, loom-b2, loom-b3\n--- prompt\n",
		"<candidates>",
		"Partition follows.",
		"fake judge: model unavailable",
	} {
		if !strings.Contains(string(judgeLog), want) {
			t.Errorf("judge log missing %q:\n%s", want, judgeLog)
		}
	}
	if n := strings.Count(string(judgeLog), "=== "); n != 3 {
		t.Errorf("judge log holds %d call(s), want 3", n)
	}

	// --no-judge prints the same blocking without a call.
	before := len(calls)
	out, _, err = runDupes(t, "--no-judge")
	if err != nil {
		t.Fatalf("knowledge dupes --no-judge: %v", err)
	}
	if !strings.Contains(out, "blocked cluster 1 (3 ids, not judged)") {
		t.Errorf("--no-judge report missing the blocked cluster:\n%s", out)
	}
	if n := len(judgeCalls(t, log)); n != before {
		t.Errorf("--no-judge called the judge %d time(s)", n-before)
	}
}

// TestKnowledgeDupesFixtureForgeReviewAgents runs the candidates behind the
// 2026-11-07 measurement, verbatim from the live store, with a fake judge that
// calls the five forge review-agents ids one truth and everything else
// distinct.
func TestKnowledgeDupesFixtureForgeReviewAgents(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LOOM_KNOWLEDGE_ROOT", root)
	t.Setenv("LOOM_HOME", t.TempDir())
	forge := []string{
		"forge-review-agents-no-tk-mcp-access",
		"forge-review-agents-stateless-inline-required",
		"forge-review-agents-stateless-need-inlined-tickets",
		"forge-review-agents-stateless-no-mcp",
		"forge-stateless-review-agents-need-inlined-context",
	}
	fakeJudge(t, forge...)
	paths, err := filepath.Glob(filepath.Join("testdata", "dupes-fixture", "loom", "*.md"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no dupes fixtures: %v", err)
	}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		dst := filepath.Join(root, "_candidates", "truths", "loom", filepath.Base(p))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dst, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	out, _, err := runDupes(t)
	if err != nil {
		t.Fatalf("knowledge dupes: %v\n%s", err, out)
	}
	groups := parseJudgedGroups(t, out)
	found := false
	for _, g := range groups {
		if g.same && slices.Equal(slices.Sorted(slices.Values(g.ids)), forge) {
			found = true
		}
		for _, pair := range [][2]string{
			{"forge-pipeline-stage-routing-by-ticket-type", "ticket-task-pipeline-no-implement-stage"},
			{"forge-tk-dev-mcp-config-split", "forge-dist-bundle-src-drift"},
			{"forge-tk-dev-mcp-config-split", "forge-skill-md-cache-drift"},
		} {
			if g.same && slices.Contains(g.ids, pair[0]) && slices.Contains(g.ids, pair[1]) {
				t.Errorf("%s and %s reported as one truth:\n%s", pair[0], pair[1], out)
			}
		}
	}
	if !found {
		t.Errorf("no same-truth group holds exactly the five forge review-agents ids %v; groups %+v\n%s", forge, groups, out)
	}
	// The related-but-distinct pair blocking proposes reaches the judge and is
	// reported as its call, rather than passing only because it was never asked.
	distinct := 0
	for _, g := range groups {
		if !g.same && (slices.Contains(g.ids, "forge-pipeline-stage-routing-by-ticket-type") || slices.Contains(g.ids, "ticket-task-pipeline-no-implement-stage")) {
			distinct++
		}
	}
	if distinct != 2 {
		t.Errorf("pipeline routing vs ticket task pipeline: %d distinct group(s) reported, want 2\n%s", distinct, out)
	}
}

// TestKnowledgeDupesJudgeCallsNeedTheBinaryOnce: a judge binary that is not
// there is one error naming the path and its override, before any cluster is
// tried, and --no-judge does not need it.
func TestKnowledgeDupesJudgeCallsNeedTheBinaryOnce(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LOOM_KNOWLEDGE_ROOT", root)
	t.Setenv("LOOM_HOME", t.TempDir())
	missing := filepath.Join(t.TempDir(), "no-claude")
	t.Setenv("LOOM_DUPES_JUDGE_BIN", missing)
	writeTruthCandidate(t, root, "loom", "loom-c1--20260815-050505.md", "loom-c1", "Launchd plist lint gate", "plutil bootstrap", "s5")
	writeTruthCandidate(t, root, "loom", "loom-c2--20260815-060606.md", "loom-c2", "Launchd plist lint before bootstrap", "plutil", "s6")

	out, errOut, err := runDupes(t)
	if err == nil || !strings.Contains(err.Error(), missing) || !strings.Contains(err.Error(), "LOOM_DUPES_JUDGE_BIN") {
		t.Errorf("err = %v, want one naming %s and LOOM_DUPES_JUDGE_BIN", err, missing)
	}
	if out != "" || errOut != "" {
		t.Errorf("output before the refusal:\nstdout %q\nstderr %q", out, errOut)
	}
	if out, _, err := runDupes(t, "--no-judge"); err != nil || !strings.Contains(out, "blocked cluster 1 (2 ids, not judged)") {
		t.Errorf("--no-judge without a judge binary: err %v\n%s", err, out)
	}
}

// TestKnowledgeDupesEscapesTerminalControls: candidate ids, filenames and
// sessions are transcript-derived and parseArtifact keeps any control they
// carry, so every place the report prints one — same-id heading, judged and
// unjudged members, the judge-failure line on stderr, the shed list — has to
// emit it inert.
func TestKnowledgeDupesEscapesTerminalControls(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LOOM_KNOWLEDGE_ROOT", root)
	t.Setenv("LOOM_HOME", t.TempDir())
	fakeJudge(t)
	failID := "esc-a-0\x1b]0;pwned\x07"
	t.Setenv("LOOM_TEST_JUDGE_FAIL", failID)
	// Two groups of six and a bridge the cap sheds, as TestDupesCapsClusterSize.
	for i, u := range []string{"aone", "atwo", "athree", "afour", "afive", "asix"} {
		id := "esc-a-" + string(rune('0'+i))
		if i == 0 {
			id = failID
		}
		writeTruthCandidate(t, root, "loom", fmt.Sprintf("esc-a-%d--20260815-01010%d.md", i, i), id, "apple apricot avocado", "acorn "+u, "sess-a")
	}
	for i, u := range []string{"bone", "btwo", "bthree", "bfour", "bfive", "bsix"} {
		writeTruthCandidate(t, root, "warp", fmt.Sprintf("esc-b-%d--20260815-02020%d.md", i, i), fmt.Sprintf("esc-b-%d", i),
			"banana blueberry bilberry", "beech "+u, "sess-b\x1b[2J\u202e")
	}
	writeTruthCandidate(t, root, "loom", "esc-bridge--20260815-030303.md", "esc-bridge\x1b[H", "apple apricot", "banana blueberry", "sess-c")
	writeTruthCandidate(t, root, "loom", "esc-same\x1b[31m--20260815-040404.md", "esc-same\x1b[31m", "Receiver token", "rotation", "sess-d")
	writeTruthCandidate(t, root, "loom", "esc-same\x1b[31m--20260815-050505.md", "esc-same\x1b[31m", "Shipper backoff", "doubling", "sess-e")

	inert := func(name, s string) {
		t.Helper()
		for _, r := range s {
			if r != '\n' && (unicode.IsControl(r) || unicode.Is(unicode.Cf, r)) {
				t.Errorf("%s carries %U:\n%q", name, r, s)
				return
			}
		}
	}
	out, errOut, err := runDupes(t)
	if err == nil {
		t.Error("the failing cluster was not counted")
	}
	inert("stdout", out)
	inert("stderr", errOut)
	for _, want := range []string{
		"same-id cluster esc-same [31m (2 files)",
		"id=esc-a-0 ]0;pwned  sessions=sess-a",
		"sessions=sess-b [2J",
		"shed by the size cap (1 ids)",
		"id=esc-bridge [H  sessions=sess-c",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(errOut, "(esc-a-0 ]0;pwned, esc-a-1, ") {
		t.Errorf("stderr does not name the failed cluster:\n%s", errOut)
	}

	out, errOut, err = runDupes(t, "--no-judge")
	if err != nil {
		t.Fatalf("knowledge dupes --no-judge: %v", err)
	}
	inert("--no-judge stdout", out)
	inert("--no-judge stderr", errOut)
}
