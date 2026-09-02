package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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
func TestKnowledgeWriteRejectsBadPlans(t *testing.T) {
	for _, tc := range []struct{ name, plan, want string }{
		{"unparseable", "not json", "parse plan"},
		{"no message", `{"changes":[{"op":"touch","path":"x"}]}`, "message is required"},
		{"no changes", `{"message":"m","changes":[]}`, "changes is empty"},
		{"unknown op", `{"message":"m","changes":[{"op":"chmod","path":"x"}]}`, `unknown op "chmod"`},
		{"missing path", `{"message":"m","changes":[{"op":"write","body":"x"}]}`, "write requires path"},
		{"missing rename field", `{"message":"m","changes":[{"op":"rename","from":"x"}]}`, "rename requires from and to"},
		{"droppable on a write", `{"message":"m","changes":[{"op":"write","path":"x","body":"y","droppable":true}]}`, "droppable applies to rename only"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := seedStore(t)
			head := storeGit(t, root, "rev-parse", "HEAD")

			out, err := runWrite(t, tc.plan)

			if err == nil {
				t.Fatalf("plan accepted: %s", tc.plan)
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
