package cmd

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loom/internal/summaries"
)

// TestLoomProcess is the child runLoom re-executes: the real Execute, so the
// stderr text and exit status asserted are the binary's own rather than what
// cobra hands back to a caller.
func TestLoomProcess(t *testing.T) {
	if os.Getenv("LOOM_TEST_PROCESS") != "1" {
		return
	}
	rootCmd.SetArgs(flag.Args())
	Execute()
}

// runLoom runs `loom <args>` in a child process under an isolated LOOM_HOME
// and returns its stdout, stderr and exit code. The knowledge, extractor and
// tk roots are moved off the developer's too, so a regressed guard lands on
// empty trees rather than on real state.
func runLoom(t *testing.T, home string, args ...string) (string, string, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], append([]string{"-test.run=^TestLoomProcess$", "--"}, args...)...)
	cmd.Env = append(os.Environ(),
		"LOOM_TEST_PROCESS=1",
		"LOOM_HOME="+home,
		"LOOM_KNOWLEDGE_ROOT="+filepath.Join(t.TempDir(), "knowledge"),
		"LOOM_EXTRACTORS_DIR="+filepath.Join(t.TempDir(), "absent"),
		"TK_STORE_ROOT="+t.TempDir(),
		"HOME="+t.TempDir(),
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		t.Fatalf("loom %v did not exit within the timeout\nstdout: %q\nstderr: %q", args, stdout.String(), stderr.String())
	}
	code := 0
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		code = exit.ExitCode()
	} else if err != nil {
		t.Fatalf("run loom %v: %v", args, err)
	}
	return stdout.String(), stderr.String(), code
}

func missingStoreMessage(home string) string {
	return "summaries.db not found at " + filepath.Join(home, "summaries.db") + " — run `loom summarize`\n"
}

func TestUIRequiresSummaryStore(t *testing.T) {
	home := t.TempDir()
	stdout, stderr, code := runLoom(t, home, "ui")
	if code == 0 {
		t.Fatal("loom ui exited 0 with no summaries.db")
	}
	if want := missingStoreMessage(home); stderr != want {
		t.Fatalf("stderr = %q, want exactly %q", stderr, want)
	}
	// Nothing at all, so not the alt-screen's smcup either: the refusal has to
	// land before the program draws anything.
	if stdout != "" {
		t.Fatalf("stdout = %q, want nothing written before the refusal", stdout)
	}
}

func TestRetrospectRequiresSummaryStore(t *testing.T) {
	home := t.TempDir()
	_, stderr, code := runLoom(t, home, "retrospect", "loom/some-ticket-0001")
	if code == 0 {
		t.Fatal("loom retrospect exited 0 with no summaries.db")
	}
	if want := missingStoreMessage(home); stderr != want {
		t.Fatalf("stderr = %q, want exactly %q", stderr, want)
	}

	// With the DB present the guard stands aside: the run goes on to the next
	// precondition, the extractor script this environment withholds.
	st, err := summaries.Open(filepath.Join(home, "summaries.db"))
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	_, stderr, code = runLoom(t, home, "retrospect", "loom/some-ticket-0001")
	if code == 0 {
		t.Fatal("loom retrospect exited 0 without an extractor script")
	}
	if strings.Contains(stderr, "summaries.db not found") {
		t.Fatalf("stderr = %q, refused a store that is present", stderr)
	}
	if !strings.Contains(stderr, "not found — point LOOM_EXTRACTORS_DIR at a loom checkout") {
		t.Fatalf("stderr = %q, want the extractor-script refusal that follows the guard", stderr)
	}
}
