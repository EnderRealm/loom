// Package staging owns the per-session JSONL files that live between the
// agent's source directory and the receiver. Capture appends source bytes
// here; ship reads from here. Once bytes are captured the agent can delete
// its own files without data loss.
//
// A sibling .meta.json carries the session's authoritative project
// identity (git remote + raw cwd) so the ship pass can include it in the
// wire payload even when the agent's source file is gone. A subagent
// transcript stages under <project>/<parent>/subagents/ — mirroring the
// receiver layout — with its dispatch metadata in a .subagent.json sidecar
// beside it.
package staging

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"loom/internal/config"
)

// Dir returns the staging root (~/.loom/transport/staging).
func Dir() string {
	return filepath.Join(config.TransportDir(), "staging")
}

// Path returns the staging file path for one transcript. parentSessionID
// is empty for a top-level session; a subagent nests under its parent.
func Path(agent, project, parentSessionID, sessionID string) string {
	return filepath.Join(dir(agent, project, parentSessionID), sessionID+".jsonl")
}

// dir returns the directory a transcript stages in.
func dir(agent, project, parentSessionID string) string {
	if parentSessionID == "" {
		return filepath.Join(Dir(), agent, project)
	}
	return filepath.Join(Dir(), agent, project, parentSessionID, "subagents")
}

// metaPath returns the sidecar meta.json path for one transcript.
func metaPath(agent, project, parentSessionID, sessionID string) string {
	return filepath.Join(dir(agent, project, parentSessionID), sessionID+".meta.json")
}

// subagentPath returns the dispatch-metadata sidecar path for one subagent
// transcript. Deliberately a separate file from .meta.json so the existing
// flat identity sidecars keep parsing unchanged.
func subagentPath(agent, project, parentSessionID, sessionID string) string {
	return filepath.Join(dir(agent, project, parentSessionID), sessionID+".subagent.json")
}

// Identity is the project-identity sidecar written next to each staged
// session. Mirrors wire.ProjectIdentity but stays in this package so the
// staging layer doesn't depend on the wire package.
type Identity struct {
	GitRemote string `json:"git_remote,omitempty"`
	Cwd       string `json:"cwd,omitempty"`
	RootSlug  string `json:"root_slug,omitempty"`
}

// Subagent is the dispatch-metadata sidecar written next to a staged
// subagent transcript. Mirrors wire.Subagent but stays in this package so
// the staging layer doesn't depend on the wire package.
type Subagent struct {
	ParentSessionID string `json:"parent_session_id"`
	AgentType       string `json:"agent_type,omitempty"`
	Description     string `json:"description,omitempty"`
	ToolUseID       string `json:"tool_use_id,omitempty"`
	SpawnDepth      int    `json:"spawn_depth,omitempty"`
}

// Entry describes one staged transcript, used by the ship pass and notifier
// to walk staging without re-listing source files. Subagent is nil for a
// top-level session.
type Entry struct {
	Agent     string
	Project   string
	SessionID string
	Path      string
	Identity  Identity
	Subagent  *Subagent
}

// Key is the flat, filesystem-safe identifier used for cursor files, matching
// source.Session.Key so a transcript keeps one cursor across both passes.
func (e Entry) Key() string {
	if e.Subagent == nil {
		return e.SessionID
	}
	return e.Subagent.ParentSessionID + "." + e.SessionID
}

// ParentID is the id of the session that dispatched this one, or "" for a
// top-level session, matching source.Session.ParentID.
func (e Entry) ParentID() string {
	if e.Subagent == nil {
		return ""
	}
	return e.Subagent.ParentSessionID
}

// Append copies data into the staging file for (agent, project,
// parentSessionID, sessionID), creating parent dirs as needed. Fsync on
// close so a crash mid-tick doesn't leave a short file that the next tick
// would re-fill from the wrong offset.
func Append(agent, project, parentSessionID, sessionID string, data []byte) error {
	if len(data) == 0 {
		return nil
	}
	p := Path(agent, project, parentSessionID, sessionID)
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
func Size(agent, project, parentSessionID, sessionID string) (int64, error) {
	info, err := os.Stat(Path(agent, project, parentSessionID, sessionID))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	return info.Size(), nil
}

// List returns every staged transcript for the given agent — top-level
// sessions and the subagents staged beneath them. Used by the ship pass so
// it can drain staging even when the source file is gone.
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
			if f.IsDir() {
				subs, err := listSubagents(agent, p.Name(), f.Name())
				if err != nil {
					return nil, err
				}
				out = append(out, subs...)
				continue
			}
			if filepath.Ext(f.Name()) != ".jsonl" {
				continue
			}
			// Skip sidecar meta files masquerading via .jsonl extension
			// (none today, but cheap guard against a future rename).
			if strings.HasSuffix(f.Name(), ".meta.json") {
				continue
			}
			sessionID := f.Name()[:len(f.Name())-len(".jsonl")]
			id, _ := ReadIdentity(agent, p.Name(), "", sessionID)
			out = append(out, Entry{
				Agent:     agent,
				Project:   p.Name(),
				SessionID: sessionID,
				Path:      Path(agent, p.Name(), "", sessionID),
				Identity:  id,
			})
		}
	}
	return out, nil
}

// listSubagents returns the staged subagent transcripts under one parent
// session. ParentSessionID comes from the directory name, so a transcript
// whose dispatch sidecar is missing still lists with a usable Entry.
func listSubagents(agent, project, parentSessionID string) ([]Entry, error) {
	files, err := os.ReadDir(dir(agent, project, parentSessionID))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []Entry
	for _, f := range files {
		if f.IsDir() || filepath.Ext(f.Name()) != ".jsonl" {
			continue
		}
		sessionID := f.Name()[:len(f.Name())-len(".jsonl")]
		id, _ := ReadIdentity(agent, project, parentSessionID, sessionID)
		sub, _ := ReadSubagent(agent, project, parentSessionID, sessionID)
		sub.ParentSessionID = parentSessionID
		out = append(out, Entry{
			Agent:     agent,
			Project:   project,
			SessionID: sessionID,
			Path:      Path(agent, project, parentSessionID, sessionID),
			Identity:  id,
			Subagent:  &sub,
		})
	}
	return out, nil
}

// WriteIdentity persists or updates the per-session meta sidecar. Safe
// to call repeatedly: the file is rewritten atomically via temp+rename.
// Empty Identity is a no-op so the ship pass doesn't accidentally erase
// a sidecar written by an earlier capture pass.
func WriteIdentity(agent, project, parentSessionID, sessionID string, id Identity) error {
	if id == (Identity{}) {
		return nil
	}
	p := metaPath(agent, project, parentSessionID, sessionID)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(id)
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// ReadIdentity returns the meta sidecar for one session. Missing file
// returns the zero Identity with no error — pre-identity sessions stage
// without it and ship under the legacy slug-only wire shape.
func ReadIdentity(agent, project, parentSessionID, sessionID string) (Identity, error) {
	data, err := os.ReadFile(metaPath(agent, project, parentSessionID, sessionID))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Identity{}, nil
		}
		return Identity{}, err
	}
	var id Identity
	if err := json.Unmarshal(data, &id); err != nil {
		return Identity{}, fmt.Errorf("parse meta %s: %w", metaPath(agent, project, parentSessionID, sessionID), err)
	}
	return id, nil
}

// WriteSubagent persists the dispatch-metadata sidecar for one staged
// subagent transcript, atomically via temp+rename. The capture pass calls
// this every tick for every staged subagent, so a write that would produce
// the bytes already on disk is skipped — otherwise the common case is a
// temp+rename per subagent per tick (~2000 on the measured corpus) that
// changes nothing. An unreadable sidecar compares unequal and is rewritten.
func WriteSubagent(agent, project, parentSessionID, sessionID string, sub Subagent) error {
	if cur, err := ReadSubagent(agent, project, parentSessionID, sessionID); err == nil && cur == sub {
		return nil
	}
	p := subagentPath(agent, project, parentSessionID, sessionID)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(sub)
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// ReadSubagent returns the dispatch-metadata sidecar for one staged subagent
// transcript. Missing file returns the zero Subagent with no error — the
// agent writes no sidecar for some subagents and those still ship.
func ReadSubagent(agent, project, parentSessionID, sessionID string) (Subagent, error) {
	data, err := os.ReadFile(subagentPath(agent, project, parentSessionID, sessionID))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Subagent{}, nil
		}
		return Subagent{}, err
	}
	var sub Subagent
	if err := json.Unmarshal(data, &sub); err != nil {
		return Subagent{}, fmt.Errorf("parse subagent meta %s: %w", subagentPath(agent, project, parentSessionID, sessionID), err)
	}
	return sub, nil
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
