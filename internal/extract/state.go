package extract

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
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
	data, err := os.ReadFile(statePath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			s := &state{Watermark: time.Now().UTC(), Sessions: map[string]record{}}
			if err := s.save(); err != nil {
				return nil, err
			}
			return s, nil
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
	if err := s.save(); err != nil {
		log.Printf("save extract state: %v", err)
	}
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
