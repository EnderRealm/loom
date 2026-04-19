// Package cursor persists per-session byte offsets. One file per session,
// atomic replace on write. Missing cursor means "start from 0".
//
// Two kinds of cursor coexist:
//
//	KindSource  — how many bytes of the agent's source file we have copied into staging
//	KindShip    — how many bytes of the staging file we have shipped to the receiver
//
// Legacy layout (pre-staging) kept a single cursor per session under
// cursors/<agent>/<session>.cursor which represented "bytes shipped from
// source". Migrate() promotes those files into the ship namespace and seeds
// matching source cursors at the same offset, so the first post-migration
// tick is a no-op (capture finds nothing new; ship finds nothing new).
package cursor

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"loom/internal/config"
)

// Kind is a cursor namespace.
type Kind string

const (
	KindSource Kind = "source"
	KindShip   Kind = "ship"
)

func baseDir() string {
	return filepath.Join(config.TransportDir(), "cursors")
}

func kindDir(kind Kind, agent string) string {
	return filepath.Join(baseDir(), string(kind), agent)
}

func kindPath(kind Kind, agent, sessionID string) string {
	return filepath.Join(kindDir(kind, agent), sessionID+".cursor")
}

// Read returns the stored byte offset for a session, or 0 if none exists yet.
func Read(kind Kind, agent, sessionID string) (int64, error) {
	return readFile(kindPath(kind, agent, sessionID))
}

// Write stores a new byte offset for a session, atomically via temp-file + rename.
func Write(kind Kind, agent, sessionID string, offset int64) error {
	if err := os.MkdirAll(kindDir(kind, agent), 0o700); err != nil {
		return err
	}
	return writeFile(kindPath(kind, agent, sessionID), offset)
}

func readFile(path string) (int64, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse cursor %s: %w", path, err)
	}
	return n, nil
}

func writeFile(path string, offset int64) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(strconv.FormatInt(offset, 10)), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Migrate promotes any pre-staging cursor files at cursors/<agent>/*.cursor
// into cursors/ship/<agent>/ and seeds matching cursors/source/<agent>/ at the
// same offset. Safe to call on every tick: if the ship cursor already exists
// for a session we skip it. The legacy file is removed after a successful
// migration so subsequent ticks are cheap.
func Migrate() error {
	root := baseDir()
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		// Skip the new-layout directories themselves.
		if name == string(KindSource) || name == string(KindShip) {
			continue
		}
		agent := name
		legacyDir := filepath.Join(root, agent)
		files, err := os.ReadDir(legacyDir)
		if err != nil {
			return fmt.Errorf("read legacy cursor dir %s: %w", legacyDir, err)
		}
		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".cursor") {
				continue
			}
			sessionID := strings.TrimSuffix(f.Name(), ".cursor")
			legacyPath := filepath.Join(legacyDir, f.Name())
			shipPath := kindPath(KindShip, agent, sessionID)
			sourcePath := kindPath(KindSource, agent, sessionID)
			// If already migrated, just drop the legacy file.
			if _, err := os.Stat(shipPath); err == nil {
				_ = os.Remove(legacyPath)
				continue
			}
			off, err := readFile(legacyPath)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(shipPath), 0o700); err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(sourcePath), 0o700); err != nil {
				return err
			}
			if err := writeFile(shipPath, off); err != nil {
				return err
			}
			if err := writeFile(sourcePath, off); err != nil {
				return err
			}
			_ = os.Remove(legacyPath)
		}
		// Remove the legacy agent dir if now empty.
		_ = os.Remove(legacyDir)
	}
	return nil
}

// ListSessions returns every session that has a cursor in the given kind
// namespace for the given agent. Used by the ship pass and notifier to walk
// staged state without re-listing source files.
func ListSessions(kind Kind, agent string) ([]string, error) {
	dir := kindDir(kind, agent)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".cursor") {
			continue
		}
		out = append(out, strings.TrimSuffix(e.Name(), ".cursor"))
	}
	return out, nil
}
