package tui

import (
	"bufio"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"loom/internal/config"

	"gopkg.in/yaml.v3"
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
}

// TicketSummary is the rollup for a project's .tickets directory.
type TicketSummary struct {
	Dir    string
	Total  int
	Status map[string]int
	Type   map[string]int
}

// LoadProjects walks the loom state tree and returns one Project per
// root-slug, sorted by most-recent activity. Worktree slugs roll up under
// their parent project.
func LoadProjects() ([]Project, error) {
	home := config.Home()
	received := filepath.Join(home, "received")
	staging := filepath.Join(home, "transport", "staging")

	projects := map[string]*Project{}
	// rootSlug -> (agent + "\x00" + sessionID) -> session index.
	sessionIdx := map[string]map[string]int{}

	upsert := func(slug string) *Project {
		root, worktree := splitWorktree(slug)
		p, ok := projects[root]
		if !ok {
			p = &Project{Slug: root, Path: reconstructPath(root)}
			p.Name = projectName(p.Path, root)
			projects[root] = p
			sessionIdx[root] = map[string]int{}
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
		k := agent + "\x00" + sid
		if i, ok := sessionIdx[p.Slug][k]; ok {
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
		sessionIdx[p.Slug][k] = i
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

	out := make([]Project, 0, len(projects))
	for _, p := range projects {
		sort.Slice(p.Sessions, func(i, j int) bool {
			return p.Sessions[i].Modified.After(p.Sessions[j].Modified)
		})
		sort.Strings(p.Agents)
		sort.Strings(p.Worktrees)
		sort.Strings(p.Slugs)
		p.Tickets = loadTicketSummary(p.Path)
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
// path (title-cased), falls back to the last non-empty slug segment.
func projectName(path, slug string) string {
	if path != "" {
		if base := filepath.Base(path); base != "" && base != "/" && base != "." {
			return titleCase(base)
		}
	}
	segs := strings.Split(strings.Trim(slug, "-"), "-")
	for i := len(segs) - 1; i >= 0; i-- {
		if segs[i] != "" {
			return titleCase(segs[i])
		}
	}
	return slug
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

// reconstructPath inverts the agent-specific slug sanitization enough to
// find a .tickets directory. Claude replaces '/' with '-'. Codex also
// replaces '.' with '_'. Both are lossy; we only surface tickets when the
// best-effort reconstruction points at a real directory.
func reconstructPath(slug string) string {
	candidates := []string{
		filepath.Clean("/" + strings.ReplaceAll(slug, "-", "/")),
		filepath.Clean("/" + strings.ReplaceAll(strings.ReplaceAll(slug, "_", "."), "-", "/")),
	}
	for _, c := range candidates {
		if c == "/" || c == "." {
			continue
		}
		if fi, err := os.Stat(c); err == nil && fi.IsDir() {
			return c
		}
	}
	return ""
}

func loadTicketSummary(projectPath string) *TicketSummary {
	if projectPath == "" {
		return nil
	}
	dir := filepath.Join(projectPath, ".tickets")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	s := &TicketSummary{
		Dir:    dir,
		Status: map[string]int{},
		Type:   map[string]int{},
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		fm, ok := readFrontmatter(filepath.Join(dir, e.Name()))
		if !ok {
			continue
		}
		s.Total++
		if v, ok := fm["status"].(string); ok {
			s.Status[v]++
		}
		if v, ok := fm["type"].(string); ok {
			s.Type[v]++
		}
	}
	if s.Total == 0 {
		return nil
	}
	return s
}

// readFrontmatter parses the YAML frontmatter block delimited by "---\n"
// at the start of the file. Returns (nil, false) if the file has no block.
func readFrontmatter(path string) (map[string]any, bool) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	r := bufio.NewReader(f)
	first, err := r.ReadString('\n')
	if err != nil || strings.TrimSpace(first) != "---" {
		return nil, false
	}
	var buf strings.Builder
	for {
		line, err := r.ReadString('\n')
		if strings.TrimSpace(line) == "---" {
			break
		}
		buf.WriteString(line)
		if err != nil {
			return nil, false
		}
	}
	out := map[string]any{}
	if err := yaml.Unmarshal([]byte(buf.String()), &out); err != nil {
		return nil, false
	}
	return out, true
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
