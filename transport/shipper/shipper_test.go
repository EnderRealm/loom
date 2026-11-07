package shipper

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"loom/internal/config"
	"loom/transport/internal/source"
)

// A spool that cannot be read hides the hosted agents' sessions, which the
// report has to say, but not the operator's own backlog. The spool root is a
// file, so listing it fails with something other than not-exist.
func TestPrintHealthUnreadableSpoolCountsOperatorSessions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("LOOM_HOME", t.TempDir())
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	const project = "-Users-steve-code-loom"
	const session = "55555555-5555-4555-8555-555555555555"
	dir := filepath.Join(home, ".claude", "projects", project)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := `{"type":"user","cwd":"/Users/steve/code/loom","sessionId":"` + session + `"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, session+".jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	spool := config.ClaudeSpoolDir()
	if err := os.MkdirAll(filepath.Dir(spool), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(spool, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	var health bytes.Buffer
	if err := PrintHealth(&health); err != nil {
		t.Fatal(err)
	}
	if want := source.ClaudeAgent + " / " + project + ": 1 session,"; !strings.Contains(health.String(), want) {
		t.Errorf("health is missing %q:\n%s", want, health.String())
	}
	if want := "fail stage=status agent=" + source.ClaudeAgent; !strings.Contains(logs.String(), want) {
		t.Errorf("logs are missing %q:\n%s", want, logs.String())
	}
}
