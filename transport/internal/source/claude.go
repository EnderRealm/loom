package source

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"

	"loom/internal/config"
	"loom/transport/internal/wire"
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

// ListClaudeSessions enumerates every .jsonl session file under ~/.claude/projects/*/,
// plus every subagent transcript under ~/.claude/projects/*/<session>/subagents/
// (at any depth — workflow dispatches nest under workflows/<wf_id>/), then does
// the same for the spool of transcripts copied out of hosted agents' homes.
// Kept as a package-level function so tests (and any direct callers) can invoke
// it without going through the Adapter slice.
//
// Cursors key on agent and Session.Key alone, so two files sharing a key would
// advance one offset through both and interleave their bytes in staging. The
// first listed keeps the key, which is why the operator's own tree goes first:
// a spooled file is named by an agent and must not be able to take over a
// session the operator's agent is writing. For the same reason a failure to
// list the operator's tree stops here, with nothing to compare the spool
// against, while a failure to list the spool still returns the operator's
// sessions alongside the error.
func ListClaudeSessions() ([]Session, error) {
	base, err := claudeProjectsDir()
	if err != nil {
		return nil, err
	}
	out, err := listClaudeRoot(base, false)
	if err != nil {
		return nil, err
	}
	spooled, err := listClaudeRoot(config.ClaudeSpoolDir(), true)
	seen := make(map[string]bool, len(out))
	for _, s := range out {
		seen[s.Key()] = true
	}
	for _, s := range spooled {
		if seen[s.Key()] {
			logSpoolSkip(s.Project, s.ParentID(), s.SessionID, "key already listed, and a cursor holds one offset per key")
			continue
		}
		seen[s.Key()] = true
		out = append(out, s)
	}
	return out, err
}

// listClaudeRoot enumerates one tree laid out like ~/.claude/projects. A
// missing tree yields no sessions and no error. spool marks a tree holding
// what a hosted agent wrote, whose every name and entry type that agent chose;
// capture follows symlinks and runs as the operator, so spoolAdmits gates each
// entry on the way down. The operator's own tree is read as it stands.
func listClaudeRoot(base string, spool bool) ([]Session, error) {
	entries, err := os.ReadDir(base)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Session
	for _, e := range entries {
		if spool && !spoolAdmits(e, "", "") {
			continue
		}
		if !e.IsDir() {
			continue
		}
		project := e.Name()
		files, err := os.ReadDir(filepath.Join(base, project))
		if err != nil {
			continue
		}
		for _, f := range files {
			if spool && !spoolAdmits(f, project, "") {
				continue
			}
			if f.IsDir() {
				// Claude Code parks each session's subagent transcripts in
				// <session>/subagents/; the directory name is the parent
				// session's uuid.
				out = append(out, listClaudeSubagents(base, project, f.Name(), spool)...)
				continue
			}
			if !strings.HasSuffix(f.Name(), ".jsonl") {
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

// spoolAdmits reports whether a listing may touch a spool entry, logging the
// reason when it may not. d carries the lstat type, so a symlink reads as
// neither a directory nor a regular file whatever it points at: reaching one
// would ship any file the operator can read, and opening a FIFO would hang the
// tick. An unsafe name is refused here because it would 400 at the receiver on
// every tick without ever advancing its ship cursor, and a control character
// in it would forge lines in the shipper's own log. Checking at listing holds
// only because nothing but the operator's worker writes the spool, so an entry
// cannot change between this check and the read.
//
// The name checked is the one the receiver would see: a directory's whole, as
// Project or ParentSessionID, and a transcript's less its suffix, as the session
// id. Any other file, a sidecar included, sends no name at all.
func spoolAdmits(d fs.DirEntry, project, parent string) bool {
	name := d.Name()
	if !d.IsDir() {
		name = strings.TrimSuffix(name, ".jsonl")
	}
	reason := ""
	switch {
	case !d.IsDir() && !d.Type().IsRegular():
		reason = "not a regular file or directory: " + d.Type().String()
	case !d.IsDir() && name == d.Name():
		return true
	case !wire.SafeComponent(name):
		reason = "name the receiver rejects"
	default:
		return true
	}
	logSpoolSkip(project, parent, d.Name(), reason)
	return false
}

// logSpoolSkip records a spool entry left out of the listing. Names are quoted
// because an agent chose them.
func logSpoolSkip(project, parent, entry, reason string) {
	log.Printf("skip stage=list agent=%s project=%q parent=%q entry=%q reason=%q",
		ClaudeAgent, project, parent, entry, reason)
}

// claudeWorkflowJournal is the per-workflow bookkeeping file Claude Code
// writes alongside the subagent transcripts in subagents/workflows/<wf_id>/.
const claudeWorkflowJournal = "journal.jsonl"

// listClaudeSubagents enumerates every transcript beneath
// <project>/<parent>/subagents/. A missing subagents/ dir means the session
// dispatched nothing (or predates the layout) and yields no sessions.
//
// The subtree holds two layouts at once: transcripts written directly into
// subagents/, and workflow dispatches nested one level deeper under
// workflows/<wf_id>/. A nested transcript is namespaced by the directories
// between subagents/ and the file, joined with "." — so it stays a single
// path component, and two transcripts under one parent can never share a
// staging file or a cursor. Joining removes the separators the receiver's
// identifier guard rejects but not the ".." it also rejects, which a
// directory or transcript name that begins or ends with a dot produces; an
// id like that is skipped rather than enumerated, since it would 400 on
// every tick without ever advancing its ship cursor.
//
// WalkDir hands the callback lstat-typed entries and does not descend a
// symlink, including one at subagents/ itself, so spoolAdmits sees each level.
func listClaudeSubagents(base, project, parentSessionID string, spool bool) []Session {
	dir := filepath.Join(base, project, parentSessionID, "subagents")
	var out []Session
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// Missing subagents/ dir or an unreadable subtree: nothing to
			// enumerate this tick.
			return nil
		}
		if spool && !spoolAdmits(d, project, parentSessionID) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		// The .meta.json sidecars live here too; they are metadata, not
		// transcripts.
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".jsonl") {
			return nil
		}
		// journal.jsonl is a workflow dir's own bookkeeping — no dispatch,
		// no sidecar — and must not ship as a subagent transcript.
		if d.Name() == claudeWorkflowJournal {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return nil
		}
		sessionID := strings.ReplaceAll(strings.TrimSuffix(rel, ".jsonl"), string(filepath.Separator), ".")
		if strings.Contains(sessionID, "..") {
			log.Printf("skip stage=list agent=%s project=%s session=%s parent=%s reason=%q",
				ClaudeAgent, project, sessionID, parentSessionID, "flattened id contains \"..\", which the receiver rejects")
			return nil
		}
		cwd, err := readClaudeCwd(path)
		if err != nil {
			return nil
		}
		sidecar := strings.TrimSuffix(path, ".jsonl") + ".meta.json"
		var sub Subagent
		if !spool || spoolSidecarRegular(sidecar) {
			sub = readClaudeSubagentMeta(sidecar)
		}
		sub.ParentSessionID = parentSessionID
		out = append(out, Session{
			Project:   project,
			SessionID: sessionID,
			Path:      path,
			Cwd:       cwd,
			Subagent:  &sub,
		})
		return nil
	})
	return out
}

// spoolSidecarRegular reports whether a spool sidecar may be read. The walk
// visits sidecars as entries and logs a refusal there, so this only keeps the
// read off one; refusing costs the metadata, and the transcript ships as it
// would with no sidecar at all.
func spoolSidecarRegular(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular()
}

// readClaudeSubagentMeta reads the agent-<id>.meta.json sidecar written
// beside a subagent transcript. A missing or unparseable sidecar is not an
// error: the transcript still ships, just without the metadata. All 1972
// transcripts on the machine this was measured on carry one — the 13 files
// that did not were the workflow journals, which no longer enumerate — but
// the sidecar is the agent's to write, not ours to require.
func readClaudeSubagentMeta(path string) Subagent {
	data, err := os.ReadFile(path)
	if err != nil {
		return Subagent{}
	}
	var meta struct {
		AgentType   string `json:"agentType"`
		Description string `json:"description"`
		ToolUseID   string `json:"toolUseId"`
		SpawnDepth  int    `json:"spawnDepth"`
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return Subagent{}
	}
	return Subagent{
		AgentType:   meta.AgentType,
		Description: meta.Description,
		ToolUseID:   meta.ToolUseID,
		SpawnDepth:  meta.SpawnDepth,
	}
}
