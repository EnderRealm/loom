// Package cursor persists per-session byte offsets. One file per session,
// atomic replace on write. Missing cursor means "start from 0".
package cursor

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"loom/internal/config"
)

func dir(agent string) string {
	return filepath.Join(config.TransportDir(), "cursors", agent)
}

func path(agent, sessionID string) string {
	return filepath.Join(dir(agent), sessionID+".cursor")
}

// Read returns the stored byte offset for a session, or 0 if none exists yet.
func Read(agent, sessionID string) (int64, error) {
	data, err := os.ReadFile(path(agent, sessionID))
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse cursor %s: %w", path(agent, sessionID), err)
	}
	return n, nil
}

// Write stores a new byte offset for a session, atomically via temp-file + rename.
func Write(agent, sessionID string, offset int64) error {
	if err := os.MkdirAll(dir(agent), 0o700); err != nil {
		return err
	}
	p := path(agent, sessionID)
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, []byte(strconv.FormatInt(offset, 10)), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}
