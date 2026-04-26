package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// repoRoot finds the loom repo root, where install.sh lives. Resolution
// order: $LOOM_REPO, walk up from CWD for install.sh+go.mod, fall back to
// the source file's compile-time location. install.sh remains the
// authoritative installer until ported into Go.
func repoRoot() (string, error) {
	if env := os.Getenv("LOOM_REPO"); env != "" {
		return env, nil
	}
	if cwd, err := os.Getwd(); err == nil {
		if root, ok := walkUpForInstall(cwd); ok {
			return root, nil
		}
	}
	if _, thisFile, _, ok := runtime.Caller(0); ok {
		// cmd/loom/cmd/util.go → repo root is three dirs up.
		root := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", "..", ".."))
		if _, err := os.Stat(filepath.Join(root, "install.sh")); err == nil {
			return root, nil
		}
	}
	return "", fmt.Errorf("loom repo not found; set LOOM_REPO or run from inside a clone")
}

func walkUpForInstall(start string) (string, bool) {
	dir := start
	for {
		if _, err := os.Stat(filepath.Join(dir, "install.sh")); err == nil {
			if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
				return dir, true
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

func execPassthrough(name string, args ...string) error {
	c := exec.Command(name, args...)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	c.Env = os.Environ()
	return c.Run()
}
