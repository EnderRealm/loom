package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestBuildPlistValid runs `plutil -lint` against every plist shape buildPlist
// can produce. Purpose: catch template bugs without bootstrapping into real
// launchd. This is the only test in the shipper for now — justified because
// the plist generator is the one function we can't exercise via smoke test
// without touching the user's ~/Library/LaunchAgents.
func TestBuildPlistValid(t *testing.T) {
	if _, err := exec.LookPath("plutil"); err != nil {
		t.Skip("plutil not available")
	}
	tmp := t.TempDir()
	cases := []struct {
		name  string
		plist string
	}{
		{"no-loom-home", buildPlist("/usr/local/bin/loom-shipper", 600, "/Users/me/.loom/transport/shipper.log", "")},
		{"with-loom-home", buildPlist("/usr/local/bin/loom-shipper", 420, "/Users/me/.loom/transport/shipper.log", "/custom/loom")},
		{"path-with-spaces", buildPlist("/Users/Test User/bin/loom-shipper", 600, "/tmp/log file.log", "")},
		{"path-with-ampersand", buildPlist("/Users/me/bin/A&B/loom-shipper", 300, "/tmp/log", "")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := filepath.Join(tmp, c.name+".plist")
			if err := os.WriteFile(p, []byte(c.plist), 0o644); err != nil {
				t.Fatal(err)
			}
			out, err := exec.Command("plutil", "-lint", p).CombinedOutput()
			if err != nil {
				t.Fatalf("plutil -lint failed: %v\n%s\n---plist---\n%s", err, out, c.plist)
			}
			if !strings.Contains(string(out), "OK") {
				t.Fatalf("unexpected plutil output: %s", out)
			}
		})
	}
}
