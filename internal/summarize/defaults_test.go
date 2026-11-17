package summarize

import (
	"path/filepath"
	"testing"
)

func TestDefaultPathsFollowLoomHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LOOM_HOME", home)
	if got, want := DefaultReceivedDir(), filepath.Join(home, "received"); got != want {
		t.Errorf("DefaultReceivedDir() = %q, want %q", got, want)
	}
	if got, want := DefaultDBPath(), filepath.Join(home, "summaries.db"); got != want {
		t.Errorf("DefaultDBPath() = %q, want %q", got, want)
	}
}

func TestDefaultPathsWithoutLoomHome(t *testing.T) {
	userHome := t.TempDir()
	t.Setenv("LOOM_HOME", "")
	t.Setenv("HOME", userHome)
	if got, want := DefaultReceivedDir(), filepath.Join(userHome, ".loom", "received"); got != want {
		t.Errorf("DefaultReceivedDir() = %q, want %q", got, want)
	}
	if got, want := DefaultDBPath(), filepath.Join(userHome, ".loom", "summaries.db"); got != want {
		t.Errorf("DefaultDBPath() = %q, want %q", got, want)
	}
}
