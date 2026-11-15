package shipper

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"loom/transport/internal/source"
	"loom/transport/internal/staging"
)

func sampleLine(n int) string {
	return fmt.Sprintf(`{"v":1,"ts":"2026-11-14T10:%02d:00Z","host":"engine","cpu":{"user_pct":%d}}`+"\n", n, n)
}

func samples(from, to int) string {
	var b strings.Builder
	for n := from; n <= to; n++ {
		b.WriteString(sampleLine(n))
	}
	return b.String()
}

func appendLog(t *testing.T, path, body string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(body); err != nil {
		t.Fatal(err)
	}
}

// rotate does what newsyslog does to the sample log: shift each <path>.<n>
// to <n+1>, hard-link the live file to .0, then rename a new empty file over
// the live name. It returns the old inode open for append, so a write after
// the rename lands in .0 the way a sampler run in flight does.
func rotate(t *testing.T, path string) *os.File {
	t.Helper()
	old, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { old.Close() })
	for n := 5; n >= 0; n-- {
		from := fmt.Sprintf("%s.%d", path, n)
		if _, err := os.Stat(from); err == nil {
			if err := os.Rename(from, fmt.Sprintf("%s.%d", path, n+1)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := os.Link(path, path+".0"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".new", nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".new", path); err != nil {
		t.Fatal(err)
	}
	return old
}

func hostMetricsTick(t *testing.T, path string) (tickCounts, string) {
	t.Helper()
	var logs bytes.Buffer
	log.SetOutput(&logs)
	defer log.SetOutput(os.Stderr)
	counts := tickCounts{}
	captureAdapters(&counts, []source.Adapter{source.HostMetrics(path)})
	if counts.captureFailed != 0 {
		t.Fatalf("capture failed: %+v\n%s", counts, logs.String())
	}
	return counts, logs.String()
}

func stagedSamples(t *testing.T) string {
	t.Helper()
	entries, err := staging.List(source.HostMetricsAgent)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].SessionID != "samples" {
		t.Fatalf("staging entries = %+v, want one samples session", entries)
	}
	b, err := os.ReadFile(entries[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Every line the producer writes reaches staging exactly once across a
// rotation, a line written through the old inode after the rename, two
// rotations between ticks, and an unterminated line held back for a tick.
func TestHostMetricsCaptureFollowsRotation(t *testing.T) {
	t.Setenv("LOOM_HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "samples.jsonl")
	appendLog(t, path, samples(1, 2))
	hostMetricsTick(t, path)
	if got := stagedSamples(t); got != samples(1, 2) {
		t.Fatalf("first tick staged %q", got)
	}

	appendLog(t, path, sampleLine(3))
	old := rotate(t, path)
	if _, err := old.WriteString(sampleLine(4)); err != nil {
		t.Fatal(err)
	}
	appendLog(t, path, sampleLine(5))
	_, logs := hostMetricsTick(t, path)
	if got := stagedSamples(t); got != samples(1, 5) {
		t.Fatalf("after rotation staged %q, want lines 1-5", got)
	}
	if !strings.Contains(logs, "rotated="+path+".0") {
		t.Errorf("no rotation line in log:\n%s", logs)
	}

	if counts, _ := hostMetricsTick(t, path); counts.captured != 0 {
		t.Errorf("idle tick captured %d", counts.captured)
	}

	appendLog(t, path, sampleLine(6))
	rotate(t, path)
	appendLog(t, path, sampleLine(7))
	rotate(t, path)
	line9 := sampleLine(9)
	appendLog(t, path, sampleLine(8)+line9[:10])
	hostMetricsTick(t, path)
	if got := stagedSamples(t); got != samples(1, 8) {
		t.Fatalf("after two rotations staged %q, want lines 1-8", got)
	}
	appendLog(t, path, line9[10:])
	hostMetricsTick(t, path)
	if got := stagedSamples(t); got != samples(1, 9) {
		t.Fatalf("after completing line 9 staged %q", got)
	}
}

// Rotated copies gone before capture saw them are a logged gap, and the live
// file is read from its start; a live file truncated in place is read again
// from 0.
func TestHostMetricsCaptureGapAndTruncate(t *testing.T) {
	t.Setenv("LOOM_HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "samples.jsonl")
	appendLog(t, path, samples(1, 1))
	hostMetricsTick(t, path)

	appendLog(t, path, sampleLine(2))
	rotate(t, path)
	appendLog(t, path, sampleLine(3))
	if err := os.Remove(path + ".0"); err != nil {
		t.Fatal(err)
	}
	_, logs := hostMetricsTick(t, path)
	if !strings.Contains(logs, "gap:") {
		t.Errorf("no gap line in log:\n%s", logs)
	}
	if got, want := stagedSamples(t), sampleLine(1)+sampleLine(3); got != want {
		t.Fatalf("after gap staged %q, want %q", got, want)
	}

	if err := os.Truncate(path, 0); err != nil {
		t.Fatal(err)
	}
	_, logs = hostMetricsTick(t, path)
	if !strings.Contains(logs, "truncated:") {
		t.Errorf("no truncate line in log:\n%s", logs)
	}
	appendLog(t, path, sampleLine(4))
	hostMetricsTick(t, path)
	if got, want := stagedSamples(t), sampleLine(1)+sampleLine(3)+sampleLine(4); got != want {
		t.Fatalf("after truncate staged %q, want %q", got, want)
	}
}

// No host_metrics_log means no adapter: an ordinary host's capture pass is
// unchanged, and nothing stages under the agent.
func TestHostMetricsAbsentConfig(t *testing.T) {
	t.Setenv("LOOM_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	counts := tickCounts{}
	capturePass(&Config{ServerURL: "https://x"}, &counts)
	entries, err := staging.List(source.HostMetricsAgent)
	if err != nil || len(entries) != 0 {
		t.Fatalf("entries=%v err=%v, want none", entries, err)
	}
	if _, err := os.Stat(followStatePath()); !os.IsNotExist(err) {
		t.Errorf("follow state written without host_metrics_log: %v", err)
	}
}
