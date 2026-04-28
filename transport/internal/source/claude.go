package source

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
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

// readClaudeCwd reads the first record's `cwd` field. Empty return
// means "not yet available" (file is empty or first line isn't \n-
// terminated yet); a parse error is returned so the caller can skip
// the session this tick rather than emit a Session with no Cwd.
func readClaudeCwd(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", nil
	}
	defer f.Close()

	r := bufio.NewReader(f)
	line, err := r.ReadBytes('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	if len(line) == 0 || line[len(line)-1] != '\n' {
		return "", nil
	}
	var probe struct {
		Cwd string `json:"cwd"`
	}
	if err := json.Unmarshal(line[:len(line)-1], &probe); err != nil {
		// Malformed first line — caller skips; capture log will surface
		// it as drift on the next pass once the line completes.
		return "", err
	}
	return probe.Cwd, nil
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
			path := filepath.Join(base, project, f.Name())
			cwd, err := readClaudeCwd(path)
			if err != nil {
				// Parse failure on the first line of an active session is
				// a transient state (interleaved write); skip and retry next tick.
				continue
			}
			out = append(out, Session{
				Project:   project,
				SessionID: strings.TrimSuffix(f.Name(), ".jsonl"),
				Path:      path,
				Cwd:       cwd,
			})
		}
	}
	return out, nil
}
