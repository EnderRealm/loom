package receiver

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode"

	"loom/internal/config"
	"loom/transport/internal/source"
)

const (
	spoolProject = "-Users-weftagent1-work-loom"
	spoolSession = "33333333-3333-4333-8333-333333333333"
	spoolShared  = "44444444-4444-4444-8444-444444444444"
)

func replaceFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
}

func assertFile(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("%s = %q, want %q", path, got, want)
	}
}

// serveSpoolReceiver runs a receiver in this process, which is what lets a test
// read its log, and points the shipper's config at it.
func serveSpoolReceiver(t *testing.T, loomHome string) (*httptest.Server, string) {
	t.Helper()
	storage := t.TempDir()
	srv := &server{storage: storage, authToken: "test-token"}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(http.ResponseWriter, *http.Request) {})
	mux.HandleFunc("/v1/ingest", srv.handleIngest)
	httpServer := httptest.NewServer(mux)
	t.Cleanup(httpServer.Close)
	cfg, _ := json.Marshal(map[string]any{"server_url": httpServer.URL, "auth_token": "test-token", "notify_on_failure": false})
	if err := os.WriteFile(filepath.Join(loomHome, "config.json"), cfg, 0o600); err != nil {
		t.Fatal(err)
	}
	return httpServer, storage
}

// A hosted run's worktree is usually gone by the time its transcripts are
// captured, which is why cwd names a directory that does not exist here. The
// session must land in the received tree anyway, under claude-code, exactly
// byte for byte; a later copy lands by rename and ships only what it added.
func TestSpooledTranscriptsReachTheReceiver(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	loomHome := t.TempDir()
	t.Setenv("LOOM_HOME", loomHome)
	spool := config.ClaudeSpoolDir()

	cwd := filepath.Join(t.TempDir(), "removed-worktree")
	first := `{"type":"user","cwd":"` + cwd + `"}` + "\n"
	top := filepath.Join(spool, spoolProject, spoolSession+".jsonl")
	replaceFile(t, top, first)
	sub := filepath.Join(spool, spoolProject, spoolSession, "subagents", "agent-a0e0.jsonl")
	subBody := `{"type":"user","cwd":"` + cwd + `","marker":"subagent"}` + "\n"
	replaceFile(t, sub, subBody)
	replaceFile(t, strings.TrimSuffix(sub, ".jsonl")+".meta.json", `{"agentType":"reviewer"}`)

	// A link aimed at something only the operator can read.
	secret := filepath.Join(t.TempDir(), "id_ed25519")
	replaceFile(t, secret, `{"operator-secret":true}`+"\n")
	if err := os.Symlink(secret, filepath.Join(spool, spoolProject, "stolen.jsonl")); err != nil {
		t.Fatal(err)
	}

	// A spooled file claiming a session the operator's agent owns, longer than
	// the real one so its tail would ship if it shared the cursor.
	operatorBody := `{"type":"user","cwd":"/Users/steve/code/loom"}` + "\n"
	replaceFile(t, filepath.Join(home, ".claude", "projects", "-Users-steve-code-loom", spoolShared+".jsonl"), operatorBody)
	replaceFile(t, filepath.Join(spool, spoolProject, spoolShared+".jsonl"),
		operatorBody+`{"operator-session":"hijacked"}`+"\n")

	_, storage := serveSpoolReceiver(t, loomHome)

	if log := runCursorCapture(t); !strings.Contains(log, "captured=3 capture-failed=0 shipped=3 skipped=0 failed=0") {
		t.Fatalf("first tick:\n%s", log)
	}

	received := filepath.Join(storage, source.ClaudeAgent)
	assertFile(t, filepath.Join(received, spoolProject, spoolSession+".jsonl"), first)
	assertFile(t, filepath.Join(received, spoolProject, spoolSession, "subagents", "agent-a0e0.jsonl"), subBody)
	assertFile(t, filepath.Join(received, "-Users-steve-code-loom", spoolShared+".jsonl"), operatorBody)

	var identity struct {
		GitRemote string `json:"git_remote"`
		Cwd       string `json:"cwd"`
	}
	meta, err := os.ReadFile(filepath.Join(received, spoolProject, spoolSession+".meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(meta, &identity); err != nil {
		t.Fatal(err)
	}
	if identity.Cwd != cwd || identity.GitRemote != "" {
		t.Fatalf("identity = %+v, want the removed cwd and no remote", identity)
	}
	subMeta, err := os.ReadFile(filepath.Join(received, spoolProject, spoolSession, "subagents", "agent-a0e0.subagent.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(subMeta), `"agent_type":"reviewer"`) {
		t.Fatalf("subagent sidecar = %s", subMeta)
	}

	second := first + `{"type":"assistant","cwd":"` + cwd + `"}` + "\n"
	replaceFile(t, top, second)
	if log := runCursorCapture(t); !strings.Contains(log, "captured=1 capture-failed=0 shipped=1") {
		t.Fatalf("second tick:\n%s", log)
	}
	assertFile(t, filepath.Join(received, spoolProject, spoolSession+".jsonl"), second)

	err = filepath.WalkDir(storage, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, leaked := range []string{"operator-secret", "hijacked"} {
			if strings.Contains(string(body), leaked) {
				t.Errorf("%s reached %s", leaked, path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// The agent writes cwd, and through a repo at cwd it writes git_remote too, so
// a value carrying a newline must stay on the one ingest line the receiver logs
// for it. The sidecar is what readers group by and keeps the value as claimed.
func TestSpooledIdentityCannotForgeReceiverLogLines(t *testing.T) {
	const forged = "\n2026/11/06 12:00:00 ingest from=forged agent=claude-code session=forged"

	check := func(t *testing.T, cwd, remote, wantTag string) {
		t.Helper()
		t.Setenv("HOME", t.TempDir())
		loomHome := t.TempDir()
		t.Setenv("LOOM_HOME", loomHome)
		httpServer, storage := serveSpoolReceiver(t, loomHome)

		var logs bytes.Buffer
		log.SetOutput(&logs)
		t.Cleanup(func() { log.SetOutput(os.Stderr) })

		line, err := json.Marshal(map[string]string{"type": "user", "cwd": cwd})
		if err != nil {
			t.Fatal(err)
		}
		replaceFile(t, filepath.Join(config.ClaudeSpoolDir(), spoolProject, spoolSession+".jsonl"), string(line)+"\n")

		if out := runCursorCapture(t); !strings.Contains(out, "captured=1 capture-failed=0 shipped=1") {
			t.Fatalf("tick:\n%s", out)
		}

		// Close waits out the handler, so logs is safe to read below.
		httpServer.Close()
		got := logs.String()
		if n := strings.Count(got, "\n"); n != 1 {
			t.Fatalf("receiver log has %d lines, want 1:\n%s", n, got)
		}
		if strings.ContainsFunc(strings.TrimSuffix(got, "\n"), unicode.IsControl) {
			t.Fatalf("receiver log carries a raw control character:\n%q", got)
		}
		if !strings.Contains(got, " identity="+wantTag+" ") {
			t.Fatalf("receiver log missing identity=%s:\n%s", wantTag, got)
		}

		var identity struct {
			GitRemote string `json:"git_remote"`
			Cwd       string `json:"cwd"`
		}
		meta, err := os.ReadFile(filepath.Join(storage, source.ClaudeAgent, spoolProject, spoolSession+".meta.json"))
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(meta, &identity); err != nil {
			t.Fatal(err)
		}
		if identity.Cwd != cwd || identity.GitRemote != remote {
			t.Fatalf("identity = %+v, want cwd %q and remote %q", identity, cwd, remote)
		}
	}

	t.Run("cwd", func(t *testing.T) {
		cwd := filepath.Join(t.TempDir(), "removed-worktree"+forged)
		check(t, cwd, "", "cwd="+strconv.Quote(cwd))
	})

	t.Run("git_remote", func(t *testing.T) {
		cwd := t.TempDir()
		remote := "https://example.com/repo.git" + forged
		for _, args := range [][]string{{"init", "-q"}, {"config", "remote.origin.url", remote}} {
			if out, err := exec.Command("git", append([]string{"-C", cwd}, args...)...).CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v\n%s", args, err, out)
			}
		}
		check(t, cwd, remote, "remote="+strconv.Quote(remote))
	})
}
