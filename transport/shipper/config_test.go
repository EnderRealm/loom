package shipper

import (
	"errors"
	"fmt"
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
		{"minutes default", `{"server_url":"https://x"}`, 10 * time.Minute, 30 * time.Second, ""},
		{"minutes explicit", `{"server_url":"https://x","interval_minutes":3}`, 3 * time.Minute, 30 * time.Second, ""},
		{"seconds override minutes", `{"server_url":"https://x","interval_minutes":3,"interval_seconds":5}`, 5 * time.Second, 30 * time.Second, ""},
		{"summarizer seconds", `{"server_url":"https://x","summarizer_interval_seconds":5}`, 10 * time.Minute, 5 * time.Second, ""},
		{"interval zero", `{"server_url":"https://x","interval_seconds":0}`, 0, 0, "interval_seconds"},
		{"interval negative", `{"server_url":"https://x","interval_seconds":-1}`, 0, 0, "interval_seconds"},
		{"summarizer zero", `{"server_url":"https://x","summarizer_interval_seconds":0}`, 0, 0, "summarizer_interval_seconds"},
		{"summarizer negative", `{"server_url":"https://x","summarizer_interval_seconds":-30}`, 0, 0, "summarizer_interval_seconds"},
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

// Every refusal is one LoadConfig reports as ErrTransportRefused, naming the
// config file. The opt-out reaches http and nothing else, and a host that
// only looks local is judged by what it dials.
func TestLoadConfigTransport(t *testing.T) {
	cases := []struct {
		name  string
		url   string
		optIn bool
		want  bool
	}{
		{"https", "https://loom.example", false, true},
		{"https port and path", "https://loom.example:8443/base/", false, true},
		{"https upper-case scheme", "HTTPS://loom.example", false, true},
		{"http localhost", "http://localhost:8765", false, true},
		{"http 127.0.0.1", "http://127.0.0.1:8765", false, true},
		{"http 127/8", "http://127.1.2.3:8765", false, true},
		{"http ipv6 loopback", "http://[::1]:8765", false, true},
		{"http remote opted in", "http://loom.example:8765", true, true},

		{"http remote", "http://loom.example:8765", false, false},
		{"http private address", "http://192.168.1.5:8765", false, false},
		{"http tailnet address", "http://100.64.0.1:8765", false, false},
		{"http wildcard", "http://0.0.0.0:8765", false, false},
		{"loopback as a subdomain", "http://127.0.0.1.evil.example", false, false},
		{"localhost as a subdomain", "http://localhost.evil.example", false, false},
		{"loopback as userinfo", "http://127.0.0.1:8765@evil.example", false, false},
		{"localhost as userinfo", "http://localhost@evil.example", false, false},
		{"no scheme", "loom.example:8765", false, false},
		{"scheme-relative", "//loom.example", false, false},
		{"unsupported scheme", "ftp://loom.example", false, false},
		{"unsupported scheme opted in", "ftp://loom.example", true, false},
		{"ws opted in", "ws://loom.example", true, false},
		{"https no host", "https://", false, false},
		{"https opaque", "https:loom.example", false, false},
		{"http no host opted in", "http://", true, false},
		{"unparseable", "http://bad host:8765", false, false},
		{"unparseable opted in", "http://bad host:8765", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			writeConfig(t, fmt.Sprintf(`{"server_url":%q,"allow_plaintext_transcripts_and_token":%t}`, tc.url, tc.optIn))
			_, err := LoadConfig()
			if tc.want {
				if err != nil {
					t.Fatalf("LoadConfig: %v", err)
				}
				return
			}
			if !errors.Is(err, ErrTransportRefused) || !strings.Contains(err.Error(), config.Path()) {
				t.Fatalf("LoadConfig err = %v, want a refusal naming %s", err, config.Path())
			}
		})
	}
}

// The refusal is the only thing an operator sees on the failure path, so it
// says what is exposed and both ways out, and never echoes credentials a
// server_url carries, parseable or not.
func TestRefusalExplainsItself(t *testing.T) {
	writeConfig(t, `{"server_url":"http://user:hunter2@loom.example:8765"}`)
	_, err := LoadConfig()
	if err == nil {
		t.Fatal("LoadConfig accepted plaintext to a remote host")
	}
	for _, want := range []string{"loom.example:8765", "transcripts", "bearer token", "https://", allowPlaintextKey} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q does not mention %q", err, want)
		}
	}
	for _, body := range []string{`{"server_url":"http://user:hunter2@loom.example:8765"}`, `{"server_url":"http://user:hunter2@bad host:8765"}`} {
		writeConfig(t, body)
		if _, err := LoadConfig(); err == nil || strings.Contains(err.Error(), "hunter2") {
			t.Errorf("refusal of %s = %v, want one without the password", body, err)
		}
	}
}

// receiver_addr is read on the server role, which has no server_url, and an
// address install could not health-check is refused before it reaches a plist.
func TestReceiverAddr(t *testing.T) {
	t.Setenv("LOOM_HOME", t.TempDir())
	if got, err := ReceiverAddr(); got != "" || err != nil {
		t.Fatalf("no config: %q, %v", got, err)
	}
	writeConfig(t, `{"summarizer_interval_seconds":5}`)
	if got, err := ReceiverAddr(); got != "" || err != nil {
		t.Fatalf("no field: %q, %v", got, err)
	}
	for _, addr := range []string{"127.0.0.1:8765", ":9000", "[::1]:8765", "0.0.0.0:8765"} {
		writeConfig(t, fmt.Sprintf(`{"receiver_addr":%q}`, addr))
		if got, err := ReceiverAddr(); got != addr || err != nil {
			t.Errorf("%s: %q, %v", addr, got, err)
		}
	}
	for _, addr := range []string{"8765", "127.0.0.1", "127.0.0.1:http", "127.0.0.1:0", "127.0.0.1:70000"} {
		writeConfig(t, fmt.Sprintf(`{"receiver_addr":%q}`, addr))
		if _, err := ReceiverAddr(); err == nil || !strings.Contains(err.Error(), "receiver_addr") {
			t.Errorf("%s: err = %v", addr, err)
		}
	}
}
