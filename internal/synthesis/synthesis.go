// Package synthesis builds the input a synthesis pass reads for one project
// over a window: the tickets that were created, closed or edited in it, read
// through tk, each joined to the sessions whose commits carry its `[<id>]`
// marker, plus the project's sessions in the window that landed nothing for
// any of those tickets.
package synthesis

import (
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"time"

	"github.com/EnderRealm/ticket/v8/pkg/ticket"

	"loom/internal/summaries"
)

// The signals a ticket carries into synthesis. A ticket created and closed in
// the same window carries both new and done; edited is only ever alone.
const (
	SignalNew    = "new"    // created in the window: what the project decided it still needs
	SignalDone   = "done"   // closed in the window: what shipped, or was rejected
	SignalEdited = "edited" // written in the window, neither created nor closed in it: scope that moved
)

// Input is the whole synthesis input for one project and window.
type Input struct {
	Project            string    `json:"project"`
	Since              time.Time `json:"since"`
	Until              time.Time `json:"until"`
	Tickets            []Ticket  `json:"tickets"`
	UnticketedSessions []Session `json:"unticketed_sessions"`
}

// Ticket is one ticket with a signal in the window and the sessions that
// landed commits for it, at any time.
type Ticket struct {
	ID                 string     `json:"id"`
	Title              string     `json:"title"`
	Status             string     `json:"status"`
	Type               string     `json:"type"`
	Parent             string     `json:"parent,omitempty"`
	Created            *time.Time `json:"created,omitempty"`
	Updated            *time.Time `json:"updated,omitempty"`
	Closed             *time.Time `json:"closed,omitempty"`
	Signals            []string   `json:"signals"`
	Description        string     `json:"description,omitempty"`
	Design             string     `json:"design,omitempty"`
	AcceptanceCriteria string     `json:"acceptance_criteria,omitempty"`
	Sessions           []Session  `json:"sessions"`
}

// Session is one summarized session. Under a ticket, Commits are the ones
// marked for that ticket; as an unticketed session, all of its commits.
type Session struct {
	Agent     string     `json:"agent"`
	SessionID string     `json:"session_id"`
	Start     *time.Time `json:"start,omitempty"`
	End       *time.Time `json:"end,omitempty"`
	Cwd       string     `json:"cwd,omitempty"`
	Commits   []Commit   `json:"commits"`
}

// Commit is one commit a session landed.
type Commit struct {
	Hash        string     `json:"hash"`
	Subject     string     `json:"subject"`
	Branch      string     `json:"branch,omitempty"`
	CommittedAt *time.Time `json:"committed_at,omitempty"`
}

// Build assembles the input for project over [since, until). Tickets come from
// the tk CLI and sessions from summaries.db; diag receives tk's stderr and the
// counts behind the result.
func Build(project string, since, until time.Time, diag io.Writer) (*Input, error) {
	all, err := queryProject(diag, project)
	if err != nil {
		return nil, err
	}

	in := func(t *time.Time) bool {
		return t != nil && !t.Before(since) && t.Before(until)
	}
	var tickets []Ticket
	var ids []string
	counts := map[string]int{}
	for _, tk := range all {
		t := Ticket{
			ID:       tk.ID,
			Title:    tk.Title,
			Status:   tk.Status,
			Type:     tk.Type,
			Parent:   tk.Parent,
			Created:  tkTime(diag, tk.ID, "created", tk.Created),
			Updated:  tkTime(diag, tk.ID, "updated", tk.Updated),
			Closed:   tkTime(diag, tk.ID, "closed", tk.Closed),
			Signals:  []string{},
			Sessions: []Session{},
		}
		if in(t.Created) {
			t.Signals = append(t.Signals, SignalNew)
		}
		if in(t.Closed) {
			t.Signals = append(t.Signals, SignalDone)
		}
		if len(t.Signals) == 0 && in(t.Updated) {
			t.Signals = append(t.Signals, SignalEdited)
		}
		if len(t.Signals) == 0 {
			continue
		}
		for _, s := range t.Signals {
			counts[s]++
		}
		tickets = append(tickets, t)
		ids = append(ids, t.ID)
	}
	sort.Slice(tickets, func(i, j int) bool { return tickets[i].ID < tickets[j].ID })
	sort.Strings(ids)
	fmt.Fprintf(diag, "tk: %d ticket(s) in %s, %d with a signal in the window (new %d, done %d, edited %d)\n",
		len(all), project, len(tickets), counts[SignalNew], counts[SignalDone], counts[SignalEdited])

	bodies, err := showBodies(diag, project, ids)
	if err != nil {
		return nil, err
	}
	for i := range tickets {
		b := bodies[tickets[i].ID]
		tickets[i].Description = b.Description
		tickets[i].Design = b.Design
		tickets[i].AcceptanceCriteria = b.AcceptanceCriteria
	}

	spans, commits, err := summaries.LoadSessionsAndCommits()
	if err != nil {
		return nil, err
	}
	spanOf := map[string]summaries.SessionSpan{}
	for _, s := range spans {
		spanOf[summaries.SessionKey(s.Agent, s.SessionID)] = s
	}
	index := map[string]int{}
	for i, t := range tickets {
		index[t.ID] = i
	}

	// Per ticket, the sessions whose commits carry its marker, each with those
	// commits, in the order the commits were read.
	ticketed := map[string]bool{}
	bySession := map[string][]summaries.SessionCommit{}
	for _, c := range commits {
		key := summaries.SessionKey(c.Agent, c.SessionID)
		bySession[key] = append(bySession[key], c)
		i, ok := index[c.TicketID]
		if !ok {
			continue
		}
		ticketed[key] = true
		t := &tickets[i]
		j := len(t.Sessions) - 1
		for ; j >= 0; j-- {
			if t.Sessions[j].Agent == c.Agent && t.Sessions[j].SessionID == c.SessionID {
				break
			}
		}
		if j < 0 {
			t.Sessions = append(t.Sessions, newSession(c.Agent, c.SessionID, spanOf[key]))
			j = len(t.Sessions) - 1
		}
		t.Sessions[j].Commits = append(t.Sessions[j].Commits, newCommit(c))
	}
	joined := 0
	for i := range tickets {
		sortSessions(tickets[i].Sessions)
		if len(tickets[i].Sessions) > 0 {
			joined++
		}
	}

	// The project's sessions active in the window that landed nothing marked for
	// a ticket above. A session belongs to the project when its checkout
	// resolves to the project's tk namespace.
	unticketed := []Session{}
	resolved := map[string]string{}
	var active, attributed, noCwd int
	for _, s := range spans {
		start, end := s.Start, s.End
		if end.IsZero() {
			end = start
		}
		if end.IsZero() || end.Before(since) || (!start.IsZero() && !start.Before(until)) {
			continue
		}
		active++
		cwd := s.CwdRaw
		if cwd == "" {
			cwd = s.Cwd
		}
		if cwd == "" {
			noCwd++
			continue
		}
		ns, ok := resolved[cwd]
		if !ok {
			ns = namespaceFor(diag, cwd)
			resolved[cwd] = ns
		}
		if ns != project {
			continue
		}
		attributed++
		key := summaries.SessionKey(s.Agent, s.SessionID)
		if ticketed[key] {
			continue
		}
		sess := newSession(s.Agent, s.SessionID, s)
		for _, c := range bySession[key] {
			sess.Commits = append(sess.Commits, newCommit(c))
		}
		unticketed = append(unticketed, sess)
	}
	sortSessions(unticketed)
	fmt.Fprintf(diag, "summaries: %d ticket(s) joined to at least one session; %d session(s) active in the window, %d without a cwd, %d attributed to %s, %d unticketed\n",
		joined, active, noCwd, attributed, project, len(unticketed))

	if tickets == nil {
		tickets = []Ticket{}
	}
	return &Input{
		Project:            project,
		Since:              since,
		Until:              until,
		Tickets:            tickets,
		UnticketedSessions: unticketed,
	}, nil
}

// namespaceFor resolves a session's checkout to its tk namespace through the
// same resolution tk and the dashboard use (ticket.ResolveStoreForRepo wraps
// it): a configured path, the git remote, then the directory name. "" when it
// resolves to no project. A relative path would resolve against this
// process's directory rather than the session's, so it resolves to nothing.
func namespaceFor(diag io.Writer, cwd string) string {
	if !filepath.IsAbs(cwd) {
		return ""
	}
	store, _, ok, err := ticket.CentralStoreForRepo(cwd)
	if err != nil {
		fmt.Fprintf(diag, "resolve %s: %v — not attributed\n", cwd, err)
		return ""
	}
	if !ok {
		return ""
	}
	return store.Project
}

func newSession(agent, sessionID string, span summaries.SessionSpan) Session {
	s := Session{Agent: agent, SessionID: sessionID, Commits: []Commit{}}
	if !span.Start.IsZero() {
		s.Start = &span.Start
	}
	if !span.End.IsZero() {
		s.End = &span.End
	}
	s.Cwd = span.CwdRaw
	if s.Cwd == "" {
		s.Cwd = span.Cwd
	}
	return s
}

func newCommit(c summaries.SessionCommit) Commit {
	out := Commit{Hash: c.Hash, Subject: c.Subject, Branch: c.Branch}
	if !c.CommittedAt.IsZero() {
		at := c.CommittedAt
		out.CommittedAt = &at
	}
	return out
}

// sortSessions orders sessions by start, unknown starts last, with the session
// id breaking ties so the output is the same on every run.
func sortSessions(ss []Session) {
	sort.SliceStable(ss, func(i, j int) bool {
		a, b := ss[i].Start, ss[j].Start
		switch {
		case a != nil && b != nil && !a.Equal(*b):
			return a.Before(*b)
		case (a == nil) != (b == nil):
			return a != nil
		}
		return ss[i].SessionID < ss[j].SessionID
	})
}
