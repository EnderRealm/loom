package synthesis

import (
	"bytes"
	"database/sql"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/EnderRealm/ticket/v8/pkg/ticket"

	"loom/internal/summaries"
)

// ProjectState computes the standing state of each scope over the window
// ending at now, from one `tk query --all-projects` and the sessions and
// commits in db. A scope is matched to tickets by tk namespace, so it counts
// only where the two names agree.
//
// Attribution follows Build: a session belongs to the namespace its checkout
// resolves to. A commit belongs to the namespace its `[<id>]` marker names,
// which survives a commit made from another checkout or from a worktree that
// no longer resolves; an unmarked commit, or one whose marker carries no
// namespace, belongs to its session's. A commit read twice — its hash under
// two sessions — counts once.
//
// tk's stderr rides on the error when the query fails, since diag is discarded
// when the summarizer runs without -v, and goes to diag otherwise.
func ProjectState(db *sql.DB, scopes []string, window time.Duration, now time.Time, diag io.Writer) ([]summaries.ProjectState, error) {
	var tkStderr bytes.Buffer
	tickets, err := queryAll(&tkStderr)
	if err != nil {
		if msg := strings.TrimSpace(tkStderr.String()); msg != "" {
			return nil, fmt.Errorf("%w: %s", err, msg)
		}
		return nil, err
	}
	io.Copy(diag, &tkStderr)
	spans, commits, err := summaries.SessionsAndCommits(db)
	if err != nil {
		return nil, err
	}

	since := now.Add(-window)
	in := func(t time.Time) bool { return !t.IsZero() && !t.Before(since) && t.Before(now) }
	later := func(cur *time.Time, t time.Time) *time.Time {
		if t.IsZero() || (cur != nil && !t.After(*cur)) {
			return cur
		}
		return &t
	}

	byScope := map[string]*summaries.ProjectState{}
	out := make([]summaries.ProjectState, len(scopes))
	for i, s := range scopes {
		out[i] = summaries.ProjectState{Project: s, ComputedAt: now, WindowSeconds: int64(window / time.Second)}
		byScope[s] = &out[i]
	}

	var tracked int
	for _, t := range tickets {
		ns, _ := ticket.ParseNamespacedID(t.ID)
		st, ok := byScope[ns]
		if !ok {
			continue
		}
		tracked++
		if s := ticket.Status(t.Status); s != ticket.StatusDone && s != ticket.StatusClosed {
			st.OpenTickets++
		}
		if closed := tkTime(diag, t.ID, "closed", t.Closed); closed != nil {
			st.LastTicketClosedAt = later(st.LastTicketClosedAt, *closed)
			if in(*closed) {
				st.TicketsClosedInWindow++
			}
		}
	}

	sessionNS := map[string]string{}
	resolved := map[string]string{}
	var attributedSessions int
	for _, s := range spans {
		cwd := s.CwdRaw
		if cwd == "" {
			cwd = s.Cwd
		}
		if cwd == "" {
			continue
		}
		ns, ok := resolved[cwd]
		if !ok {
			ns = namespaceFor(diag, cwd)
			resolved[cwd] = ns
		}
		sessionNS[summaries.SessionKey(s.Agent, s.SessionID)] = ns
		st, ok := byScope[ns]
		if !ok {
			continue
		}
		attributedSessions++
		// A span with no end time is read at its start, as Build reads it.
		start, end := s.Start, s.End
		if end.IsZero() {
			end = start
		}
		st.LastSessionAt = later(st.LastSessionAt, end)
		if !end.IsZero() && !end.Before(since) && (start.IsZero() || start.Before(now)) {
			st.SessionsInWindow++
		}
	}

	seen := map[string]bool{}
	var attributedCommits int
	for _, c := range commits {
		ns, _ := ticket.ParseNamespacedID(c.TicketID)
		if ns == "" {
			ns = sessionNS[summaries.SessionKey(c.Agent, c.SessionID)]
		}
		st, ok := byScope[ns]
		if !ok || seen[ns+"\x00"+c.Hash] {
			continue
		}
		seen[ns+"\x00"+c.Hash] = true
		attributedCommits++
		st.LastCommitAt = later(st.LastCommitAt, c.CommittedAt)
		if in(c.CommittedAt) {
			st.CommitsInWindow++
		}
	}

	for i := range out {
		st := &out[i]
		st.Dormant = st.CommitsInWindow == 0 && st.SessionsInWindow == 0 && st.TicketsClosedInWindow == 0
	}
	fmt.Fprintf(diag, "project state: %d scope(s); tk %d ticket(s), %d in a scope; summaries %d session(s) over %d checkout(s), %d in a scope; %d commit(s) in a scope\n",
		len(scopes), len(tickets), tracked, len(spans), len(resolved), attributedSessions, attributedCommits)
	return out, nil
}
