// Package source enumerates agent session files and reads deltas from them.
// v1 hardcodes the Claude Code adapter; when a second agent appears we extract
// an interface. YAGNI until then.
package source

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Agent is the stable identifier we ship in IngestRequest.Agent for Claude sessions.
const Agent = "claude-code"

// Session is one Claude session file on disk.
type Session struct {
	Project   string // sanitized project dir name, e.g. "-Users-smacbeth-code-loom"
	SessionID string // UUID parsed from the filename
	Path      string // absolute path to the .jsonl file
}

// claudeProjectsDir returns ~/.claude/projects.
func claudeProjectsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", "projects"), nil
}

// ListClaudeSessions enumerates every .jsonl session file under ~/.claude/projects/*/.
func ListClaudeSessions() ([]Session, error) {
	base, err := claudeProjectsDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Session
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		project := e.Name()
		files, err := os.ReadDir(filepath.Join(base, project))
		if err != nil {
			continue
		}
		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".jsonl") {
				continue
			}
			out = append(out, Session{
				Project:   project,
				SessionID: strings.TrimSuffix(f.Name(), ".jsonl"),
				Path:      filepath.Join(base, project, f.Name()),
			})
		}
	}
	return out, nil
}

// ReadDelta opens path, seeks to from, and returns only COMPLETE lines plus the
// new safe offset (byte after the last \n). Any partial trailing line is left
// for the next tick — guarantees we never ship a half-written JSON record.
func ReadDelta(path string, from int64) (lines []string, toOffset int64, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, from, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, from, err
	}
	size := info.Size()
	if from >= size {
		return nil, from, nil
	}
	if _, err := f.Seek(from, io.SeekStart); err != nil {
		return nil, from, err
	}
	buf, err := io.ReadAll(f)
	if err != nil {
		return nil, from, err
	}

	lastNL := bytes.LastIndexByte(buf, '\n')
	if lastNL < 0 {
		// No complete line available yet.
		return nil, from, nil
	}
	complete := buf[:lastNL+1]
	raw := bytes.Split(bytes.TrimSuffix(complete, []byte("\n")), []byte("\n"))
	lines = make([]string, 0, len(raw))
	for _, l := range raw {
		if len(l) == 0 {
			continue
		}
		lines = append(lines, string(l))
	}
	return lines, from + int64(lastNL+1), nil
}
