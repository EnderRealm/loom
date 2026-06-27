package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"loom/internal/config"
	"loom/internal/summaries"
	"loom/transport/cursor"

	"github.com/EnderRealm/ticket/v7/pkg/ticket"
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
	TopTools      []summaries.ToolStat
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
	Summary *summaries.SessionMetrics
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
// authoritative identity (git remote, then raw cwd, then slug-derived
// key as the legacy fallback), sorted by most-recent activity.
//
// Identity precedence is what makes cross-machine grouping correct:
// `steve/code/loom` and `smacbeth/code/loom` shipped from different
// laptops resolve to the same git remote and roll up into one project.
// Pre-identity sessions (legacy capture, no meta sidecar, no DB row
// yet) fall through to a slug-derived key — splitWorktree collapses
// Forge worktrees back into the parent slug, and a basename-heuristic
// (projectKeyForSlug) handles cross-machine slug variants until those
// sessions are re-shipped under the new wire shape.
func LoadProjects() ([]Project, error) {
	home := config.Home()
	received := filepath.Join(home, "received")
	staging := filepath.Join(home, "transport", "staging")

	// Pull summary metrics first so their identity columns drive the
	// grouping decision, not the slug we're about to walk past.
	summary, _ := summaries.Load()

	// Pre-walk slugs so the legacy basename heuristic can match
	// suffixes against locally-resolved paths. Only consulted when a
	// session has no identity; identity-bearing sessions skip it.
	rootSlugs := map[string]bool{}
	collectSlugs := func(root string) {
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
				r, _ := splitDisplayWorktree(s.Name())
				rootSlugs[r] = true
			}
		}
	}
	collectSlugs(received)
	collectSlugs(staging)

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

	// resolveIdentity returns the authoritative (key, gitRemote, cwd,
	// displayName, localPath) for a session, drawing from summaries.db
	// (preferred) and the on-disk meta sidecar (fallback for sessions
	// the summarizer hasn't folded yet). When neither carries identity
	// it falls back to the legacy slug-basename heuristic so worktrees
	// and cross-machine variants of the same repo still collapse.
	resolveIdentity := func(agent, slug, sid, jsonlPath string) (key, gitRemote, cwd, name, localPath string) {
		if summary != nil {
			if m, ok := summary.BySession[summaries.SessionKey(agent, sid)]; ok {
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
			g, c := readSidecarIdentity(jsonlPath)
			if gitRemote == "" {
				gitRemote = g
			}
			if cwd == "" {
				cwd = c
			}
		}
		switch {
		case gitRemote != "":
			key = "remote:" + normalizeRemote(gitRemote)
		case cwd != "":
			key = "cwd:" + cwd
		default:
			// Legacy path: collapse worktrees back into the parent
			// slug, then derive a basename key so cross-machine variants
			// of the same repo still group. Same algorithm the deleted
			// projectKeyFor used; lives here as a fallback only.
			rootSlug, _ := splitDisplayWorktree(slug)
			key = "slug:" + projectKeyForSlug(rootSlug, pathByRoot[rootSlug], rootSlugs, knownBases)
			if path := pathByRoot[rootSlug]; path != "" && localPath == "" {
				localPath = path
			}
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
		ship, _ := cursor.Read(cursor.KindShip, agent, sid)
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

// splitDisplayWorktree separates a slug into root + worktree label.
// Forge stores worktrees at <project>/.claude/worktrees/<branch>, which
// sanitizes into "<root>--claude-worktrees-<branch>". Used for display
// labels and as the legacy slug-fallback's pre-collapse step so a
// worktree groups with its parent project even without identity.
func splitDisplayWorktree(slug string) (root, worktree string) {
	const marker = "--claude-worktrees-"
	if i := strings.Index(slug, marker); i > 0 {
		return slug[:i], slug[i+len(marker):]
	}
	return slug, ""
}

// projectKeyForSlug is the legacy basename heuristic used only when a
// session carries no identity (no sidecar, no summary row yet). It
// collapses cross-machine slug variants of the same repo by matching
// trailing path components against basenames learned from
// locally-resolved sibling slugs. Identity-bearing sessions never reach
// this function; once every session has been re-shipped under the new
// wire shape it can be deleted along with reconstructPath.
func projectKeyForSlug(rootSlug, resolvedPath string, allSlugs, knownBases map[string]bool) string {
	if resolvedPath != "" {
		return strings.ToLower(filepath.Base(resolvedPath))
	}
	body := strings.TrimPrefix(rootSlug, "-")
	parts := strings.Split(body, "-")

	// Suffix match against known basenames: e.g. a slug from another
	// machine like "Users-smacbeth-code-forge-data" lines up with
	// "forge-data" learned from a locally-resolved slug.
	for baseLen := len(parts); baseLen >= 1; baseLen-- {
		cand := strings.ToLower(strings.Join(parts[len(parts)-baseLen:], "-"))
		if knownBases[cand] {
			return cand
		}
	}

	// Longest parent-prefix removal: prefix qualifies if it's itself a
	// known root slug (someone ran an agent in that container) or if it
	// prefixes ≥2 other slugs (shared container directory).
	for prefixLen := len(parts) - 1; prefixLen >= 1; prefixLen-- {
		prefix := "-" + strings.Join(parts[:prefixLen], "-")
		if isParentPrefix(prefix, rootSlug, allSlugs) {
			return strings.ToLower(strings.Join(parts[prefixLen:], "-"))
		}
	}

	if len(parts) > 0 {
		return strings.ToLower(parts[len(parts)-1])
	}
	return rootSlug
}

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

// reconstructPath inverts the agent-specific slug sanitization. Claude
// replaces '/' with '-'; Codex additionally replaces '.' with '_'. Both
// are lossy: from the slug alone we can't tell whether a '-' was a path
// separator or part of a directory name (e.g. "forge-data"). We try
// every possible split and stop at the first interpretation that
// resolves to a real directory; "" means "this slug came from a
// machine other than this one." Used only by the legacy fallback path.
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
		if strings.Contains(path, "_") {
			alt := strings.ReplaceAll(path, "_", ".")
			if fi, err := os.Stat(alt); err == nil && fi.IsDir() {
				return alt
			}
		}
	}
	return ""
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

// TicketActivity is the rolling-window rollup of tickets created and closed
// across every project in the central tk store, newest-first. Available is
// false when the store couldn't be resolved, so the screen can distinguish a
// genuinely quiet window from a missing store.
type TicketActivity struct {
	Since     time.Time
	Available bool
	Created   []TicketChange
	Closed    []TicketChange
}

// TicketChange is one created or closed ticket. When is the relevant
// timestamp: Created for the created list, Completed for the closed list.
type TicketChange struct {
	ID      string
	Title   string
	Project string
	Status  string
	When    time.Time
}

// LoadTicketActivity reads every ticket across all projects in the central tk
// store and buckets them by Created/Completed within [now-window, now).
// Completed is tk's authoritative close timestamp (zero for non-terminal
// tickets). Resilient: an unresolvable store yields an empty rollup rather
// than failing the whole screen.
func LoadTicketActivity(window time.Duration) TicketActivity {
	since := time.Now().Add(-window)
	centralRoot, err := config.CentralStoreRoot()
	if err != nil {
		return TicketActivity{Since: since}
	}
	tickets, err := ticket.NewMultiStore(filepath.Join(centralRoot, "tickets")).List()
	if err != nil {
		return TicketActivity{Since: since}
	}
	return bucketTicketActivity(tickets, since)
}

// bucketTicketActivity splits a ticket list into the created/closed changes
// that fall within [since, now), newest-first. Pure over its inputs so the
// window/zero-value logic is testable without a real central store.
func bucketTicketActivity(tickets []*ticket.Ticket, since time.Time) TicketActivity {
	ta := TicketActivity{Since: since, Available: true}
	for _, t := range tickets {
		project, _ := ticket.ParseNamespacedID(t.ID)
		if !t.Created.IsZero() && !t.Created.Before(since) {
			ta.Created = append(ta.Created, TicketChange{
				ID: t.ID, Title: t.Title, Project: project,
				Status: string(t.Status), When: t.Created,
			})
		}
		if !t.Completed.IsZero() && !t.Completed.Before(since) {
			ta.Closed = append(ta.Closed, TicketChange{
				ID: t.ID, Title: t.Title, Project: project,
				Status: string(t.Status), When: t.Completed,
			})
		}
	}
	sort.Slice(ta.Created, func(i, j int) bool {
		return ta.Created[i].When.After(ta.Created[j].When)
	})
	sort.Slice(ta.Closed, func(i, j int) bool {
		return ta.Closed[i].When.After(ta.Closed[j].When)
	})
	return ta
}

// attachSummary joins a project's sessions with rows from summaries.db and
// rolls per-session metrics into project-level aggregates. No-op when the
// summary DB isn't available.
func attachSummary(p *Project, d *summaries.View) {
	if d == nil || !d.Available {
		return
	}
	// Per-session lookup.
	for i := range p.Sessions {
		s := &p.Sessions[i]
		if m, ok := d.BySession[summaries.SessionKey(s.Agent, s.SessionID)]; ok {
			s.Summary = m
			p.TurnCount += m.TurnCount
			p.ToolCallCount += m.ToolCallCount
			p.ErrorCount += m.ErrorCount
		}
	}
	// Top tools across the project's slugs (root + worktrees) merged into
	// one ranked list.
	merged := map[string]*summaries.ToolStat{}
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

func homeDir() (string, error) {
	return os.UserHomeDir()
}
