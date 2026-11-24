package cmd

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"loom/internal/config"
	"loom/internal/extract"
	"loom/internal/launchd"
	"loom/internal/updater"
	"loom/transport/cursor"
	"loom/transport/receiver"
	"loom/transport/shipper"
)

const fakeLaunchctl = `#!/bin/sh
[ "$1" = print ] || exit 64
f="$LOOM_TEST_LAUNCHD/$2"
[ -f "$f.exit" ] && { echo "launchctl is broken" >&2; exit "$(cat "$f.exit")"; }
[ -f "$f" ] || { echo "Could not find service" >&2; exit 113; }
cat "$f"
`

const syncedState = `{"last_success_ts":"2026-11-24T10:00:00Z","failure_active":true}`

var logTime = time.Date(2026, time.November, 24, 9, 30, 0, 0, time.UTC)

// statusHost is a host isolated from this one: HOME, LOOM_HOME, the system
// plist dir and launchctl are the test's.
type statusHost struct {
	home, systemPlists, launchd string
}

func newStatusHost(t *testing.T) statusHost {
	t.Helper()
	h := statusHost{home: t.TempDir(), systemPlists: t.TempDir(), launchd: t.TempDir()}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "launchctl"), []byte(fakeLaunchctl), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", h.home)
	t.Setenv("LOOM_HOME", filepath.Join(h.home, "loom"))
	t.Setenv("LOOM_TEST_LAUNCHD", h.launchd)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	orig := systemPlistPath
	systemPlistPath = func(label string) string { return filepath.Join(h.systemPlists, label+".plist") }
	t.Cleanup(func() { systemPlistPath = orig })
	return h
}

func (h statusHost) agentPlist(label string) string {
	return filepath.Join(h.home, "Library", "LaunchAgents", label+".plist")
}

// job is where the fake launchctl reads a job's print output.
func (h statusHost) job(d launchd.Domain, label string) string {
	return filepath.Join(h.launchd, d.String(), label)
}

// printOutput is the shape of a loaded job's `launchctl print`; exit is the
// line reporting its last exit.
func printOutput(state, exit string) string {
	return "gui/501/com.loom.x = {\n" +
		"\tactive count = 1\n" +
		"\tstate = " + state + "\n\n" +
		"\tprogram = /Users/x/.local/bin/loom\n" +
		"\targuments = {\n\t\t/Users/x/.local/bin/loom\n\t}\n\n" +
		"\truns = 2\n" +
		"\t" + exit + "\n\n" +
		"\tresource coalition = {\n\t\tstate = active\n\t}\n" +
		"}\n"
}

func put(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func putLog(t *testing.T, path string) {
	t.Helper()
	put(t, path, "log\n")
	if err := os.Chtimes(path, logTime, logTime); err != nil {
		t.Fatal(err)
	}
}

func stage(t *testing.T, session string, shipped int64) {
	t.Helper()
	const agent, body = "claude-code", "{}\n"
	put(t, filepath.Join(config.TransportDir(), "staging", agent, "project", session+".jsonl"), body)
	if err := cursor.Write(cursor.KindShip, agent, session, shipped); err != nil {
		t.Fatal(err)
	}
}

func notifyState() string { return filepath.Join(config.TransportDir(), "notify.state") }

// executeStatusJSON drives `loom status --json` through the root command and
// returns its stdout and error.
func executeStatusJSON(t *testing.T) (string, error) {
	t.Helper()
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		f := statusCmd.Flags().Lookup("json")
		_ = f.Value.Set(f.DefValue)
		f.Changed = false
	})
	var runErr error
	raw := captureStdout(t, func(io.Writer) error {
		rootCmd.SetArgs([]string{"status", "--json"})
		runErr = rootCmd.Execute()
		return nil
	})
	return raw, runErr
}

// runStatusJSON requires `loom status --json` to succeed with exactly one JSON
// object on stdout.
func runStatusJSON(t *testing.T) (string, statusJSON) {
	t.Helper()
	raw, err := executeStatusJSON(t)
	if err != nil {
		t.Fatalf("status --json: %v", err)
	}
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	var report statusJSON
	if err := dec.Decode(&report); err != nil {
		t.Fatalf("decode: %v\n%s", err, raw)
	}
	if _, err := dec.Token(); err != io.EOF {
		t.Fatalf("stdout holds more than one JSON object (%v):\n%s", err, raw)
	}
	if len(report.Components) != 5 {
		t.Fatalf("components = %d, want all five:\n%s", len(report.Components), raw)
	}
	return raw, report
}

func sameJSON(t *testing.T, got, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal([]byte(got), &g); err != nil {
		t.Fatalf("got: %v\n%s", err, got)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("want: %v\n%s", err, want)
	}
	if !reflect.DeepEqual(g, w) {
		t.Fatalf("status --json =\n%s\nwant\n%s", got, want)
	}
}

func TestStatusJSON(t *testing.T) {
	t.Run("every reading", func(t *testing.T) {
		h := newStatusHost(t)
		if err := config.WriteRole(config.RoleServer); err != nil {
			t.Fatal(err)
		}
		for _, label := range []string{receiver.AgentLabel, summarizerLabel, extract.AgentLabel, updater.AgentLabel} {
			put(t, h.agentPlist(label), "plist\n")
		}
		// A boot-time daemon: the system plist is the one reported, and it alone
		// makes sync health meaningful.
		put(t, systemPlistPath(shipper.AgentLabel), "plist\n")

		put(t, h.job(launchd.User, receiver.AgentLabel), printOutput("running", "last exit code = (never exited)"))
		put(t, h.job(launchd.User, summarizerLabel), printOutput("spawn scheduled", "last exit code = 78: EX_CONFIG"))
		put(t, h.job(launchd.User, extract.AgentLabel), printOutput("not running", "last terminating signal = Killed: 9"))
		put(t, h.job(launchd.System, shipper.AgentLabel), printOutput("running", "last exit code = 1"))
		// Nothing for the updater: installed, never loaded, no log.
		putLog(t, filepath.Join(config.Home(), "receiver.log"))
		putLog(t, filepath.Join(config.Home(), "summarizer.log"))
		putLog(t, extract.LogPath())
		putLog(t, filepath.Join(config.TransportDir(), "shipper.log"))
		put(t, notifyState(), syncedState)
		stage(t, "shipped", 3)
		stage(t, "unshipped", 0)

		raw, _ := runStatusJSON(t)
		sameJSON(t, raw, `{
			"role": "server",
			"role_error": null,
			"components": {
				"receiver": {"installed": true, "expected": true, "state": "running",
					"last_exit_code": null, "last_exit_signal": null, "last_exit_reason": null,
					"last_activity": "2026-11-24T09:30:00Z", "error": null},
				"summarizer": {"installed": true, "expected": true, "state": "spawn scheduled",
					"last_exit_code": 78, "last_exit_signal": null, "last_exit_reason": null,
					"last_activity": "2026-11-24T09:30:00Z", "error": null},
				"extractor": {"installed": true, "expected": true, "state": "not running",
					"last_exit_code": null, "last_exit_signal": "Killed: 9", "last_exit_reason": null,
					"last_activity": "2026-11-24T09:30:00Z", "error": null},
				"shipper": {"installed": true, "expected": false, "state": "running",
					"last_exit_code": 1, "last_exit_signal": null, "last_exit_reason": null,
					"last_activity": "2026-11-24T09:30:00Z", "error": null},
				"updater": {"installed": true, "expected": true, "state": "not-loaded",
					"last_exit_code": null, "last_exit_signal": null, "last_exit_reason": null,
					"last_activity": null, "error": null}
			},
			"sync": {"last_success": "2026-11-24T10:00:00Z", "unshipped_sessions": 1,
				"failure_active": true, "error": null}
		}`)
	})

	// A remote missing its one component, beside components its role does not
	// expect, and no shipper for sync health to describe.
	t.Run("not installed", func(t *testing.T) {
		newStatusHost(t)
		if err := config.WriteRole(config.RoleRemote); err != nil {
			t.Fatal(err)
		}
		_, report := runStatusJSON(t)
		for name, c := range report.Components {
			if c.State != stateNotInstalled || c.Installed == nil || *c.Installed || c.Error != nil {
				t.Errorf("%s = %+v, want not installed and no error", name, c)
			}
			if want := name == "shipper"; c.Expected == nil || *c.Expected != want {
				t.Errorf("%s expected = %v, want %v", name, c.Expected, want)
			}
		}
		if report.Sync != nil {
			t.Errorf("sync = %+v, want null without a shipper", report.Sync)
		}
	})

	// The updater is expected wherever its plist is, so a boot-time one is not
	// exempted by having no LaunchAgent.
	t.Run("system updater", func(t *testing.T) {
		h := newStatusHost(t)
		if err := config.WriteRole(config.RoleRemote); err != nil {
			t.Fatal(err)
		}
		put(t, systemPlistPath(updater.AgentLabel), "plist\n")
		put(t, h.job(launchd.System, updater.AgentLabel), printOutput("running", "last exit code = 0"))

		_, report := runStatusJSON(t)
		c := report.Components["updater"]
		if c.Installed == nil || !*c.Installed || c.Expected == nil || !*c.Expected || c.State != "running" || c.Error != nil {
			t.Errorf("updater = %+v, want installed, expected and running", c)
		}
	})

	t.Run("no role and no sync yet", func(t *testing.T) {
		h := newStatusHost(t)
		put(t, h.agentPlist(shipper.AgentLabel), "plist\n")
		put(t, h.job(launchd.User, shipper.AgentLabel), printOutput("running",
			"last exit reason = JETSAM_REASON_MEMORY_PERPROCESSLIMIT"))

		raw, report := runStatusJSON(t)
		if report.Role != nil || report.RoleError != nil {
			t.Errorf("role = %v, error = %v, want both null", report.Role, report.RoleError)
		}
		c := report.Components["shipper"]
		if c.LastExitCode != nil || c.LastExitReason == nil || *c.LastExitReason != "JETSAM_REASON_MEMORY_PERPROCESSLIMIT" || c.Error != nil {
			t.Errorf("shipper = %+v, want the jetsam reason", c)
		}
		if c.Expected == nil || *c.Expected {
			t.Errorf("shipper expected = %v, want false with no role", c.Expected)
		}
		s := report.Sync
		if s == nil || s.LastSuccess != nil || s.Error != nil ||
			s.UnshippedSessions == nil || *s.UnshippedSessions != 0 || s.FailureActive == nil || *s.FailureActive {
			t.Errorf("sync is not a first run's:\n%s", raw)
		}
	})
}

func TestStatusJSONUnreadable(t *testing.T) {
	// healthyHost reads clean in every field, so whatever a case reports below
	// is what it broke.
	healthyHost := func(t *testing.T) statusHost {
		t.Helper()
		h := newStatusHost(t)
		if err := config.WriteRole(config.RoleServer); err != nil {
			t.Fatal(err)
		}
		for _, label := range []string{receiver.AgentLabel, summarizerLabel, extract.AgentLabel, shipper.AgentLabel, updater.AgentLabel} {
			put(t, h.agentPlist(label), "plist\n")
			put(t, h.job(launchd.User, label), printOutput("running", "last exit code = 0"))
		}
		putLog(t, filepath.Join(config.Home(), "receiver.log"))
		put(t, notifyState(), syncedState)
		stage(t, "unshipped", 0)
		return h
	}
	replace := func(t *testing.T, path string, with func(string) error) {
		t.Helper()
		if err := os.RemoveAll(path); err != nil {
			t.Fatal(err)
		}
		if err := with(path); err != nil {
			t.Fatal(err)
		}
	}
	asFile := func(path string) error { return os.WriteFile(path, nil, 0o600) }
	asDir := func(path string) error { return os.Mkdir(path, 0o700) }

	errs := func(report statusJSON) map[string]string {
		out := map[string]string{}
		add := func(name string, e *string) {
			if e != nil {
				out[name] = *e
			}
		}
		add("role", report.RoleError)
		for name, c := range report.Components {
			add(name, c.Error)
		}
		if report.Sync != nil {
			add("sync", report.Sync.Error)
		}
		return out
	}
	// noReading requires a component to claim no run state at all.
	noReading := func(t *testing.T, c componentJSON) {
		t.Helper()
		if c.State != stateUnknown || c.LastExitCode != nil || c.LastExitSignal != nil || c.LastExitReason != nil {
			t.Errorf("component = %+v, want state %s and no exit", c, stateUnknown)
		}
	}
	noCount := func(t *testing.T, report statusJSON) {
		t.Helper()
		s := report.Sync
		if s.UnshippedSessions != nil {
			t.Errorf("unshipped = %d, want null", *s.UnshippedSessions)
		}
		if s.LastSuccess == nil || s.FailureActive == nil || !*s.FailureActive {
			t.Errorf("sync = %+v, want the notify state that could be read", s)
		}
	}
	shipCursor := func() string {
		return filepath.Join(config.TransportDir(), "cursors", string(cursor.KindShip), "claude-code", "unshipped.cursor")
	}
	all := []string{"receiver", "summarizer", "extractor", "shipper", "updater"}

	t.Run("baseline reads clean", func(t *testing.T) {
		healthyHost(t)
		raw, report := runStatusJSON(t)
		if got := errs(report); len(got) != 0 {
			t.Fatalf("errors = %v:\n%s", got, raw)
		}
	})

	// A system-domain shipper resolves without HOME, so nothing else in the
	// object would show that its logs and sync state were never found.
	t.Run("no state root", func(t *testing.T) {
		h := newStatusHost(t)
		put(t, systemPlistPath(shipper.AgentLabel), "plist\n")
		put(t, h.job(launchd.System, shipper.AgentLabel), printOutput("running", "last exit code = 0"))
		t.Setenv("HOME", "")
		t.Setenv("LOOM_HOME", "")

		raw, err := executeStatusJSON(t)
		if err == nil || raw != "" {
			t.Fatalf("err = %v, stdout = %q, want an error and no object", err, raw)
		}
	})

	cases := []struct {
		name   string
		damage func(t *testing.T, h statusHost)
		failed []string // whose error must be set; no other may be
		check  func(t *testing.T, report statusJSON)
	}{
		{
			name:   "launchctl missing",
			damage: func(t *testing.T, h statusHost) { t.Setenv("PATH", t.TempDir()) },
			failed: all,
			check: func(t *testing.T, report statusJSON) {
				for _, c := range report.Components {
					noReading(t, c)
				}
			},
		},
		{
			name: "launchctl failing other than not-found",
			damage: func(t *testing.T, h statusHost) {
				put(t, h.job(launchd.User, receiver.AgentLabel)+".exit", "112")
			},
			failed: []string{"receiver"},
			check: func(t *testing.T, report statusJSON) {
				noReading(t, report.Components["receiver"])
			},
		},
		{
			name: "no state of the job's own",
			damage: func(t *testing.T, h statusHost) {
				out := strings.Replace(printOutput("running", "last exit code = 0"), "\tstate = running\n", "", 1)
				put(t, h.job(launchd.User, receiver.AgentLabel), out)
			},
			failed: []string{"receiver"},
			check: func(t *testing.T, report statusJSON) {
				if got := report.Components["receiver"].State; got != stateUnknown {
					t.Errorf("state = %q, want %s rather than the nested block's", got, stateUnknown)
				}
			},
		},
		{
			name: "no last exit",
			damage: func(t *testing.T, h statusHost) {
				put(t, h.job(launchd.User, receiver.AgentLabel), printOutput("running", "runs = 2"))
			},
			failed: []string{"receiver"},
			check: func(t *testing.T, report statusJSON) {
				if c := report.Components["receiver"]; c.State != "running" || c.LastExitCode != nil {
					t.Errorf("receiver = %+v, want running with no exit code", c)
				}
			},
		},
		{
			name: "last exit code not a number",
			damage: func(t *testing.T, h statusHost) {
				put(t, h.job(launchd.User, receiver.AgentLabel), printOutput("running", "last exit code = (abandoned)"))
			},
			failed: []string{"receiver"},
			check: func(t *testing.T, report statusJSON) {
				if c := report.Components["receiver"]; c.State != "running" || c.LastExitCode != nil {
					t.Errorf("receiver = %+v, want running with no exit code", c)
				}
			},
		},
		{
			name: "plists unreadable",
			damage: func(t *testing.T, h statusHost) {
				replace(t, filepath.Join(h.home, "Library", "LaunchAgents"), asFile)
			},
			failed: all,
			check: func(t *testing.T, report statusJSON) {
				for name, c := range report.Components {
					noReading(t, c)
					if c.Installed != nil {
						t.Errorf("%s installed = %v, want null", name, *c.Installed)
					}
				}
				if e := report.Components["updater"].Expected; e != nil {
					t.Errorf("updater expected = %v, want null: it turns on the plist", *e)
				}
				if report.Sync == nil {
					t.Error("sync is null though the shipper may be installed")
				}
			},
		},
		{
			name: "system plists unreadable",
			damage: func(t *testing.T, h statusHost) {
				replace(t, h.systemPlists, asFile)
			},
			failed: all,
			check: func(t *testing.T, report statusJSON) {
				for _, c := range report.Components {
					noReading(t, c)
				}
			},
		},
		{
			name: "log unreadable",
			damage: func(t *testing.T, h statusHost) {
				replace(t, filepath.Join(config.Home(), "receiver.log"), func(path string) error {
					return os.Symlink(path, path)
				})
			},
			failed: []string{"receiver"},
			check: func(t *testing.T, report statusJSON) {
				if c := report.Components["receiver"]; c.State != "running" || c.LastActivity != nil {
					t.Errorf("receiver = %+v, want running with no activity time", c)
				}
			},
		},
		{
			name:   "notify state unreadable",
			damage: func(t *testing.T, h statusHost) { replace(t, notifyState(), asDir) },
			failed: []string{"sync"},
			check: func(t *testing.T, report statusJSON) {
				s := report.Sync
				if s.LastSuccess != nil || s.FailureActive != nil {
					t.Errorf("sync = %+v, want no last success and no failure reading", s)
				}
				if s.UnshippedSessions == nil || *s.UnshippedSessions != 1 {
					t.Errorf("unshipped = %v, want the 1 that could be read", s.UnshippedSessions)
				}
			},
		},
		{
			name:   "notify state unparseable",
			damage: func(t *testing.T, h statusHost) { put(t, notifyState(), `{"last_success_ts":`) },
			failed: []string{"sync"},
			check: func(t *testing.T, report statusJSON) {
				if s := report.Sync; s.LastSuccess != nil || s.FailureActive != nil {
					t.Errorf("sync = %+v, want no last success and no failure reading", s)
				}
			},
		},
		{
			name: "staging unreadable",
			damage: func(t *testing.T, h statusHost) {
				replace(t, filepath.Join(config.TransportDir(), "staging"), asFile)
			},
			failed: []string{"sync"},
			check:  noCount,
		},
		{
			name: "one agent's staging unlistable",
			damage: func(t *testing.T, h statusHost) {
				put(t, filepath.Join(config.TransportDir(), "staging", "codex", "project", "parent", "subagents"), "")
			},
			failed: []string{"sync"},
			check:  noCount,
		},
		{
			name:   "ship cursor malformed",
			damage: func(t *testing.T, h statusHost) { put(t, shipCursor(), "3x") },
			failed: []string{"sync"},
			check:  noCount,
		},
		{
			name:   "ship cursor unreadable",
			damage: func(t *testing.T, h statusHost) { replace(t, shipCursor(), asDir) },
			failed: []string{"sync"},
			check:  noCount,
		},
		{
			name: "staged file unreadable",
			damage: func(t *testing.T, h statusHost) {
				path := filepath.Join(config.TransportDir(), "staging", "claude-code", "project", "unshipped.jsonl")
				replace(t, path, func(path string) error { return os.Symlink(path, path) })
			},
			failed: []string{"sync"},
			check:  noCount,
		},
		{
			name:   "role unreadable",
			damage: func(t *testing.T, h statusHost) { replace(t, filepath.Join(config.Home(), "role"), asDir) },
			failed: []string{"role"},
			check: func(t *testing.T, report statusJSON) {
				for name, c := range report.Components {
					if c.Expected != nil {
						t.Errorf("%s expected = %v, want null", name, *c.Expected)
					}
				}
			},
		},
		{
			name:   "role unrecognized",
			damage: func(t *testing.T, h statusHost) { put(t, filepath.Join(config.Home(), "role"), "servr\n") },
			failed: []string{"role"},
			check: func(t *testing.T, report statusJSON) {
				if report.Role != nil {
					t.Errorf("role = %q, want null", *report.Role)
				}
				for name, c := range report.Components {
					if c.Expected != nil {
						t.Errorf("%s expected = %v, want null", name, *c.Expected)
					}
				}
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.damage(t, healthyHost(t))
			raw, report := runStatusJSON(t)
			got := errs(report)
			for _, name := range c.failed {
				if got[name] == "" {
					t.Errorf("%s carries no error:\n%s", name, raw)
				}
				delete(got, name)
			}
			if len(got) != 0 {
				t.Errorf("errors outside %v: %v", c.failed, got)
			}
			if !t.Failed() {
				c.check(t, report)
			}
		})
	}
}
