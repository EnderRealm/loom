package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Machine roles. A machine's role narrows which daemons `loom dev` and
// `loom status` expect to be running: a server folds sessions
// (receiver + summarizer), a remote only ships them. Persisted by
// `loom install server` / `loom install remote`; individual component
// installs leave the role untouched.
const (
	RoleServer = "server"
	RoleRemote = "remote"
)

// rolePath returns the path to the role marker file under Home().
func rolePath() string {
	return filepath.Join(Home(), "role")
}

// validRole reports whether s is a recognized role token.
func validRole(s string) bool {
	return s == RoleServer || s == RoleRemote
}

// ReadRole returns the machine's persisted role, or "" when the marker is
// missing, unreadable, or holds an unrecognized token. Callers treat ""
// as "no role set" and fall back to legacy whole-machine expectations.
func ReadRole() string {
	data, err := os.ReadFile(rolePath())
	if err != nil {
		return ""
	}
	role := strings.TrimSpace(string(data))
	if !validRole(role) {
		return ""
	}
	return role
}

// WriteRole validates role and persists it as the trimmed token in the
// marker file under Home(), creating Home() if needed.
func WriteRole(role string) error {
	role = strings.TrimSpace(role)
	if !validRole(role) {
		return fmt.Errorf("invalid role %q", role)
	}
	if err := os.MkdirAll(Home(), 0o700); err != nil {
		return err
	}
	return os.WriteFile(rolePath(), []byte(role+"\n"), 0o644)
}
