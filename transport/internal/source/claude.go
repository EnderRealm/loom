package source

import (
	"os"
	"path/filepath"
	"strings"
)

// ClaudeAgent is the Adapter.Agent() value for Claude Code.
const ClaudeAgent = "claude-code"

// Agent is the legacy alias for ClaudeAgent. Kept because older callers and
// cursor files reference it. New code should prefer ClaudeAgent or adapter.Agent().
const Agent = ClaudeAgent

type claudeAdapter struct{}

func (claudeAdapter) Agent() string { return ClaudeAgent }

func (claudeAdapter) List() ([]Session, error) {
	return ListClaudeSessions()
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
// Kept as a package-level function so tests (and any direct callers) can invoke
// it without going through the Adapter slice.
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
