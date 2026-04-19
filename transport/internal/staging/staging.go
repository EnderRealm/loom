// Package staging owns the per-session JSONL files that live between the
// agent's source directory and the receiver. Capture appends source bytes
// here; ship reads from here. Once bytes are captured the agent can delete
// its own files without data loss.
package staging

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"loom/internal/config"
)

// Dir returns the staging root (~/.loom/transport/staging).
func Dir() string {
	return filepath.Join(config.TransportDir(), "staging")
}

// Path returns the staging file path for one session.
func Path(agent, project, sessionID string) string {
	return filepath.Join(Dir(), agent, project, sessionID+".jsonl")
}

// Entry describes one staged session, used by the ship pass and notifier to
// walk staging without re-listing source files.
type Entry struct {
	Agent     string
	Project   string
	SessionID string
	Path      string
}

// Append copies data into the staging file for (agent, project, sessionID),
// creating parent dirs as needed. Fsync on close so a crash mid-tick doesn't
// leave a short file that the next tick would re-fill from the wrong offset.
func Append(agent, project, sessionID string, data []byte) error {
	if len(data) == 0 {
		return nil
	}
	p := Path(agent, project, sessionID)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// Size returns the current size of the staging file, or 0 if it doesn't exist.
func Size(agent, project, sessionID string) (int64, error) {
	info, err := os.Stat(Path(agent, project, sessionID))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	return info.Size(), nil
}

// List returns every staged session for the given agent. Used by the ship
// pass so it can drain staging even when the source file is gone.
func List(agent string) ([]Entry, error) {
	agentDir := filepath.Join(Dir(), agent)
	projects, err := os.ReadDir(agentDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []Entry
	for _, p := range projects {
		if !p.IsDir() {
			continue
		}
		files, err := os.ReadDir(filepath.Join(agentDir, p.Name()))
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			if f.IsDir() || filepath.Ext(f.Name()) != ".jsonl" {
				continue
			}
			sessionID := f.Name()[:len(f.Name())-len(".jsonl")]
			out = append(out, Entry{
				Agent:     agent,
				Project:   p.Name(),
				SessionID: sessionID,
				Path:      filepath.Join(agentDir, p.Name(), f.Name()),
			})
		}
	}
	return out, nil
}

// AgentDirs returns the set of agent subdirectories that exist under staging.
// Caller uses this to walk "what's on disk regardless of which adapters are
// registered today" — e.g. if we drop Codex support, leftover Codex staging
// can still be shipped.
func AgentDirs() ([]string, error) {
	entries, err := os.ReadDir(Dir())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read staging dir: %w", err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out, nil
}
