package summarize

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"loom/internal/summaries"
)

const (
	sampleFull = `{"v":1,"ts":"2026-11-14T10:00:00Z","host":"engine","lineup":{"models":[]},"budget":{"wired_mib":41000,"budget_mib":96000,"footprint_mib":52000},"tailnet":{"peer":"studio","path":"direct","via":"100.64.0.2:41641","latency_ms":2.5},"cpu":{"user_pct":12.5,"sys_pct":4.25,"idle_pct":83.25,"load":[1.1,0.9,0.8],"ncpu":16},"gpu":{"device_pct":37,"renderer_pct":36,"tiler_pct":1,"alloc_bytes":1,"in_use_bytes":1},"memory":{"total_bytes":137438953472,"pressure_level":1},"disk":{"path":"/opt/engine/models","total_bytes":4,"used_bytes":3,"avail_bytes":994662584320},"updates":{"pending":[],"restart_required":false,"last_scan":null}}`
	sampleErr  = `{"v":1,"ts":"2026-11-14T10:01:00Z","host":"engine","cpu":{"error":"top failed"},"gpu":{"error":"ioreg failed"},"tailnet":{"peer":"studio","path":"unreachable","via":null,"latency_ms":null,"detail":"timeout"},"updates":{"pending":[],"restart_required":true,"last_scan":null}}`
	sampleV2   = `{"v":2,"ts":"2026-11-14T10:02:00Z","host":"engine","cpu":{"user_pct":"high"}}`
)

func sampleAt(minute int) string {
	return fmt.Sprintf(`{"v":1,"ts":"2026-11-14T10:%02d:00Z","host":"engine","cpu":{"user_pct":%d,"sys_pct":1}}`, minute, minute)
}

// dumpHostSamples renders every host_samples row, ordered, as one string per
// row, so two databases can be compared column for column.
func dumpHostSamples(t *testing.T, dbPath string) []string {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT host, ts, v, cpu_user_pct, cpu_sys_pct, gpu_device_pct,
		memory_pressure_level, budget_wired_mib, disk_avail_bytes, tailnet_path,
		tailnet_latency_ms, updates_restart_required, sample, source_path, source_line
		FROM host_samples ORDER BY host, ts`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		cols := make([]any, 15)
		ptrs := make([]any, 15)
		for i := range cols {
			ptrs[i] = &cols[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		out = append(out, fmt.Sprint(cols...))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func column(t *testing.T, dbPath, col, ts string) any {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var v any
	if err := db.QueryRow(`SELECT `+col+` FROM host_samples WHERE host = 'engine' AND ts = ?`, ts).Scan(&v); err != nil {
		t.Fatalf("%s at %s: %v", col, ts, err)
	}
	return v
}

// A received sample log folds into host_samples incrementally — malformed
// lines skipped, an unknown version stored whole, an unterminated line held
// for the next sweep — and `--rebuild` reproduces the same rows from
// received/, including after the file is replaced by a shorter one.
func TestSweepFoldsHostSamples(t *testing.T) {
	received := t.TempDir()
	path := filepath.Join(received, summaries.HostMetricsAgent, "engine_local", "samples.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	partial := sampleAt(4)
	body := strings.Join([]string{sampleFull, sampleErr, `{"v":1,"ts":"bad`, `{"v":1,"ts":"2026-11-14T10:03:00Z"}`, sampleV2}, "\n") + "\n" + partial[:20]
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(t.TempDir(), "summaries.db")
	if err := Run(Options{ReceivedDir: received, DBPath: dbPath, Rebuild: true}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := dumpHostSamples(t, dbPath); len(got) != 3 {
		t.Fatalf("rows = %d, want 3 (two v1, one v2):\n%s", len(got), strings.Join(got, "\n"))
	}
	for col, want := range map[string]any{
		"cpu_user_pct": 12.5, "cpu_sys_pct": 4.25, "gpu_device_pct": 37.0, "memory_pressure_level": int64(1),
		"budget_wired_mib": int64(41000), "disk_avail_bytes": int64(994662584320), "tailnet_path": "direct",
		"tailnet_latency_ms": 2.5, "updates_restart_required": int64(0), "sample": sampleFull, "source_line": int64(1),
	} {
		if got := column(t, dbPath, col, "2026-11-14T10:00:00Z"); got != want {
			t.Errorf("%s = %#v, want %#v", col, got, want)
		}
	}
	for col, want := range map[string]any{
		"cpu_user_pct": nil, "gpu_device_pct": nil, "tailnet_path": "unreachable", "tailnet_latency_ms": nil,
		"updates_restart_required": int64(1), "budget_wired_mib": nil,
	} {
		if got := column(t, dbPath, col, "2026-11-14T10:01:00Z"); got != want {
			t.Errorf("error-section sample %s = %#v, want %#v", col, got, want)
		}
	}
	for col, want := range map[string]any{"v": int64(2), "cpu_user_pct": nil, "sample": sampleV2, "source_line": int64(5)} {
		if got := column(t, dbPath, col, "2026-11-14T10:02:00Z"); got != want {
			t.Errorf("v2 sample %s = %#v, want %#v", col, got, want)
		}
	}

	// The rest of the held-back line and one more arrive; the next sweep reads
	// only them.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(partial[20:] + "\n" + sampleAt(5) + "\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if err := Run(Options{ReceivedDir: received, DBPath: dbPath}); err != nil {
		t.Fatalf("incremental Run: %v", err)
	}
	incremental := dumpHostSamples(t, dbPath)
	if len(incremental) != 5 {
		t.Fatalf("rows after append = %d, want 5", len(incremental))
	}
	if got := column(t, dbPath, "source_line", "2026-11-14T10:05:00Z"); got != int64(7) {
		t.Errorf("appended sample source_line = %#v, want 7", got)
	}
	st, err := summaries.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	current, err := st.HostSamplesCurrent(path, info.Size())
	st.Close()
	if err != nil || !current {
		t.Fatalf("log not current after sweep: current=%t err=%v", current, err)
	}

	if err := Run(Options{ReceivedDir: received, DBPath: dbPath, Rebuild: true}); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if rebuilt := dumpHostSamples(t, dbPath); !reflect.DeepEqual(rebuilt, incremental) {
		t.Fatalf("rebuild differs from incremental fold:\nrebuilt:\n%s\nincremental:\n%s",
			strings.Join(rebuilt, "\n"), strings.Join(incremental, "\n"))
	}

	// A file shorter than the recorded offset is re-read from 0, dropping
	// the rows only the longer file held.
	if err := os.WriteFile(path, []byte(sampleAt(7)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Run(Options{ReceivedDir: received, DBPath: dbPath}); err != nil {
		t.Fatalf("Run after shrink: %v", err)
	}
	shrunk := dumpHostSamples(t, dbPath)
	if err := Run(Options{ReceivedDir: received, DBPath: dbPath, Rebuild: true}); err != nil {
		t.Fatalf("rebuild after shrink: %v", err)
	}
	if rebuilt := dumpHostSamples(t, dbPath); len(shrunk) != 1 || !reflect.DeepEqual(rebuilt, shrunk) {
		t.Fatalf("after shrink: incremental=%v rebuilt=%v, want one identical row", shrunk, rebuilt)
	}
}
