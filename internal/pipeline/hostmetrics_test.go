package pipeline

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"loom/internal/summaries"
	"loom/internal/summarize"
	"loom/transport/shipper"
)

func hostSample(day, minute int) string {
	return fmt.Sprintf(`{"v":1,"ts":"2026-11-%02dT23:%02d:00Z","host":"engine","cpu":{"user_pct":%d,"sys_pct":2},"disk":{"error":"df failed"}}`, day, minute, minute)
}

func loadSamples(t *testing.T, dbPath string) []string {
	t.Helper()
	got, err := summaries.LoadHostSamples(dbPath, "engine",
		time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC), time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("LoadHostSamples: %v", err)
	}
	var out []string
	for _, s := range got {
		out = append(out, string(s))
	}
	return out
}

// A host's sample log travels from the producer's file, across a newsyslog
// rotation with a line written through the old inode after it, through the
// receiver and the summarizer to the read side, every sample once and in
// order; a rebuild from received/ returns the same samples.
func TestHostMetricsPipeline(t *testing.T) {
	home := t.TempDir()
	loomHome := filepath.Join(home, "loom")
	t.Setenv("HOME", home)
	t.Setenv("LOOM_HOME", loomHome)
	received := filepath.Join(loomHome, "received")
	dbPath := filepath.Join(loomHome, "summaries.db")
	logPath := filepath.Join(home, "var", "log", "engine", "samples.jsonl")

	write(t, logPath, hostSample(14, 57)+"\n"+hostSample(14, 58)+"\n")

	addr := freePort(t)
	stop := startReceiver(t, addr, received)
	defer stop()
	cfg, err := json.Marshal(map[string]any{
		"server_url": "http://" + addr, "auth_token": "tok", "notify_on_failure": false, "host_metrics_log": logPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(loomHome, "config.json"), string(cfg))

	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	shipper.Once()

	// newsyslog: link the live file to .0, rename a new one over the name.
	old, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	if err := os.Link(logPath, logPath+".0"); err != nil {
		t.Fatal(err)
	}
	write(t, logPath+".new", "")
	if err := os.Rename(logPath+".new", logPath); err != nil {
		t.Fatal(err)
	}
	if _, err := old.WriteString(hostSample(14, 59) + "\n"); err != nil {
		t.Fatal(err)
	}
	appendTo(t, logPath, hostSample(15, 0)+"\n")
	appendTo(t, logPath, hostSample(15, 1)+"\n")

	shipper.Once()
	if strings.Contains(logs.String(), "fail stage=") {
		t.Fatalf("shipper failed:\n%s", logs.String())
	}
	// The fold keys on (host, ts), so a duplicate would vanish there: the
	// received file itself must be each line once, in order.
	shipped, err := filepath.Glob(filepath.Join(received, summaries.HostMetricsAgent, "*", "samples.jsonl"))
	if err != nil || len(shipped) != 1 {
		t.Fatalf("received sample logs = %v, err %v; want one", shipped, err)
	}
	body, err := os.ReadFile(shipped[0])
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.Join([]string{hostSample(14, 57), hostSample(14, 58), hostSample(14, 59), hostSample(15, 0), hostSample(15, 1)}, "\n") + "\n"; string(body) != want {
		t.Fatalf("received:\n%s\nwant:\n%s", body, want)
	}

	if err := summarize.Run(summarize.Options{ReceivedDir: received, DBPath: dbPath}); err != nil {
		t.Fatalf("summarize: %v", err)
	}
	want := []string{hostSample(14, 57), hostSample(14, 58), hostSample(14, 59)}
	if got := loadSamples(t, dbPath); !reflect.DeepEqual(got, want) {
		t.Fatalf("samples on 2026-11-14:\n%s\nwant:\n%s\nlog:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"), logs.String())
	}
	next, err := summaries.LoadHostSamples(dbPath, "engine",
		time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC), time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC))
	if err != nil || len(next) != 2 {
		t.Fatalf("samples on 2026-11-15 = %d, err %v; want the two the new file holds", len(next), err)
	}

	if err := summarize.Run(summarize.Options{ReceivedDir: received, DBPath: dbPath, Rebuild: true}); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if got := loadSamples(t, dbPath); !reflect.DeepEqual(got, want) {
		t.Fatalf("after rebuild:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
