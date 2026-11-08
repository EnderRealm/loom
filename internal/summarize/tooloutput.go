package summarize

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"loom/internal/parse/claudeparse"
	"loom/internal/parse/codexparse"
	"loom/internal/parse/cursorparse"
	"loom/internal/parse/summary"
	"loom/internal/runs"
)

// ToolOutputQuery selects tool calls from the received tree: started in
// [Since, Until), whose key argument contains Command (any when empty) and
// whose whole output matches Match.
type ToolOutputQuery struct {
	ReceivedDir string
	// DBPath is the summaries.db tickets are attributed from. Absent means
	// every ticket is unknown, not a failure.
	DBPath  string
	Since   time.Time
	Until   time.Time
	Match   *regexp.Regexp
	Command string
}

// ToolOutputMatch is one tool call whose output matched. Ticket is nil when
// no run in summaries.db places the call under one ticket.
type ToolOutputMatch struct {
	Agent     string    `json:"agent"`
	SessionID string    `json:"session_id"`
	Ticket    *string   `json:"ticket"`
	StartedAt time.Time `json:"started_at"`
	KeyArg    string    `json:"key_arg"`
	Output    string    `json:"output"`
}

// ToolOutputFailure is one input the query could not read through: a
// transcript, a directory of them, or summaries.db.
type ToolOutputFailure struct {
	Path  string `json:"path"`
	Error string `json:"error"`
}

// ToolOutput reads every received transcript that can hold a call in the
// window and returns the matching calls ordered by start time, and every
// input it could not read. Nothing is written: transcripts and summaries.db
// are opened read-only and no state is kept between queries.
//
// A transcript whose mtime, and for Claude every subagent transcript's, is
// before Since is not read: a record lands in its file before the file's
// last write. That holds on one clock; a producer host whose clock runs
// ahead of the receiver's can place a call past the file's mtime.
func ToolOutput(q ToolOutputQuery) ([]ToolOutputMatch, []ToolOutputFailure) {
	matches := []ToolOutputMatch{}
	var failures []ToolOutputFailure
	fail := func(path string, err error) {
		failures = append(failures, ToolOutputFailure{Path: path, Error: err.Error()})
	}

	tickets, err := loadTicketIndex(q.DBPath)
	if err != nil {
		fail(q.DBPath, err)
	}
	collect := func(agent summary.Agent, sessionID string, calls []summary.ToolCall) {
		for _, tc := range calls {
			if tc.StartedAt.Before(q.Since) || !tc.StartedAt.Before(q.Until) {
				continue
			}
			if !strings.Contains(tc.KeyArg, q.Command) || !q.Match.MatchString(tc.Output) {
				continue
			}
			matches = append(matches, ToolOutputMatch{
				Agent:     string(agent),
				SessionID: sessionID,
				Ticket:    tickets.ticket(string(agent), sessionID, tc.StartedAt),
				StartedAt: tc.StartedAt,
				KeyArg:    tc.KeyArg,
				Output:    tc.Output,
			})
		}
	}

	for _, agent := range []summary.Agent{summary.AgentClaude, summary.AgentCodex, summary.AgentCursor} {
		root := filepath.Join(q.ReceivedDir, string(agent))
		if _, err := os.Stat(root); errors.Is(err, fs.ErrNotExist) {
			continue
		}
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				fail(path, err)
				return nil
			}
			// Claude subagent transcripts are read with their parent, which
			// names the dispatch each one answers.
			if d.IsDir() {
				if agent == summary.AgentClaude && d.Name() == "subagents" {
					return fs.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".jsonl") {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				fail(path, err)
				return nil
			}
			var subs []string
			if agent == summary.AgentClaude {
				if subs, err = subagentPaths(path); err != nil {
					fail(subagentDir(path), err)
				}
			}
			if staleBefore(info.ModTime(), subs, q.Since) {
				return nil
			}
			if agent == summary.AgentCodex {
				imported, err := codexImportCopy(path)
				if err != nil {
					fail(path, err)
					return nil
				}
				// A copy of a session already received under claude-code.
				if imported {
					return nil
				}
			}

			f, err := os.Open(path)
			if err != nil {
				fail(path, err)
				return nil
			}
			var sum *summary.SessionSummary
			readErrs := make([]error, len(subs))
			switch agent {
			case summary.AgentClaude:
				sum, err = claudeparse.ParseWithSubagents(f, subagentInputs(subs, readErrs))
			case summary.AgentCodex:
				sum, err = codexparse.Parse(f)
			case summary.AgentCursor:
				sum, err = cursorparse.Parse(f)
			}
			f.Close()
			if err != nil {
				fail(path, err)
				return nil
			}
			if sum.SessionID == "" {
				sum.SessionID = strings.TrimSuffix(filepath.Base(path), ".jsonl")
			}
			collect(agent, sum.SessionID, sum.ToolCalls)
			for _, sa := range sum.Subagents {
				if sa.Session != nil {
					collect(agent, sa.SessionID, sa.Session.ToolCalls)
				}
			}
			for i, err := range readErrs {
				if err != nil {
					fail(subs[i], err)
				}
			}
			return nil
		})
	}

	sort.SliceStable(matches, func(i, j int) bool {
		a, b := matches[i], matches[j]
		if !a.StartedAt.Equal(b.StartedAt) {
			return a.StartedAt.Before(b.StartedAt)
		}
		if a.Agent != b.Agent {
			return a.Agent < b.Agent
		}
		return a.SessionID < b.SessionID
	})
	return matches, failures
}

// staleBefore reports whether a transcript and its subagent transcripts were
// all last written before since. A subagent that cannot be stat'ed counts as
// fresh, so the read that follows reports it.
func staleBefore(mtime time.Time, subs []string, since time.Time) bool {
	if !mtime.Before(since) {
		return false
	}
	for _, p := range subs {
		info, err := os.Stat(p)
		if err != nil || !info.ModTime().Before(since) {
			return false
		}
	}
	return true
}

// subagentPaths lists the subagent transcripts one Claude session dispatched,
// the same files collectSubagents folds. No directory is no subagents.
func subagentPaths(sessionPath string) ([]string, error) {
	dir := subagentDir(sessionPath)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		out = append(out, filepath.Join(dir, e.Name()))
	}
	return out, nil
}

// subagentInputs is collectSubagents for a reader that must name every
// transcript it could not read: the parser folds an unreadable one as
// unmeasured and carries on, so each open and read error is kept in errs at
// the transcript's index.
func subagentInputs(paths []string, errs []error) []claudeparse.SubagentInput {
	inputs := make([]claudeparse.SubagentInput, len(paths))
	for i, path := range paths {
		in := readSubagentSidecar(path)
		in.SessionID = strings.TrimSuffix(filepath.Base(path), ".jsonl")
		in.Open = func() (io.ReadCloser, error) {
			f, err := os.Open(path)
			if err != nil {
				errs[i] = err
				return nil, err
			}
			return &recordingReader{ReadCloser: f, err: &errs[i]}, nil
		}
		inputs[i] = in
	}
	return inputs
}

// recordingReader keeps the first read error other than EOF.
type recordingReader struct {
	io.ReadCloser
	err *error
}

func (r *recordingReader) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	if err != nil && err != io.EOF && *r.err == nil {
		*r.err = err
	}
	return n, err
}

// ticketIndex places a session's calls under the runs summaries.db knows.
// A session that is a run's child — a subagent, a lens session — belongs to
// that run outright; a run's root session can hold several runs, so a call
// there belongs to the run whose span holds its start.
type ticketIndex struct {
	children map[runs.TranscriptRef]map[string]bool
	roots    map[runs.TranscriptRef][]runSpan
}

type runSpan struct {
	ticket     string
	start, end time.Time
}

// loadTicketIndex reads every run in summaries.db read-only. A missing
// database yields an empty index; a nil index answers unknown.
func loadTicketIndex(dbPath string) (*ticketIndex, error) {
	if _, err := os.Stat(dbPath); errors.Is(err, fs.ErrNotExist) {
		return &ticketIndex{}, nil
	}
	// Always immutable: SQLite then reads the main file alone and never
	// creates or touches -wal or -shm, whatever a concurrent summarizer does —
	// a mode=ro reader recreates both if the writer removes them between any
	// check and the open. The cost is that rows still only in the -wal are not
	// seen, so a run the summarizer has not yet checkpointed comes back with a
	// null ticket. A checkpoint that writes the main file mid-read can also
	// fail the read or yield wrong tickets; that is accepted because a query's
	// window on the file is seconds.
	dsn := "file:" + (&url.URL{Path: dbPath}).EscapedPath() + "?mode=ro&immutable=1"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open summaries.db: %w", err)
	}
	defer db.Close()
	list, err := runs.List(db, time.Time{}, time.Time{})
	if err != nil {
		return nil, fmt.Errorf("tickets unattributed: %w", err)
	}
	idx := &ticketIndex{
		children: map[runs.TranscriptRef]map[string]bool{},
		roots:    map[runs.TranscriptRef][]runSpan{},
	}
	for _, r := range list {
		if r.Transcript != nil {
			idx.roots[*r.Transcript] = append(idx.roots[*r.Transcript], runSpan{
				ticket: r.Ticket,
				start:  parseRunTime(r.StartedAt),
				end:    parseRunTime(r.EndedAt),
			})
		}
		if r.Root == nil {
			continue
		}
		var walk func(n *runs.Node)
		walk = func(n *runs.Node) {
			for _, c := range n.Children {
				if c.Transcript != nil && (r.Transcript == nil || *c.Transcript != *r.Transcript) {
					if idx.children[*c.Transcript] == nil {
						idx.children[*c.Transcript] = map[string]bool{}
					}
					idx.children[*c.Transcript][r.Ticket] = true
				}
				walk(c)
			}
		}
		walk(r.Root)
	}
	return idx, nil
}

// ticket is the one ticket the runs place a call under, nil when none does
// or the candidates disagree. A run with no end is open.
func (idx *ticketIndex) ticket(agent, sessionID string, at time.Time) *string {
	if idx == nil {
		return nil
	}
	ref := runs.TranscriptRef{Agent: agent, SessionID: sessionID}
	found := map[string]bool{}
	if set, ok := idx.children[ref]; ok {
		found = set
	} else {
		for _, s := range idx.roots[ref] {
			if !s.start.IsZero() && !at.Before(s.start) && (s.end.IsZero() || at.Before(s.end)) {
				found[s.ticket] = true
			}
		}
	}
	if len(found) != 1 {
		return nil
	}
	for t := range found {
		if t != "" {
			return &t
		}
	}
	return nil
}

func parseRunTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}
