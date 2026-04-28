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

// readClaudeCwd scans the head of a Claude session JSONL for the first
// non-empty `cwd` field. Sparse-headered records (permission-mode,
// file-history-snapshot, agent-name, custom-title, last-prompt, pr-link)
// carry no cwd, so the FIRST line frequently misses it; the canonical
// cwd lives on user/assistant/system records that follow. Empty return
// means "not yet available" (file is empty or no \n-terminated line in
// the head carried a cwd); the capture pass treats that as "check back
// next tick" and skips writing an identity sidecar this round.
//
// 200 lines is a generous head sample — a session that hasn't logged a
// header-bearing record in its first 200 lines is broken regardless,
// and 200 small JSON parses cost <1ms.
func readClaudeCwd(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", nil
	}
	defer f.Close()

	r := bufio.NewReader(f)
	for i := 0; i < 200; i++ {
		line, err := r.ReadBytes('\n')
		if errors.Is(err, io.EOF) || (len(line) > 0 && line[len(line)-1] != '\n') {
			// Reached EOF without a complete trailing line.
			return "", nil
		}
		if err != nil {
			return "", err
		}
		var probe struct {
			Cwd string `json:"cwd"`
		}
		if err := json.Unmarshal(line[:len(line)-1], &probe); err != nil {
			// Malformed line — keep scanning; legitimate later records
			// can still answer the question.
			continue
		}
		if probe.Cwd != "" {
			return probe.Cwd, nil
		}
	}
	return "", nil
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
