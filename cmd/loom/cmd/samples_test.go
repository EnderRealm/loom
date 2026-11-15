package cmd

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"loom/internal/summaries"
	"loom/internal/summarize"
)

func runSamples(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newSamplesCmd()
	cmd.SetArgs(args)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	err := cmd.Execute()
	return out.String(), err
}

// The command prints the folded samples of one host in [since, until),
// ordered by ts and byte for byte as the producer wrote them; an empty window
// is [] and a missing summaries.db an error.
func TestSamplesCommand(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LOOM_HOME", home)
	dbPath := filepath.Join(home, "summaries.db")

	if _, err := runSamples(t, "--host", "engine", "--since", "2026-11-14", "--until", "2026-11-15"); err == nil {
		t.Fatal("missing summaries.db: want an error")
	}

	// The sampler's own write order is not ts order across hosts, and the
	// lineup error carries characters json.Marshal would escape.
	e1 := `{"v":1,"ts":"2026-11-14T10:00:00Z","host":"engine","lineup":{"models":[],"error":"curl: (7) <refused> & retried"}}`
	e2 := `{"v":1,"ts":"2026-11-14T10:01:00Z","host":"engine","cpu":{"user_pct":3}}`
	e3 := `{"v":1,"ts":"2026-11-14T10:02:00Z","host":"engine","cpu":{"user_pct":4}}`
	other := `{"v":1,"ts":"2026-11-14T10:01:30Z","host":"other","cpu":{"user_pct":9}}`
	writeFixture(t, filepath.Join(home, "received", summaries.HostMetricsAgent, "engine_local", "samples.jsonl"),
		strings.Join([]string{e2, e1, e3}, "\n")+"\n")
	writeFixture(t, filepath.Join(home, "received", summaries.HostMetricsAgent, "other_local", "samples.jsonl"), other+"\n")
	if err := summarize.Run(summarize.Options{ReceivedDir: filepath.Join(home, "received"), DBPath: dbPath}); err != nil {
		t.Fatal(err)
	}

	got, err := runSamples(t, "--host", "engine", "--since", "2026-11-14T10:00:00Z", "--until", "2026-11-14T10:02:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if want := "[" + e1 + "," + e2 + "]\n"; got != want {
		t.Fatalf("output:\n%s\nwant:\n%s", got, want)
	}
	if !json.Valid([]byte(got)) {
		t.Fatalf("output is not JSON: %s", got)
	}

	// A fractional bound still excludes the sample before it.
	got, err = runSamples(t, "--host", "engine", "--since", "2026-11-14T10:00:00.5Z", "--until", "2026-11-14T11:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if want := "[" + e2 + "," + e3 + "]\n"; got != want {
		t.Fatalf("fractional since:\n%s\nwant:\n%s", got, want)
	}

	for _, args := range [][]string{
		{"--host", "engine", "--since", "2026-11-13T00:00:00Z", "--until", "2026-11-14T10:00:00Z"},
		{"--host", "nobody", "--since", "2026-11-14", "--until", "2026-11-15"},
	} {
		got, err := runSamples(t, args...)
		if err != nil || got != "[]\n" {
			t.Errorf("%v: output %q err %v, want []", args, got, err)
		}
	}
}
