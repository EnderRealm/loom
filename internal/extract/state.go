package extract

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"loom/internal/config"
)

// Outcomes recorded per visited session. Every one of them means "don't visit
// this session again"; the reason is what makes the skipped and failed sets
// auditable without re-deriving them.
const (
	outcomeExtracted = "extracted"
	outcomeSkipped   = "skipped"
	outcomeFailed    = "failed"
)

// state is the at-most-once ledger: one record per session the trigger has
// visited. It lives beside the other ~/.loom state files rather than in
// summaries.db, which is disposable and rebuilt from received/ — a rebuild
// must not cause every session to be extracted again.
type state struct {
	// Watermark is stamped when the ledger is created and bounds the sweep to
	// sessions summarized at or after it. Everything older is the historical
	// gap, which belongs to the batch runner (loom/batch-runner-session-12da)
	// and not to an unattended agent spending the user's LLM quota.
	Watermark time.Time         `json:"watermark"`
	Sessions  map[string]record `json:"sessions"`
}

type record struct {
	Outcome    string    `json:"outcome"`
	Scope      string    `json:"scope,omitempty"`
	Reason     string    `json:"reason,omitempty"`
	Candidates int       `json:"candidates,omitempty"`
	Score      float64   `json:"score,omitempty"`
	At         time.Time `json:"at"`
}

func statePath() string {
	return filepath.Join(config.Home(), "extract.state")
}

func sessionKey(agent, sessionID string) string {
	return agent + "/" + sessionID
}

// loadState reads the ledger. A missing file is a first run: the ledger is
// created with its watermark stamped at now and persisted immediately, so the
// bound is fixed at install rather than drifting forward one sweep at a time.
func loadState() (*state, error) {
	if _, err := os.Stat(statePath()); errors.Is(err, os.ErrNotExist) {
		s := &state{Watermark: time.Now().UTC(), Sessions: map[string]record{}}
		if err := s.save(); err != nil {
			return nil, err
		}
		return s, nil
	}
	return peekState()
}

// peekState reads the ledger without creating it. A dry run must leave
// ~/.loom/extract.state byte-identical, which on a host where the trigger has
// never run means not stamping a watermark either.
func peekState() (*state, error) {
	data, err := os.ReadFile(statePath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &state{Sessions: map[string]record{}}, nil
		}
		return nil, err
	}
	var s state
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parse extract state: %w", err)
	}
	if s.Sessions == nil {
		s.Sessions = map[string]record{}
	}
	return &s, nil
}

func (s *state) visited(agent, sessionID string) bool {
	_, ok := s.Sessions[sessionKey(agent, sessionID)]
	return ok
}

// mark records one session's outcome and persists immediately, so a sweep
// interrupted mid-backlog (extraction takes minutes) doesn't re-run the
// sessions it already finished.
func (s *state) mark(agent, sessionID string, r record) {
	r.At = time.Now().UTC()
	s.Sessions[sessionKey(agent, sessionID)] = r
	if err := s.commit(); err != nil {
		log.Printf("save extract state: %v", err)
	}
}

// commit folds the on-disk ledger into this snapshot and rewrites it, both
// under an exclusive file lock.
//
// Two writers share the ledger by design: an operator's backfill runs for
// hours off one snapshot while the installed trigger sweeps every 15 minutes
// off its own. save rewrites the whole file, so without this each would erase
// the other's records — and an erased session reads as unvisited, so it is
// extracted a second time at real LLM cost, which is exactly the at-most-once
// guarantee the ledger exists to provide. flock rather than a mutex because
// the writers are separate processes; the reload is what makes the rewrite
// merge rather than overwrite. One extra read per mark, against minutes of
// extraction per session.
func (s *state) commit() error {
	unlock, err := lockState()
	if err != nil {
		return err
	}
	defer unlock()

	cur, err := peekState()
	if err != nil {
		return err
	}
	for k, v := range cur.Sessions {
		// This snapshot wins for keys it holds: they are what this process just
		// recorded. Both writers consult the ledger immediately before
		// extracting, so a key here is one the other had no reason to record;
		// if it raced and did, the two records describe the same run.
		if _, ok := s.Sessions[k]; !ok {
			s.Sessions[k] = v
		}
	}
	return s.save()
}

// visitedOnDisk reports whether the ledger currently on disk holds this
// session. An in-memory snapshot only knows what was recorded when it was
// loaded, which for a run lasting hours is not what the other writer has done
// since. Read under the same lock commit takes, so the answer is never a view
// of a rewrite in progress.
func visitedOnDisk(agent, sessionID string) (bool, error) {
	unlock, err := lockState()
	if err != nil {
		return false, err
	}
	defer unlock()

	cur, err := peekState()
	if err != nil {
		return false, err
	}
	return cur.visited(agent, sessionID), nil
}

// lockState takes a blocking exclusive lock on a sidecar file, held across
// commit's read-modify-write and visitedOnDisk's read. The lock is not taken
// on extract.state itself
// because save renames a fresh file over it, which would leave the lock held
// on an inode no other process can reach.
func lockState() (func(), error) {
	p := statePath() + ".lock"
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	// Closing the descriptor releases the lock.
	return func() { f.Close() }, nil
}

// save writes the ledger atomically via temp-file + rename.
func (s *state) save() error {
	p := statePath()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}
