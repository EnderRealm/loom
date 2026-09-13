package shipper

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loom/internal/config"
)

func writeConfig(t *testing.T, body string) {
	t.Helper()
	t.Setenv("LOOM_HOME", t.TempDir())
	if err := os.MkdirAll(filepath.Dir(config.Path()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.Path(), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// Seconds absent keeps the minute cadence; present and positive, they win
// over minutes; zero or negative is a config error naming the field.
func TestLoadConfigIntervals(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		interval   time.Duration
		summarizer time.Duration
		wantErr    string
	}{
		{"minutes default", `{"server_url":"http://x"}`, 10 * time.Minute, 30 * time.Second, ""},
		{"minutes explicit", `{"server_url":"http://x","interval_minutes":3}`, 3 * time.Minute, 30 * time.Second, ""},
		{"seconds override minutes", `{"server_url":"http://x","interval_minutes":3,"interval_seconds":5}`, 5 * time.Second, 30 * time.Second, ""},
		{"summarizer seconds", `{"server_url":"http://x","summarizer_interval_seconds":5}`, 10 * time.Minute, 5 * time.Second, ""},
		{"interval zero", `{"server_url":"http://x","interval_seconds":0}`, 0, 0, "interval_seconds"},
		{"interval negative", `{"server_url":"http://x","interval_seconds":-1}`, 0, 0, "interval_seconds"},
		{"summarizer zero", `{"server_url":"http://x","summarizer_interval_seconds":0}`, 0, 0, "summarizer_interval_seconds"},
		{"summarizer negative", `{"server_url":"http://x","summarizer_interval_seconds":-30}`, 0, 0, "summarizer_interval_seconds"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			writeConfig(t, tc.body)
			cfg, err := LoadConfig()
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) || !strings.Contains(err.Error(), config.Path()) {
					t.Fatalf("LoadConfig err = %v, want one naming %s and %s", err, tc.wantErr, config.Path())
				}
				return
			}
			if err != nil {
				t.Fatalf("LoadConfig: %v", err)
			}
			if got := cfg.Interval(); got != tc.interval {
				t.Errorf("Interval = %s, want %s", got, tc.interval)
			}
			if got := cfg.SummarizerInterval(); got != tc.summarizer {
				t.Errorf("SummarizerInterval = %s, want %s", got, tc.summarizer)
			}
		})
	}
}

// The server role has no config.json, or one with no server_url; the
// summarizer cadence still resolves, and the seconds check still applies.
func TestSummarizerIntervalWithoutShipperConfig(t *testing.T) {
	t.Setenv("LOOM_HOME", t.TempDir())
	got, err := SummarizerInterval()
	if err != nil || got != DefaultSummarizerInterval {
		t.Fatalf("no config: %s, %v; want %s", got, err, DefaultSummarizerInterval)
	}

	writeConfig(t, `{"summarizer_interval_seconds":5}`)
	if _, err := LoadConfig(); err == nil {
		t.Fatal("LoadConfig accepted a config with no server_url")
	}
	got, err = SummarizerInterval()
	if err != nil || got != 5*time.Second {
		t.Fatalf("without server_url: %s, %v; want 5s", got, err)
	}

	writeConfig(t, `{"summarizer_interval_seconds":-5}`)
	if _, err := SummarizerInterval(); err == nil || !strings.Contains(err.Error(), "summarizer_interval_seconds") {
		t.Fatalf("negative seconds: err = %v", err)
	}
}

// Cadences resolves both tick cadences for a reader on either role: the
// defaults with no file, the configured values without a server_url.
func TestCadences(t *testing.T) {
	t.Setenv("LOOM_HOME", t.TempDir())
	ship, summarize, err := Cadences()
	if err != nil || ship != 10*time.Minute || summarize != DefaultSummarizerInterval {
		t.Fatalf("no config: %s, %s, %v; want 10m0s, %s", ship, summarize, err, DefaultSummarizerInterval)
	}

	writeConfig(t, `{"interval_seconds":30,"summarizer_interval_seconds":5}`)
	ship, summarize, err = Cadences()
	if err != nil || ship != 30*time.Second || summarize != 5*time.Second {
		t.Fatalf("without server_url: %s, %s, %v; want 30s, 5s", ship, summarize, err)
	}

	writeConfig(t, `{"interval_seconds":0}`)
	if _, _, err := Cadences(); err == nil || !strings.Contains(err.Error(), "interval_seconds") {
		t.Fatalf("zero seconds: err = %v", err)
	}
}
