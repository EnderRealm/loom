package launchd

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestPlistsLintClean walks every component's plist shape through plutil-lint
// so a generator bug fails the build instead of poisoning ~/Library.
func TestPlistsLintClean(t *testing.T) {
	if _, err := exec.LookPath("plutil"); err != nil {
		t.Skip("plutil not available")
	}
	tmp := t.TempDir()

	cases := []struct {
		name string
		spec Spec
	}{
		{
			name: "shipper-daemon",
			spec: Spec{
				Label:                   "com.loom.shipper",
				Program:                 "/Users/me/.local/bin/loom",
				Args:                    []string{"shipper", "daemon"},
				LogPath:                 "/Users/me/.loom/transport/shipper.log",
				KeepAlive:               true,
				RunAtLoad:               true,
				ThrottleIntervalSeconds: 10,
			},
		},
		{
			name: "shipper-with-env",
			spec: Spec{
				Label:                   "com.loom.shipper",
				Program:                 "/Users/me/.local/bin/loom",
				Args:                    []string{"shipper", "daemon"},
				LogPath:                 "/Users/me/.loom/transport/shipper.log",
				Env:                     map[string]string{"LOOM_HOME": "/custom/loom"},
				KeepAlive:               true,
				RunAtLoad:               true,
				ThrottleIntervalSeconds: 10,
			},
		},
		{
			name: "receiver",
			spec: Spec{
				Label:     "com.loom.receiver",
				Program:   "/Users/me/.local/bin/loom",
				Args:      []string{"receiver"},
				LogPath:   "/Users/me/.loom/receiver.log",
				Env:       map[string]string{"LOOM_RECEIVER_TOKEN": "deadbeef", "LOOM_HOME": "/Users/me/.loom"},
				KeepAlive: true,
				RunAtLoad: true,
			},
		},
		{
			name: "summarizer-watch",
			spec: Spec{
				Label:     "com.loom.summarizer",
				Program:   "/Users/me/.local/bin/loom",
				Args:      []string{"summarize", "--watch"},
				LogPath:   "/Users/me/.loom/summarizer.log",
				Env:       map[string]string{"LOOM_HOME": "/Users/me/.loom"},
				KeepAlive: true,
				RunAtLoad: true,
			},
		},
		{
			name: "path-with-spaces-and-amp",
			spec: Spec{
				Label:     "com.loom.shipper",
				Program:   "/Users/Test User/A&B/loom",
				Args:      []string{"shipper", "daemon"},
				LogPath:   "/tmp/log file.log",
				KeepAlive: true,
				RunAtLoad: true,
			},
		},
		{
			name: "system-daemon",
			spec: Spec{
				Label:                   "com.loom.shipper",
				Program:                 "/Users/engine/.local/bin/loom",
				Args:                    []string{"shipper", "daemon"},
				LogPath:                 "/Users/engine/.loom/transport/shipper.log",
				Env:                     map[string]string{"HOME": "/Users/engine", DomainEnv: SystemDomain},
				KeepAlive:               true,
				RunAtLoad:               true,
				ThrottleIntervalSeconds: 10,
				Domain:                  System,
				UserName:                "engine",
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := filepath.Join(tmp, c.name+".plist")
			if err := os.WriteFile(p, []byte(c.spec.PlistXML()), 0o644); err != nil {
				t.Fatal(err)
			}
			out, err := exec.Command("plutil", "-lint", p).CombinedOutput()
			if err != nil {
				t.Fatalf("plutil -lint failed: %v\n%s\n---plist---\n%s", err, out, c.spec.PlistXML())
			}
			if !strings.Contains(string(out), "OK") {
				t.Fatalf("unexpected plutil output: %s", out)
			}
		})
	}
}

// TestPlistDeterministic guards against environment-map iteration order
// leaking into the rendered plist.
func TestPlistDeterministic(t *testing.T) {
	spec := Spec{
		Label:   "com.loom.x",
		Program: "/usr/local/bin/loom",
		Args:    []string{"x"},
		LogPath: "/tmp/x.log",
		Env: map[string]string{
			"Z_LAST":    "1",
			"A_FIRST":   "2",
			"M_MIDDLE":  "3",
			"LOOM_HOME": "/h",
		},
		KeepAlive: true,
		RunAtLoad: true,
	}
	a, b := spec.PlistXML(), spec.PlistXML()
	if a != b {
		t.Errorf("PlistXML not deterministic")
	}
	// Env keys must appear in lexical order.
	for _, want := range []string{"A_FIRST", "LOOM_HOME", "M_MIDDLE", "Z_LAST"} {
		if !strings.Contains(a, "<key>"+want+"</key>") {
			t.Errorf("missing env key %s", want)
		}
	}
}

// printSample is the shape of real `launchctl print` output: top-level
// fields first, then the nested coalition block that repeats "state".
const printSample = `gui/501/com.loom.receiver = {
	active count = 1
	path = /Users/steve/Library/LaunchAgents/com.loom.receiver.plist
	type = LaunchAgent
	state = running

	program = /Users/steve/.local/bin/loom
	arguments = {
		/Users/steve/.local/bin/loom
		receiver
	}
	pid = 1529
	domain = gui/501
	coalitions = {
		resource = {
			state = active
		}
	}
}
`

func TestPrintField(t *testing.T) {
	cases := []struct {
		key, want string
		found     bool
	}{
		{"state", "running", true}, // not the nested "active"
		{"pid", "1529", true},
		{"program", "/Users/steve/.local/bin/loom", true},
		{"last exit code", "", false},
	}
	for _, c := range cases {
		got, found := printField(printSample, c.key)
		if found != c.found {
			t.Errorf("printField(%q) found = %v, want %v", c.key, found, c.found)
		}
		if got != c.want {
			t.Errorf("printField(%q) = %q, want %q", c.key, got, c.want)
		}
	}
}

func TestParseETime(t *testing.T) {
	ok := []struct {
		in   string
		want time.Duration
	}{
		{"05:03", 5*time.Minute + 3*time.Second},
		{"1:02:03\n", time.Hour + 2*time.Minute + 3*time.Second},
		{"12-21:29:42", 12*24*time.Hour + 21*time.Hour + 29*time.Minute + 42*time.Second},
	}
	for _, c := range ok {
		got, err := parseETime(c.in)
		if err != nil {
			t.Errorf("parseETime(%q): %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("parseETime(%q) = %s, want %s", c.in, got, c.want)
		}
	}
	// Malformed input must error rather than return a zero duration, which
	// would read as "just started".
	for _, in := range []string{"", "42", "abc", "1:2:3:4", "12-", "12-01:02", "-01:02:03", "x:02"} {
		if d, err := parseETime(in); err == nil {
			t.Errorf("parseETime(%q) = %s, want error", in, d)
		}
	}
}

func TestProcessFromPrint(t *testing.T) {
	orig := psETime
	psETime = func(pid int) (string, error) {
		if pid != 1529 {
			t.Fatalf("ps called with pid %d, want 1529", pid)
		}
		return "01:00:00\n", nil
	}
	t.Cleanup(func() { psETime = orig })

	p, ok, err := ProcessFromPrint(printSample)
	if err != nil || !ok {
		t.Fatalf("ProcessFromPrint = (%v, %v, %v)", p, ok, err)
	}
	if p.PID != 1529 || p.Program != "/Users/steve/.local/bin/loom" {
		t.Fatalf("process = %+v", p)
	}
	if age := time.Since(p.Started); age < 59*time.Minute || age > 61*time.Minute {
		t.Fatalf("start time %s implies age %s, want ~1h", p.Started, age)
	}

	// A loaded job with no process carries no pid line: not running, not an
	// error, and never a start time to compare against.
	if _, ok, err := ProcessFromPrint("gui/501/com.loom.receiver = {\n\tstate = waiting\n}\n"); ok || err != nil {
		t.Fatalf("processless job = (%v, %v), want (false, nil)", ok, err)
	}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		name string
		spec Spec
		ok   bool
	}{
		{"empty-label", Spec{Program: "/x"}, false},
		{"slash-in-label", Spec{Label: "com/loom/x", Program: "/x"}, false},
		{"empty-program", Spec{Label: "com.loom.x"}, false},
		{"relative-program", Spec{Label: "com.loom.x", Program: "loom"}, false},
		{"valid", Spec{Label: "com.loom.x", Program: "/usr/local/bin/loom"}, true},
		{"system-without-user", Spec{Label: "com.loom.x", Program: "/x", Domain: System}, false},
		{"system-with-user", Spec{Label: "com.loom.x", Program: "/x", Domain: System, UserName: "engine"}, true},
	}
	for _, c := range cases {
		err := c.spec.validate()
		if c.ok && err != nil {
			t.Errorf("%s: got %v, want nil", c.name, err)
		}
		if !c.ok && err == nil {
			t.Errorf("%s: got nil, want error", c.name)
		}
	}
}

// A LaunchDaemon names the user it runs as; a LaunchAgent renders exactly as
// it did before system mode, with no UserName key.
func TestPlistUserName(t *testing.T) {
	spec := Spec{Label: "com.loom.x", Program: "/usr/local/bin/loom"}
	if strings.Contains(spec.PlistXML(), "UserName") {
		t.Fatalf("agent plist carries UserName:\n%s", spec.PlistXML())
	}
	spec.Domain, spec.UserName = System, "engine"
	want := "    <key>Label</key>\n    <string>com.loom.x</string>\n" +
		"    <key>UserName</key>\n    <string>engine</string>\n"
	if !strings.Contains(spec.PlistXML(), want) {
		t.Fatalf("daemon plist lacks UserName after Label:\n%s", spec.PlistXML())
	}
}

func TestSystemPaths(t *testing.T) {
	if got := SystemPlistPath("com.loom.shipper"); got != "/Library/LaunchDaemons/com.loom.shipper.plist" {
		t.Fatalf("SystemPlistPath = %q", got)
	}
	if got, _ := System.PlistPath("com.loom.shipper"); got != SystemPlistPath("com.loom.shipper") {
		t.Fatalf("System.PlistPath = %q", got)
	}
	if got := System.target("com.loom.shipper"); got != "system/com.loom.shipper" {
		t.Fatalf("System.target = %q", got)
	}
	if got, want := User.target("com.loom.shipper"), fmt.Sprintf("gui/%d/com.loom.shipper", os.Getuid()); got != want {
		t.Fatalf("User.target = %q, want %q", got, want)
	}
}

func TestDomainFromEnv(t *testing.T) {
	t.Setenv(DomainEnv, "")
	if DomainFromEnv() != User {
		t.Fatal("unset DomainEnv is not User")
	}
	t.Setenv(DomainEnv, SystemDomain)
	if DomainFromEnv() != System {
		t.Fatal("DomainEnv=system is not System")
	}
}

// SudoRestart is the updater's only way to restart a system daemon, and its
// argv must match the sudoers grant line for line: sudo -n, launchctl by
// absolute path, the system target, and the LaunchDaemon plist.
func TestSudoRestartArgv(t *testing.T) {
	var calls []string
	orig := run
	run = func(argv ...string) ([]byte, error) {
		calls = append(calls, strings.Join(argv, " "))
		if argv[0] == "launchctl" && argv[1] == "print" {
			return []byte("Could not find service"), errors.New("exit status 113")
		}
		return nil, nil
	}
	t.Cleanup(func() { run = orig })

	if err := SudoRestart("com.loom.shipper"); err != nil {
		t.Fatalf("SudoRestart: %v", err)
	}
	want := []string{
		"/usr/bin/sudo -n /bin/launchctl bootout system/com.loom.shipper",
		"launchctl print system/com.loom.shipper",
		"/usr/bin/sudo -n /bin/launchctl bootstrap system /Library/LaunchDaemons/com.loom.shipper.plist",
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls =\n%s\nwant\n%s", strings.Join(calls, "\n"), strings.Join(want, "\n"))
	}
}

// A bootstrap that keeps failing is reported by label, not swallowed.
func TestSudoRestartReportsBootstrapFailure(t *testing.T) {
	orig := run
	run = func(argv ...string) ([]byte, error) {
		if argv[0] == "launchctl" {
			return []byte("Could not find service"), errors.New("exit status 113")
		}
		if strings.Contains(strings.Join(argv, " "), "bootstrap") {
			return []byte("sudo: a password is required"), errors.New("exit status 1")
		}
		return nil, nil
	}
	t.Cleanup(func() { run = orig })

	err := SudoRestart("com.loom.updater")
	if err == nil || !strings.Contains(err.Error(), "com.loom.updater") || !strings.Contains(err.Error(), "password is required") {
		t.Fatalf("SudoRestart err = %v", err)
	}
}
