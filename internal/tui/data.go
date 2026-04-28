package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"loom/internal/config"

	"github.com/EnderRealm/ticket/pkg/ticket"
)

// Project aggregates session and ticket state for one identity-keyed
// project. Identity precedence: GitRemote > Cwd > slug. Worktrees (e.g.
// Forge's <project>/.claude/worktrees/<branch>) roll up under their
// parent project via GitRemote, so a single Project can span multiple
// slugs and worktrees.
type Project struct {
	Name      string // display name, e.g. "Loom"
	Slug      string // representative slug; one of many in Slugs
	Path      string // local cwd if it resolves to a real directory, else ""
	GitRemote string // origin URL when a meta sidecar / summary row carried one
	Cwd       string // raw cwd from sidecar / summary; authoritative when no remote

	Slugs        []string
	Worktrees    []string // display-only labels parsed from worktree-shaped slugs
	Agents       []string
	SessionCount int
	BytesTotal   int64
	LastActivity time.Time
	PendingBytes int64
	PendingCount int
	Sessions     []Session
	Tickets      *TicketSummary

	// Summary metrics rolled up from ~/.loom/summaries.db. Zero when the
	// summarizer hasn't run yet for this project's sessions.
	TurnCount     int
	ToolCallCount int
	ErrorCount    int
	Compactions   int
	TopTools      []ToolStat
}

// Session is one agent session tied to a project, with both on-disk
// (received) and in-flight (staging/ship) state collapsed into one row.
type Session struct {
	Agent        string
	SessionID    string
	Slug         string // slug the session was captured under (root or worktree)
	Worktree     string // worktree label if the slug is a worktree of the project
	Received     bool
	Staged       bool
	ReceivedSize int64
	StageSize    int64
	ShipCursor   int64
	Pending      int64
	Modified     time.Time

	// Summary metrics from ~/.loom/summaries.db. Zero when the summarizer
	// hasn't processed this session yet (e.g. brand new or DB missing).
	Summary *SessionMetrics
}

// TicketSummary is the rollup for one project's central ticket store.
type TicketSummary struct {
	ProjectName string
	Dir         string
	Total       int
	Status      map[string]int
	Type        map[string]int
	Priority    map[int]int
	OpenTop     []*ticket.Ticket
}

// LoadProjects walks the loom state tree and returns one Project per
// authoritative identity (git remote, then raw cwd, then slug as
// fallback for legacy sessions), sorted by most-recent activity.
//
// Identity precedence is what makes cross-machine grouping correct:
// `steve/code/loom` and `smacbeth/code/loom` shipped from different
// laptops resolve to the same git remote and roll up into one project.
// Pre-identity sessions (legacy capture, no meta sidecar, no DB row
// yet) keep the old slug-only behavior — they just don't merge across
// machines until they're re-summarized after this binary runs.
func LoadProjects() ([]Project, error) {
	home := config.Home()
	received := filepath.Join(home, "received")
	staging := filepath.Join(home, "transport", "staging")

	// Pull summary metrics first so their identity columns drive the
	// grouping decision, not the slug we're about to walk past.
	summary, _ := LoadSummaryData()

	projects := map[string]*Project{}
	// projectKey -> (agent + "\x00" + sessionID) -> session index.
	sessionIdx := map[string]map[string]int{}

	// resolveIdentity returns the authoritative (key, gitRemote, cwd,
	// displayName, localPath) for a session, drawing from summaries.db
	// (preferred) and the on-disk meta sidecar (fallback for sessions
	// the summarizer hasn't folded yet).
	resolveIdentity := func(agent, slug, sid, jsonlPath string) (key, gitRemote, cwd, name, localPath string) {
		if summary != nil {
			if m, ok := summary.BySession[sessionKey(agent, sid)]; ok {
				if m.GitRemote != "" {
					gitRemote = m.GitRemote
				}
				if m.CwdRaw != "" {
					cwd = m.CwdRaw
				} else if m.Cwd != "" {
					cwd = m.Cwd
				}
			}
		}
		if gitRemote == "" || cwd == "" {
			if g, c := readSidecarIdentity(jsonlPath); gitRemote == "" || cwd == "" {
				if gitRemote == "" {
					gitRemote = g
				}
				if cwd == "" {
					cwd = c
				}
			}
		}
		switch {
		case gitRemote != "":
			key = "remote:" + normalizeRemote(gitRemote)
		case cwd != "":
			key = "cwd:" + cwd
		default:
			// Legacy path: no identity, group by slug + agent so the
			// dashboard still renders rows. Pre-collapse slug rules
			// stay only as a tiebreaker for sessions that predate
			// wire-level identity.
			key = "slug:" + slug
		}
		if cwd != "" {
			if fi, err := os.Stat(cwd); err == nil && fi.IsDir() {
				localPath = cwd
			}
		}
		name = displayName(gitRemote, cwd, slug)
		return
	}

	upsert := func(agent, slug, sid, jsonlPath string) (*Project, *Session) {
		key, remote, cwd, name, path := resolveIdentity(agent, slug, sid, jsonlPath)
		p, ok := projects[key]
		if !ok {
			p = &Project{
				Slug:      slug,
				Name:      name,
				Path:      path,
				GitRemote: remote,
				Cwd:       cwd,
			}
			projects[key] = p
			sessionIdx[key] = map[string]int{}
		} else {
			// Patch in fields we learned from a later session in the
			// same group (e.g. the first one had no remote, the second
			// did).
			if p.GitRemote == "" && remote != "" {
				p.GitRemote = remote
			}
			if p.Cwd == "" && cwd != "" {
				p.Cwd = cwd
			}
			if p.Path == "" && path != "" {
				p.Path = path
				p.Name = name
			}
		}
		if !contains(p.Slugs, slug) {
			p.Slugs = append(p.Slugs, slug)
		}
		// Worktree label is display-only now; surface it when the slug
		// shape clearly identifies one (Forge's `.claude/worktrees/foo`
		// convention) so the detail panel can show it.
		if _, wt := splitDisplayWorktree(slug); wt != "" && !contains(p.Worktrees, wt) {
			p.Worktrees = append(p.Worktrees, wt)
		}

		k := agent + "\x00" + sid
		if i, ok := sessionIdx[key][k]; ok {
			return p, &p.Sessions[i]
		}
		_, worktree := splitDisplayWorktree(slug)
		p.Sessions = append(p.Sessions, Session{
			Agent:     agent,
			SessionID: sid,
			Slug:      slug,
			Worktree:  worktree,
		})
		i := len(p.Sessions) - 1
		sessionIdx[key][k] = i
		p.SessionCount++
		if !contains(p.Agents, agent) {
			p.Agents = append(p.Agents, agent)
		}
		return p, &p.Sessions[i]
	}

	if err := walkAgentSlugSessions(received, func(agent, slug, sid, path string, info os.FileInfo) {
		p, s := upsert(agent, slug, sid, path)
		s.Received = true
		s.ReceivedSize = info.Size()
		if info.ModTime().After(s.Modified) {
			s.Modified = info.ModTime()
		}
		p.BytesTotal += info.Size()
		if info.ModTime().After(p.LastActivity) {
			p.LastActivity = info.ModTime()
		}
	}); err != nil {
		return nil, err
	}

	if err := walkAgentSlugSessions(staging, func(agent, slug, sid, path string, info os.FileInfo) {
		p, s := upsert(agent, slug, sid, path)
		s.Staged = true
		s.StageSize = info.Size()
		if info.ModTime().After(s.Modified) {
			s.Modified = info.ModTime()
		}
		ship, _ := readShipCursor(agent, sid)
		s.ShipCursor = ship
		if ship < info.Size() {
			s.Pending = info.Size() - ship
			p.PendingBytes += s.Pending
			p.PendingCount++
		}
		if info.ModTime().After(p.LastActivity) {
			p.LastActivity = info.ModTime()
		}
	}); err != nil {
		return nil, err
	}

	out := make([]Project, 0, len(projects))
	for _, p := range projects {
		sort.Slice(p.Sessions, func(i, j int) bool {
			return p.Sessions[i].Modified.After(p.Sessions[j].Modified)
		})
		sort.Strings(p.Agents)
		sort.Strings(p.Worktrees)
		sort.Strings(p.Slugs)
		p.Tickets = loadTicketSummary(p.Path)
		attachSummary(p, summary)
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].SessionCount != out[j].SessionCount {
			return out[i].SessionCount > out[j].SessionCount
		}
		return out[i].LastActivity.After(out[j].LastActivity)
	})
	return out, nil
}

// readSidecarIdentity reads the meta sidecar next to a session JSONL.
// The receiver writes it under received/, the shipper under staging/.
// Missing or malformed sidecar is silent — pre-identity sessions just
// fall back to slug grouping.
func readSidecarIdentity(jsonlPath string) (gitRemote, cwd string) {
	metaPath := strings.TrimSuffix(jsonlPath, ".jsonl") + ".meta.json"
	data, err := os.ReadFile(metaPath)
	if err != nil {
		return "", ""
	}
	var m struct {
		GitRemote string `json:"git_remote"`
		Cwd       string `json:"cwd"`
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return "", ""
	}
	return m.GitRemote, m.Cwd
}

// normalizeRemote collapses common URL variants of the same origin to
// one key. We strip a trailing ".git", lowercase, and convert SSH-style
// "git@host:owner/repo" to "host/owner/repo" so HTTPS and SSH clones
// of the same repo group together.
func normalizeRemote(url string) string {
	s := strings.TrimSpace(url)
	s = strings.TrimSuffix(s, ".git")
	s = strings.TrimSuffix(s, "/")
	switch {
	case strings.HasPrefix(s, "git@"):
		// git@github.com:owner/repo → github.com/owner/repo
		s = strings.TrimPrefix(s, "git@")
		s = strings.Replace(s, ":", "/", 1)
	case strings.HasPrefix(s, "ssh://"):
		s = strings.TrimPrefix(s, "ssh://")
		s = strings.TrimPrefix(s, "git@")
	case strings.HasPrefix(s, "https://"):
		s = strings.TrimPrefix(s, "https://")
	case strings.HasPrefix(s, "http://"):
		s = strings.TrimPrefix(s, "http://")
	}
	return strings.ToLower(s)
}

// displayName picks a human label for a project. Prefers the repo
// basename from a git remote (e.g. "EnderRealm/loom" → "Loom"), then
// the cwd basename, then the slug's last segment.
func displayName(gitRemote, cwd, slug string) string {
	if gitRemote != "" {
		s := normalizeRemote(gitRemote)
		// host/owner/repo or owner/repo — take the last component.
		if i := strings.LastIndex(s, "/"); i >= 0 {
			s = s[i+1:]
		}
		if s != "" {
			return titleCase(s)
		}
	}
	if cwd != "" {
		base := filepath.Base(cwd)
		if base != "" && base != "/" && base != "." {
			return titleCase(base)
		}
	}
	body := strings.TrimPrefix(slug, "-")
	parts := strings.Split(body, "-")
	if len(parts) > 0 {
		return titleCase(parts[len(parts)-1])
	}
	return slug
}

// splitDisplayWorktree separates a slug into root + worktree label for
// the detail panel. Forge stores worktrees at <project>/.claude/worktrees/
// <branch>, which sanitizes into "<root>--claude-worktrees-<branch>".
// Used for display only — grouping is driven by GitRemote/Cwd above.
func splitDisplayWorktree(slug string) (root, worktree string) {
	const marker = "--claude-worktrees-"
	if i := strings.Index(slug, marker); i > 0 {
		return slug[:i], slug[i+len(marker):]
	}
	return slug, ""
}

// projectName picks a human label. Prefers the basename of the reconstructed
// path (title-cased), falls back to the canonical grouping key.
func projectName(path, key string) string {
	if path != "" {
		if base := filepath.Base(path); base != "" && base != "/" && base != "." {
			return titleCase(base)
		}
	}
	if key != "" {
		return titleCase(key)
	}
	return key
}

// titleCase uppercases the first rune and lowercases the rest. Good enough for
// repo basenames like "forge" or "moo-rs" (which stays "Moo-rs" — intentional;
// we don't try to be clever about mixed-case repo names).
func titleCase(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	if r[0] >= 'a' && r[0] <= 'z' {
		r[0] -= 'a' - 'A'
	}
	return string(r)
}

// walkAgentSlugSessions visits every <root>/<agent>/<slug>/*.jsonl entry.
func walkAgentSlugSessions(root string, fn func(agent, slug, sid, path string, info os.FileInfo)) error {
	agents, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, a := range agents {
		if !a.IsDir() {
			continue
		}
		agent := a.Name()
		slugs, err := os.ReadDir(filepath.Join(root, agent))
		if err != nil {
			continue
		}
		for _, s := range slugs {
			if !s.IsDir() {
				continue
			}
			slug := s.Name()
			files, err := os.ReadDir(filepath.Join(root, agent, slug))
			if err != nil {
				continue
			}
			for _, f := range files {
				if f.IsDir() || !strings.HasSuffix(f.Name(), ".jsonl") {
					continue
				}
				sid := strings.TrimSuffix(f.Name(), ".jsonl")
				path := filepath.Join(root, agent, slug, f.Name())
				info, err := os.Stat(path)
				if err != nil {
					continue
				}
				fn(agent, slug, sid, path, info)
			}
		}
	}
	return nil
}

// reconstructPath inverts the agent-specific slug sanitization. Claude
// replaces '/' with '-'; Codex additionally replaces '.' with '_'. Both
// are lossy: from the slug alone we can't tell whether a '-' was a path
// separator or part of a directory name (e.g. "forge-data").
//
// To disambiguate, we try every possible split — keep the last N segments
// joined by '-' as a single basename, treat the rest as path separators,
// and stop at the first interpretation that resolves to a real directory.
// Returns "" when nothing resolves.
func reconstructPath(slug string) string {
	body := strings.TrimPrefix(slug, "-")
	parts := strings.Split(body, "-")
	for baseLen := 1; baseLen <= len(parts); baseLen++ {
		prefix := parts[:len(parts)-baseLen]
		base := strings.Join(parts[len(parts)-baseLen:], "-")
		path := "/" + strings.Join(append(prefix, base), "/")
		path = filepath.Clean(path)
		if path == "/" || path == "." {
			continue
		}
		if fi, err := os.Stat(path); err == nil && fi.IsDir() {
			return path
		}
		// Codex variant: '_' was originally '.'.
		if strings.Contains(path, "_") {
			alt := strings.ReplaceAll(path, "_", ".")
			if fi, err := os.Stat(alt); err == nil && fi.IsDir() {
				return alt
			}
		}
	}
	return ""
}

// loadTicketSummary resolves the central ticket store for projectPath via
// ticket.ResolveStoreForRepo and rolls up the list into a summary. Returns
// nil when the project can't be resolved (no config entry, no git remote,
// unresolvable path) or when the store is empty.
func loadTicketSummary(projectPath string) *TicketSummary {
	if projectPath == "" {
		return nil
	}
	store, projectName, err := ticket.ResolveStoreForRepo(projectPath)
	if err != nil {
		return nil
	}
	tickets, err := store.List()
	if err != nil || len(tickets) == 0 {
		return nil
	}
	fs, _ := store.(*ticket.FileStore)
	s := &TicketSummary{
		ProjectName: projectName,
		Status:      map[string]int{},
		Type:        map[string]int{},
		Priority:    map[int]int{},
	}
	if fs != nil {
		s.Dir = fs.Dir
	}
	for _, t := range tickets {
		s.Total++
		s.Status[string(t.Status)]++
		s.Type[string(t.Type)]++
		s.Priority[t.Priority]++
	}
	// Top-N open/ready tickets, by priority asc then Created asc.
	// Mirrors the Inbox() ordering used by the ticket TUI.
	var open []*ticket.Ticket
	for _, t := range tickets {
		if t.Status == ticket.StatusOpen || t.Status == ticket.StatusReady {
			open = append(open, t)
		}
	}
	sort.Slice(open, func(i, j int) bool {
		if open[i].Priority != open[j].Priority {
			return open[i].Priority < open[j].Priority
		}
		return open[i].Created.Before(open[j].Created)
	})
	const topN = 5
	if len(open) > topN {
		open = open[:topN]
	}
	s.OpenTop = open
	return s
}

// attachSummary joins a project's sessions with rows from summaries.db and
// rolls per-session metrics into project-level aggregates. No-op when the
// summary DB isn't available.
func attachSummary(p *Project, d *SummaryData) {
	if d == nil || !d.Available {
		return
	}
	// Per-session lookup.
	for i := range p.Sessions {
		s := &p.Sessions[i]
		if m, ok := d.BySession[sessionKey(s.Agent, s.SessionID)]; ok {
			s.Summary = m
			p.TurnCount += m.TurnCount
			p.ToolCallCount += m.ToolCallCount
			p.ErrorCount += m.ErrorCount
		}
	}
	// Top tools across the project's slugs (root + worktrees) merged into
	// one ranked list.
	merged := map[string]*ToolStat{}
	for _, slug := range p.Slugs {
		for _, ts := range d.ToolStats[slug] {
			cur, ok := merged[ts.Kind]
			if !ok {
				cp := ts
				merged[ts.Kind] = &cp
				continue
			}
			// Weighted-average duration by call count, summed counts.
			totalCalls := cur.Calls + ts.Calls
			if totalCalls > 0 {
				cur.AvgMs = (cur.AvgMs*int64(cur.Calls) +
					ts.AvgMs*int64(ts.Calls)) / int64(totalCalls)
			}
			cur.Calls = totalCalls
			cur.Errors += ts.Errors
		}
		p.Compactions += d.CompactionsByProject[slug]
	}
	for _, ts := range merged {
		p.TopTools = append(p.TopTools, *ts)
	}
	sort.Slice(p.TopTools, func(i, j int) bool {
		return p.TopTools[i].Calls > p.TopTools[j].Calls
	})
}

func contains(ss []string, v string) bool {
	for _, s := range ss {
		if s == v {
			return true
		}
	}
	return false
}

// readShipCursor reads transport/cursors/ship/<agent>/<sid>.cursor. The file
// is a plain decimal byte offset; missing file == 0 (nothing shipped yet).
// Kept private to the TUI so we don't have to expose the transport/cursor
// package outside its own subtree.
func readShipCursor(agent, sid string) (int64, error) {
	path := filepath.Join(config.TransportDir(), "cursors", "ship", agent, sid+".cursor")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	return strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
}

func homeDir() (string, error) {
	return os.UserHomeDir()
}
