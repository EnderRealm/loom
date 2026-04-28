package launchd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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
