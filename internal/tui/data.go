package tui

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"loom/internal/config"

	"github.com/EnderRealm/ticket/pkg/ticket"
)

// Project aggregates session and ticket state for one captured cwd. Worktrees
// (e.g. Forge's <project>/.claude/worktrees/<branch>) roll up under their
// parent project, so a single Project can span multiple slugs.
type Project struct {
	Name         string // display name, e.g. "Forge"
	Slug         string // root slug on disk
	Path         string // reconstructed root path, "" if unresolvable
	Slugs        []string
	Worktrees    []string // non-root slugs contributing sessions
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
// root-slug, sorted by most-recent activity. Worktree slugs roll up under
// their parent project.
func LoadProjects() ([]Project, error) {
	home := config.Home()
	received := filepath.Join(home, "received")
	staging := filepath.Join(home, "transport", "staging")

	// Pre-walk: collect every root slug so we can resolve paths once and
	// learn which basenames are real (e.g. "forge-data" vs "data"). The
	// resulting set disambiguates slugs from other machines that don't
	// resolve locally.
	rootSlugs := map[string]bool{}
	collect := func(root string) {
		entries, err := os.ReadDir(root)
		if err != nil {
			return
		}
		for _, a := range entries {
			if !a.IsDir() {
				continue
			}
			slugs, err := os.ReadDir(filepath.Join(root, a.Name()))
			if err != nil {
				continue
			}
			for _, s := range slugs {
				if !s.IsDir() {
					continue
				}
				r, _ := splitWorktree(s.Name())
				rootSlugs[r] = true
			}
		}
	}
	collect(received)
	collect(staging)

	knownBases := map[string]bool{}
	pathByRoot := map[string]string{}
	for r := range rootSlugs {
		p := reconstructPath(r)
		pathByRoot[r] = p
		if p != "" {
			knownBases[strings.ToLower(filepath.Base(p))] = true
		}
	}

	projects := map[string]*Project{}
	// projectKey -> (agent + "\x00" + sessionID) -> session index.
	sessionIdx := map[string]map[string]int{}

	// Group by basename so the same logical project collapses across
	// machines (steve/loom + smacbeth/loom both key on "loom"). False
	// collisions are possible for two unrelated repos with the same
	// basename — the wire-protocol-aware fix tracks that.
	upsert := func(slug string) *Project {
		root, worktree := splitWorktree(slug)
		key := projectKeyFor(root, pathByRoot[root], rootSlugs, knownBases)
		p, ok := projects[key]
		if !ok {
			p = &Project{Slug: root, Path: pathByRoot[root]}
			p.Name = projectName(p.Path, key)
			projects[key] = p
			sessionIdx[key] = map[string]int{}
		} else if p.Path == "" {
			// First slug we saw didn't resolve locally; if a later slug
			// (same basename) does, prefer its path so the ticket lookup
			// and "open in tk" drill-down can work.
			if path := pathByRoot[root]; path != "" {
				p.Path = path
				p.Name = projectName(path, key)
			}
		}
		if !contains(p.Slugs, slug) {
			p.Slugs = append(p.Slugs, slug)
		}
		if worktree != "" && !contains(p.Worktrees, worktree) {
			p.Worktrees = append(p.Worktrees, worktree)
		}
		return p
	}

	addSession := func(p *Project, agent, sid, slug string) *Session {
		key := projectKeyFor(p.Slug, pathByRoot[p.Slug], rootSlugs, knownBases)
		k := agent + "\x00" + sid
		if i, ok := sessionIdx[key][k]; ok {
			return &p.Sessions[i]
		}
		_, worktree := splitWorktree(slug)
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
		return &p.Sessions[i]
	}

	// Received side.
	if err := walkAgentSlugSessions(received, func(agent, slug, sid, path string, info os.FileInfo) {
		p := upsert(slug)
		s := addSession(p, agent, sid, slug)
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

	// Staging side. Attach ship cursor so we can surface pending bytes.
	if err := walkAgentSlugSessions(staging, func(agent, slug, sid, path string, info os.FileInfo) {
		p := upsert(slug)
		s := addSession(p, agent, sid, slug)
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

	// Pull summary metrics from ~/.loom/summaries.db, if it's been
	// populated. Errors here are non-fatal — the dashboard renders the
	// pre-summary fields the same way it always has when summarizer hasn't
	// run yet.
	summary, _ := LoadSummaryData()

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

// projectKeyFor returns the canonical grouping key for a root slug.
// Precedence:
//  1. If the slug resolved to a real local path, use that path's basename.
//  2. Else, longest '-'-aligned suffix matching a basename learned from a
//     locally-resolved slug (handles "smacbeth/code/forge-data" via
//     "steve/code/forge-data").
//  3. Else, longest "parent prefix" P removed from the slug. P qualifies if
//     either it's itself a known root slug (someone ran an agent there —
//     handles "smacbeth/code/moo/moo-rs" via the sibling "smacbeth/code/moo"
//     slug) or P+"-" prefixes ≥2 other slugs (handles "smacbeth/code/X" via
//     the many other slugs sharing the "smacbeth/code" container).
//  4. Else, last '-' segment.
//
// The wire-protocol-aware fix replaces this with the captured git remote URL.
func projectKeyFor(rootSlug, resolvedPath string, allSlugs, knownBases map[string]bool) string {
	if resolvedPath != "" {
		return strings.ToLower(filepath.Base(resolvedPath))
	}
	body := strings.TrimPrefix(rootSlug, "-")
	parts := strings.Split(body, "-")

	// (2) suffix match against known basenames.
	for baseLen := len(parts); baseLen >= 1; baseLen-- {
		cand := strings.ToLower(strings.Join(parts[len(parts)-baseLen:], "-"))
		if knownBases[cand] {
			return cand
		}
	}

	// (3) longest parent-prefix removal.
	for prefixLen := len(parts) - 1; prefixLen >= 1; prefixLen-- {
		prefix := "-" + strings.Join(parts[:prefixLen], "-")
		if isParentPrefix(prefix, rootSlug, allSlugs) {
			return strings.ToLower(strings.Join(parts[prefixLen:], "-"))
		}
	}

	// (4) fallback.
	if len(parts) > 0 {
		return strings.ToLower(parts[len(parts)-1])
	}
	return rootSlug
}

// isParentPrefix reports whether `prefix` looks like a path-segment parent of
// `self`, given the full set of root slugs. Either prefix is itself a known
// slug, or at least two other slugs start with prefix+"-" (so the prefix is
// a shared container directory).
func isParentPrefix(prefix, self string, allSlugs map[string]bool) bool {
	if allSlugs[prefix] && prefix != self {
		return true
	}
	count := 0
	pfx := prefix + "-"
	for s := range allSlugs {
		if s == self {
			continue
		}
		if strings.HasPrefix(s, pfx) {
			count++
			if count >= 2 {
				return true
			}
		}
	}
	return false
}

// splitWorktree separates a slug into its root-project slug and the worktree
// label beneath it. Forge worktrees live at <project>/.claude/worktrees/<branch>,
// which sanitizes to "<root>--claude-worktrees-<branch>". For slugs without that
// marker, worktree is "" and root is the full slug.
func splitWorktree(slug string) (root, worktree string) {
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
