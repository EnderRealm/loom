package cmd

import (
	"path/filepath"
	"strings"
	"testing"

	"loom/internal/summaries"
)

const usageHeader = "Usage:"

// TestRuntimeFailurePrintsErrorOnce holds subcommands that parsed fine and
// then failed in their work: the error is the whole of stderr's report, once,
// with no usage block and no cobra "Error:" copy.
func TestRuntimeFailurePrintsErrorOnce(t *testing.T) {
	home := t.TempDir()
	// Present, so retrospect gets past its store guard to the extractor-script
	// refusal this environment provokes.
	st, err := summaries.Open(filepath.Join(home, "summaries.db"))
	if err != nil {
		t.Fatal(err)
	}
	st.Close()

	// A store recorded at schema 3, below the commits table's version 4.
	outdated := filepath.Join(t.TempDir(), "summaries.db")
	st, err = summaries.Open(outdated)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`UPDATE schema_meta SET value = '3' WHERE key = 'schema_version'`); err != nil {
		t.Fatal(err)
	}
	st.Close()

	cases := []struct {
		name string
		args []string
		want string
	}{
		{"relevant without --for-ticket", []string{"relevant"}, "--for-ticket is required"},
		{"retrospect without an extractor", []string{"retrospect", "loom/some-ticket-0001"}, "not found — point LOOM_EXTRACTORS_DIR at a loom checkout"},
		{"summarize against an outdated db", []string{"summarize", "--db", outdated, "--received", t.TempDir()}, "summary db schema is outdated: on disk 3"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, stderr, code := runLoom(t, home, tc.args...)
			if code != 1 {
				t.Fatalf("exit = %d, want 1\nstderr: %q", code, stderr)
			}
			if n := strings.Count(stderr, tc.want); n != 1 {
				t.Fatalf("stderr carries %q %d times, want once\nstderr: %q", tc.want, n, stderr)
			}
			if strings.Contains(stderr, usageHeader) {
				t.Fatalf("stderr = %q, want no usage block for a runtime failure", stderr)
			}
			if strings.Contains(stderr, "Error:") {
				t.Fatalf("stderr = %q, want cobra's prefixed copy silenced", stderr)
			}
		})
	}

	// Where the error is the only thing the command writes, it is all of stderr.
	_, stderr, _ := runLoom(t, home, "relevant")
	if want := "--for-ticket is required\n"; stderr != want {
		t.Fatalf("stderr = %q, want exactly %q", stderr, want)
	}
}

// TestMalformedInvocationPrintsUsage holds invocations cobra rejects before
// the command runs: the error once, then the failing command's usage once.
func TestMalformedInvocationPrintsUsage(t *testing.T) {
	home := t.TempDir()
	cases := []struct {
		name  string
		args  []string
		want  string
		usage string
	}{
		{"unknown flag", []string{"relevant", "--bogus"}, "unknown flag: --bogus", "loom relevant --for-ticket <namespaced-id> [flags]"},
		{"wrong arg count", []string{"retrospect"}, "accepts 1 arg(s), received 0", "loom retrospect <namespaced-ticket-id> [flags]"},
		{"missing required flag", []string{"tool-output"}, `required flag(s) "match", "since", "until" not set`, "loom tool-output [flags]"},
		{"unknown command", []string{"bogus"}, `unknown command "bogus" for "loom"`, "loom [command]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, stderr, code := runLoom(t, home, tc.args...)
			if code != 1 {
				t.Fatalf("exit = %d, want 1\nstderr: %q", code, stderr)
			}
			if !strings.HasPrefix(stderr, tc.want+"\n") {
				t.Fatalf("stderr = %q, want it to open with %q", stderr, tc.want)
			}
			if n := strings.Count(stderr, tc.want); n != 1 {
				t.Fatalf("stderr carries %q %d times, want once\nstderr: %q", tc.want, n, stderr)
			}
			if n := strings.Count(stderr, usageHeader); n != 1 {
				t.Fatalf("stderr carries %d usage blocks, want one\nstderr: %q", n, stderr)
			}
			if !strings.Contains(stderr, tc.usage) {
				t.Fatalf("stderr = %q, want the failing command's usage line %q", stderr, tc.usage)
			}
			if strings.Contains(stderr, "Error:") {
				t.Fatalf("stderr = %q, want cobra's prefixed copy silenced", stderr)
			}
		})
	}
}

func TestSuccessExitsZero(t *testing.T) {
	home := t.TempDir()
	for _, args := range [][]string{{"relevant", "--help"}, {"--version"}} {
		stdout, stderr, code := runLoom(t, home, args...)
		if code != 0 {
			t.Fatalf("loom %v exit = %d, want 0\nstderr: %q", args, code, stderr)
		}
		if stderr != "" {
			t.Fatalf("loom %v stderr = %q, want nothing", args, stderr)
		}
		if stdout == "" {
			t.Fatalf("loom %v printed nothing on stdout", args)
		}
	}
}
