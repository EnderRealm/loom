package source

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// CodexAgent is the Adapter.Agent() value for Codex CLI.
const CodexAgent = "codex-cli"

type codexAdapter struct{}

func (codexAdapter) Agent() string { return CodexAgent }

// codexSessionsDir returns ~/.codex/sessions.
func codexSessionsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".codex", "sessions"), nil
}

// List walks the Codex sessions tree and returns one Session per rollout file.
// Missing directory returns (nil, nil) so machines without Codex installed
// are silently skipped. A rollout whose first line isn't yet a session_meta
// record (brand-new session still warming up) is skipped this tick and picked
// up on the next.
func (codexAdapter) List() ([]Session, error) {
	base, err := codexSessionsDir()
	if err != nil {
		return nil, err
	}
	var out []Session
	walkErr := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return fs.SkipAll
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		if !strings.HasPrefix(name, "rollout-") || !strings.HasSuffix(name, ".jsonl") {
			return nil
		}
		sid := extractCodexSessionID(name)
		if sid == "" {
			return nil
		}
		cwd, err := readCodexCwd(path)
		if err != nil {
			// Parse errors on the first line are logged at the capture layer
			// via the surrounding io-class failure path; silently skip here.
			return nil
		}
		if cwd == "" {
			return nil
		}
		out = append(out, Session{
			Project:   encodeProjectPath(cwd),
			SessionID: sid,
			Path:      path,
		})
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, os.ErrNotExist) {
		return nil, walkErr
	}
	return out, nil
}

// extractCodexSessionID pulls the trailing 36-char UUID from a filename shaped
// like "rollout-<ISO-ts>-<uuid>.jsonl". Returns "" if the shape is wrong.
func extractCodexSessionID(fname string) string {
	base := strings.TrimSuffix(strings.TrimPrefix(fname, "rollout-"), ".jsonl")
	// UUID is always 36 chars (8-4-4-4-12 with hyphens).
	if len(base) < 36 {
		return ""
	}
	sid := base[len(base)-36:]
	if !looksLikeUUID(sid) {
		return ""
	}
	return sid
}

// looksLikeUUID is a cheap shape check: 8-4-4-4-12 hex-like with hyphens. We
// don't validate UUID version; Codex uses v7 today but that's an implementation
// detail of the producer, not our concern.
func looksLikeUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
				return false
			}
		}
	}
	return true
}

// readCodexCwd reads only the first line and parses out session_meta.payload.cwd.
// Returns "" with no error when the file is empty, the first line isn't yet
// terminated by \n, or the first record isn't a session_meta — all of which
// are "check back next tick" states, not failures.
func readCodexCwd(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	r := bufio.NewReader(f)
	line, err := r.ReadBytes('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	// Bail unless we saw a terminating newline — a half-written first line
	// could give us truncated JSON.
	if len(line) == 0 || line[len(line)-1] != '\n' {
		return "", nil
	}
	var meta struct {
		Type    string `json:"type"`
		Payload struct {
			Cwd string `json:"cwd"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(line[:len(line)-1], &meta); err != nil {
		return "", fmt.Errorf("parse codex session_meta %s: %w", path, err)
	}
	if meta.Type != "session_meta" {
		return "", nil
	}
	return meta.Payload.Cwd, nil
}

// encodeProjectPath turns a filesystem path into a Project segment the
// receiver will accept. Matches Claude's visual convention (slashes become
// hyphens) and additionally neutralizes characters safeComponent rejects:
// any '.' becomes '_' so we can never produce a ".." substring, and empty /
// "." / ".." inputs fall back to "_default".
func encodeProjectPath(p string) string {
	s := p
	s = strings.ReplaceAll(s, "/", "-")
	s = strings.ReplaceAll(s, "\\", "-")
	s = strings.ReplaceAll(s, ".", "_")
	if s == "" || s == "-" {
		return "_default"
	}
	return s
}
