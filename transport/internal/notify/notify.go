// Package notify emits macOS user notifications for shipper failures and
// persists the dedupe / recovery state that shapes them. Notifications always
// include the current pending-session count and a human-readable "last sync
// N ago" so the user can judge severity at a glance.
package notify

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"loom/internal/config"
)

// Kind identifies one class of notification. Cooldowns are keyed by Kind.
type Kind string

const (
	KindReceiverDown Kind = "receiver_down" // health check failed
	KindShipFailed   Kind = "ship_failed"   // one or more sessions exhausted retries
	KindLocalError   Kind = "local_error"   // capture/io/cursor error
	KindRecovered    Kind = "recovered"     // first all-green tick after a failure
)

// State is the persisted health view consulted by each tick.
type State struct {
	LastSuccessTS    time.Time       `json:"last_success_ts,omitempty"`
	LastNotifiedKind Kind            `json:"last_notified_kind,omitempty"`
	LastNotifiedTS   time.Time       `json:"last_notified_ts,omitempty"`
	PendingSessions  map[string]bool `json:"pending_sessions,omitempty"` // key: "<agent>/<session_id>"
	// FailureActive is true when the most recent notification was a failure
	// and we haven't yet fired "recovered". Used to gate the recovery notice.
	FailureActive bool `json:"failure_active,omitempty"`
}

func statePath() string {
	return filepath.Join(config.TransportDir(), "notify.state")
}

// LoadState reads the state file; missing file returns a zero State.
func LoadState() (*State, error) {
	data, err := os.ReadFile(statePath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &State{PendingSessions: map[string]bool{}}, nil
		}
		return nil, err
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parse notify state: %w", err)
	}
	if s.PendingSessions == nil {
		s.PendingSessions = map[string]bool{}
	}
	return &s, nil
}

// Save writes state atomically via temp-file + rename.
func (s *State) Save() error {
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

// SessionKey is the canonical "<agent>/<session_id>" key used in PendingSessions.
func SessionKey(agent, sessionID string) string {
	return agent + "/" + sessionID
}

// Event is one notification the shipper wants to fire; the notifier decides
// whether to actually emit it given cooldown + recovery rules.
type Event struct {
	Kind    Kind
	Title   string
	Summary string // short, prepended to the auto-appended "M pending, last sync …" suffix
	Now     time.Time
}

// Maybe evaluates cooldown + dedupe rules against the state, and if the event
// should fire, runs osascript and updates the state in place. Returns whether
// a notification was actually emitted.
//
// Behavior:
//   - Recovery fires iff FailureActive is true AND the caller picked Kind=Recovered.
//   - Failure events are suppressed if LastNotifiedKind==Kind AND we're still
//     inside NotifyCooldownMinutes. Crossing to a different kind always fires.
//   - Cooldown < 0 disables suppression (fire every tick).
func (s *State) Maybe(cfg *config.Config, ev Event) (bool, error) {
	if !cfg.NotifyEnabled() {
		return false, nil
	}
	cooldown := time.Duration(cfg.NotifyCooldownMinutes) * time.Minute
	if ev.Kind == KindRecovered && !s.FailureActive {
		return false, nil
	}
	if ev.Kind != KindRecovered &&
		cooldown >= 0 &&
		s.LastNotifiedKind == ev.Kind &&
		!s.LastNotifiedTS.IsZero() &&
		ev.Now.Sub(s.LastNotifiedTS) < cooldown {
		return false, nil
	}

	body := buildBody(s, ev)
	if err := osaNotify(ev.Title, body); err != nil {
		return false, err
	}

	s.LastNotifiedKind = ev.Kind
	s.LastNotifiedTS = ev.Now
	s.FailureActive = ev.Kind != KindRecovered
	return true, nil
}

// buildBody appends pending-count + last-sync-ago context to the caller's summary.
func buildBody(s *State, ev Event) string {
	pending := len(s.PendingSessions)
	var last string
	switch {
	case ev.Kind == KindRecovered:
		last = formatAgo(s.LastNotifiedTS, ev.Now, "last failure %s ago", "last failure never")
	default:
		last = formatAgo(s.LastSuccessTS, ev.Now, "last sync %s ago", "last sync never")
	}
	return fmt.Sprintf("%s. %d session%s pending, %s.",
		strings.TrimRight(ev.Summary, "."),
		pending, plural(pending), last)
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// formatAgo returns "<have> Xd Yh ago" / "<have> Xh Ym ago" / "<missing>".
func formatAgo(ts, now time.Time, haveFmt, missing string) string {
	if ts.IsZero() {
		return missing
	}
	return fmt.Sprintf(haveFmt, FormatDuration(now.Sub(ts)))
}

// FormatDuration renders a coarse "Xd Yh" or "Xh Ym" suffix.
func FormatDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	days := int(d / (24 * time.Hour))
	hours := int((d % (24 * time.Hour)) / time.Hour)
	mins := int((d % time.Hour) / time.Minute)
	if days > 0 {
		return fmt.Sprintf("%dd %dh", days, hours)
	}
	return fmt.Sprintf("%dh %dm", hours, mins)
}

// osaNotify shells out to osascript. macOS-only; on other OSes it's a no-op
// (the shipper only targets macOS today, but this keeps `go build` portable).
func osaNotify(title, body string) error {
	script := fmt.Sprintf(`display notification %q with title %q`, body, title)
	cmd := exec.Command("osascript", "-e", script)
	return cmd.Run()
}
