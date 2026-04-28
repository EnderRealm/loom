// Package config exposes loom-wide path resolution: the state root
// (LOOM_HOME or ~/.loom) and the canonical paths beneath it that every
// component agrees on. Component-specific configuration lives next to
// the component (e.g. transport/shipper/config.go for the shipper's
// server_url + interval_minutes).
package config

import (
	"os"
	"path/filepath"
)

// Home returns the loom state root: $LOOM_HOME if set, else ~/.loom.
func Home() string {
	if h := os.Getenv("LOOM_HOME"); h != "" {
		return h
	}
	home, err := os.UserHomeDir()
	if err != nil {
		// Fall back to cwd rather than panic; surfaces as a clear error
		// on first write.
		return ".loom"
	}
	return filepath.Join(home, ".loom")
}

// Path returns the absolute path to the shipper's config.json.
//
// Kept here (rather than next to the shipper config struct) because
// `loom status` and other lifecycle helpers need to know the path
// without depending on the shipper package.
func Path() string {
	return filepath.Join(Home(), "config.json")
}

// TransportDir returns the directory that holds shipper state
// (cursors, lock, log, staging).
func TransportDir() string {
	return filepath.Join(Home(), "transport")
}
