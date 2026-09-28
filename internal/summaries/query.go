package summaries

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"loom/internal/config"
)

// SessionMetrics is the per-session view of summaries.db rows that
// dashboards and detail pages surface. Mirrors the shape the TUI
// consumed before the read+write layer was unified into this package.
type SessionMetrics struct {
	Agent           string
	SessionID       string
	Project         string // legacy slug
	Cwd             string // parsed from JSONL
	CwdRaw          string // sidecar-captured raw cwd
	GitRemote       string // sidecar-captured git remote
	Model           string
	TurnCount       int
	ToolCallCount   int
	ErrorCount      int
	Compacted       bool
	InputTokens     int64
	OutputTokens    int64
	CacheReadTokens int64
	UsageKnown      bool
	DurationMs      int64
}

// ProjectMetrics rolls per-session counters up to the legacy slug
// dimension. Identity-based grouping is the caller's job.
type ProjectMetrics struct {
	TurnCount     int
	ToolCallCount int
	ErrorCount    int
	SessionCount  int
}

// ToolStat is one (project, tool_kind) aggregate.
type ToolStat struct {
	Kind       string
	Calls      int
	Errors     int
	AvgMs      int64
	TimedCalls int
	TotalMs    int64
}

// View is the bundle a dashboard refresh consumes in one shot. Indexed
// by (agent, sessionID) for fast joins; project-level rollups are
// pre-aggregated by slug for the dashboard's ACTIVITY column.
type View struct {
	BySession            map[string]*SessionMetrics
	ByProject            map[string]*ProjectMetrics
	ToolStats            map[string][]ToolStat
	CompactionsByProject map[string]int
	// Available is false when summaries.db doesn't exist yet (e.g.
	// summarizer not installed); every other field is nil/empty in
	// that case so callers can degrade silently.
	Available bool
}

// SessionKey is the canonical "<agent>\x00<session_id>" lookup key.
func SessionKey(agent, sessionID string) string {
	return agent + "\x00" + sessionID
}

// Load opens the summary DB read-only and materializes a View. Returns
// View{Available: false} when the DB doesn't exist yet so the caller
// (typically the TUI) can render its pre-summary fields.
func Load() (*View, error) {
	dbPath := filepath.Join(config.Home(), "summaries.db")
	if _, err := os.Stat(dbPath); err != nil {
		return &View{Available: false}, nil
	}

	// mode=ro keeps us out of the writer's way; the launchd summarizer
	// holds the only write connection.
	dsn := fmt.Sprintf("file:%s?mode=ro&_pragma=busy_timeout(2000)", dbPath)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open summaries.db: %w", err)
	}
	defer db.Close()

	v := &View{
		BySession:            map[string]*SessionMetrics{},
		ByProject:            map[string]*ProjectMetrics{},
		ToolStats:            map[string][]ToolStat{},
		CompactionsByProject: map[string]int{},
		Available:            true,
	}

	if err := loadSessions(db, v); err != nil {
		return nil, err
	}
	if err := loadToolStats(db, v); err != nil {
		return nil, err
	}
	if err := loadCompactions(db, v); err != nil {
		return nil, err
	}
	return v, nil
}

func loadSessions(db *sql.DB, v *View) error {
	rows, err := db.Query(`
		SELECT session_id, agent, project, cwd, cwd_raw, git_remote, model,
		       turn_count, tool_call_count, error_count, compacted,
		       input_tokens, output_tokens, cache_read_tokens, usage_known, duration_ms
		FROM sessions
	`)
	if err != nil {
		return fmt.Errorf("query sessions: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			m                    SessionMetrics
			project              sql.NullString
			cwd                  sql.NullString
			cwdRaw               sql.NullString
			gitRemote            sql.NullString
			model                sql.NullString
			compact              sql.NullInt64
			usageKnown           sql.NullBool
			input, output, cache sql.NullInt64
		)
		if err := rows.Scan(
			&m.SessionID, &m.Agent, &project, &cwd, &cwdRaw, &gitRemote, &model,
			&m.TurnCount, &m.ToolCallCount, &m.ErrorCount, &compact,
			&input, &output, &cache,
			&usageKnown,
			&m.DurationMs,
		); err != nil {
			return err
		}
		if project.Valid {
			m.Project = project.String
		}
		if cwd.Valid {
			m.Cwd = cwd.String
		}
		if cwdRaw.Valid {
			m.CwdRaw = cwdRaw.String
		}
		if gitRemote.Valid {
			m.GitRemote = gitRemote.String
		}
		if model.Valid {
			m.Model = model.String
		}
		m.Compacted = compact.Valid && compact.Int64 != 0
		m.UsageKnown = usageKnown.Valid && usageKnown.Bool
		m.InputTokens, m.OutputTokens, m.CacheReadTokens = input.Int64, output.Int64, cache.Int64

		v.BySession[SessionKey(m.Agent, m.SessionID)] = &m

		if project.Valid {
			pm, ok := v.ByProject[project.String]
			if !ok {
				pm = &ProjectMetrics{}
				v.ByProject[project.String] = pm
			}
			pm.SessionCount++
			pm.TurnCount += m.TurnCount
			pm.ToolCallCount += m.ToolCallCount
			pm.ErrorCount += m.ErrorCount
		}
	}
	return rows.Err()
}

func loadToolStats(db *sql.DB, v *View) error {
	rows, err := db.Query(`
		SELECT s.project,
		       COALESCE(tc.tool_kind, 'other') AS kind,
		       COUNT(*) AS calls,
		       SUM(tc.is_error) AS errors,
		       COUNT(CASE WHEN tc.duration_ms >= 0 THEN tc.duration_ms END),
		       SUM(CASE WHEN tc.duration_ms >= 0 THEN tc.duration_ms END)
		FROM tool_calls tc
		JOIN sessions s ON s.agent = tc.agent AND s.session_id = tc.session_id
		WHERE s.project IS NOT NULL
		GROUP BY s.project, kind
		ORDER BY s.project, calls DESC
	`)
	if err != nil {
		return fmt.Errorf("query tool_stats: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			project  sql.NullString
			ts       ToolStat
			errs     sql.NullInt64
			duration sql.NullInt64
		)
		if err := rows.Scan(&project, &ts.Kind, &ts.Calls, &errs, &ts.TimedCalls, &duration); err != nil {
			return err
		}
		if !project.Valid {
			continue
		}
		if errs.Valid {
			ts.Errors = int(errs.Int64)
		}
		ts.TotalMs = duration.Int64
		if ts.TimedCalls > 0 {
			ts.AvgMs = (ts.TotalMs + int64(ts.TimedCalls)/2) / int64(ts.TimedCalls)
		}
		v.ToolStats[project.String] = append(v.ToolStats[project.String], ts)
	}
	return rows.Err()
}

func loadCompactions(db *sql.DB, v *View) error {
	rows, err := db.Query(`
		SELECT s.project, COUNT(*) AS n
		FROM compactions c
		JOIN sessions s ON s.agent = c.agent AND s.session_id = c.session_id
		WHERE s.project IS NOT NULL
		GROUP BY s.project
	`)
	if err != nil {
		return fmt.Errorf("query compactions: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var project sql.NullString
		var n int
		if err := rows.Scan(&project, &n); err != nil {
			return err
		}
		if project.Valid {
			v.CompactionsByProject[project.String] = n
		}
	}
	return rows.Err()
}

// SessionSource is one summarized session's identity plus the raw artifact
// the summarizer folded. Consumed by the knowledge extraction trigger, which
// re-reads the artifact rather than the derived rows.
type SessionSource struct {
	Agent      string
	SessionID  string
	SourcePath string
	GitRemote  string
	CwdRaw     string // sidecar-captured raw cwd, the checkout the session ran in
	// TurnCount is how many turns the summarizer folded, valid only when
	// TurnCountKnown: the column is nullable, and a row that carries no count
	// must not read as a zero-turn session.
	TurnCount      int
	TurnCountKnown bool
}

// LoadSessionSources returns every summarized session that still records its
// source artifact and was summarized at or after since, newest-summarized
// first. A zero since is unbounded. Returns nil when summaries.db doesn't
// exist yet (the summarizer may not be installed), mirroring Load's
// degrade-silently contract.
func LoadSessionSources(since time.Time) ([]SessionSource, error) {
	dbPath := filepath.Join(config.Home(), "summaries.db")
	if _, err := os.Stat(dbPath); err != nil {
		return nil, nil
	}

	dsn := fmt.Sprintf("file:%s?mode=ro&_pragma=busy_timeout(2000)", dbPath)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open summaries.db: %w", err)
	}
	defer db.Close()

	rows, err := db.Query(`
		SELECT agent, session_id, source_path, git_remote, cwd_raw, turn_count
		FROM sessions
		WHERE source_path IS NOT NULL AND source_path != ''
		  AND summarized_at >= ?
		ORDER BY summarized_at DESC
	`, since.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil, fmt.Errorf("query session sources: %w", err)
	}
	defer rows.Close()

	var out []SessionSource
	for rows.Next() {
		var (
			s          SessionSource
			sourcePath sql.NullString
			gitRemote  sql.NullString
			cwdRaw     sql.NullString
			turnCount  sql.NullInt64
		)
		if err := rows.Scan(&s.Agent, &s.SessionID, &sourcePath, &gitRemote, &cwdRaw, &turnCount); err != nil {
			return nil, err
		}
		s.SourcePath = sourcePath.String
		s.GitRemote = gitRemote.String
		s.CwdRaw = cwdRaw.String
		s.TurnCount = int(turnCount.Int64)
		s.TurnCountKnown = turnCount.Valid
		out = append(out, s)
	}
	return out, rows.Err()
}

// LoadSessionsForTicket returns every summarized session that landed a commit
// for ticketID, deduped to one entry per session and ordered by that session's
// earliest such commit. Returns nil when summaries.db doesn't exist yet,
// mirroring LoadSessionSources' degrade-silently contract — but a DB predating
// the commits table is an error rather than an empty answer, because "no
// sessions" from a DB that cannot hold the answer reads exactly like a ticket
// that landed no commits, and the caller acts on that reading.
func LoadSessionsForTicket(ticketID string) ([]SessionSource, error) {
	return LoadSessionsForTickets([]string{ticketID})
}

// LoadSessionsForTickets is LoadSessionsForTicket over a set of ids — an epic
// and its children, resolved by the caller through tk — with each
// (agent, session_id) selected once however many of the ids its commits carry.
// One session commonly lands commits for several children of one epic, and an
// extraction per child would spend the same transcript again for the same
// candidates. The order is by the session's earliest commit for any of the ids,
// so it is the order the work happened in across the whole set.
func LoadSessionsForTickets(ticketIDs []string) ([]SessionSource, error) {
	dbPath := filepath.Join(config.Home(), "summaries.db")
	if _, err := os.Stat(dbPath); err != nil {
		return nil, nil
	}

	dsn := fmt.Sprintf("file:%s?mode=ro&_pragma=busy_timeout(2000)", dbPath)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open summaries.db: %w", err)
	}
	defer db.Close()

	if v := schemaVersionOf(db); v < commitsSchemaVersion {
		return nil, fmt.Errorf("summaries.db is at schema %d and predates the commits table (want %d) — run `loom summarize --rebuild`", v, commitsSchemaVersion)
	}

	rows, err := db.Query(`
		SELECT c.agent, c.session_id, c.subject, c.committed_at,
		       s.source_path, s.git_remote, s.cwd_raw
		FROM commits c
		JOIN sessions s ON s.agent = c.agent AND s.session_id = c.session_id
		WHERE s.source_path IS NOT NULL AND s.source_path != ''
	`)
	if err != nil {
		return nil, fmt.Errorf("query ticket sessions: %w", err)
	}
	defer rows.Close()

	// The commit subject's `[<id>]` marker is matched here rather than by a SQL
	// LIKE: a tk ticket id may legally contain `_`, which LIKE reads as a
	// single-character wildcard, and an ESCAPE clause around it is more fragile
	// than an exact comparison.
	wanted := make(map[string]bool, len(ticketIDs))
	for _, id := range ticketIDs {
		wanted[id] = true
	}

	type hit struct {
		src   SessionSource
		first time.Time
	}
	idx := map[string]int{}
	var hits []hit

	for rows.Next() {
		var (
			s                             SessionSource
			subject, committedAt          sql.NullString
			sourcePath, gitRemote, cwdRaw sql.NullString
		)
		if err := rows.Scan(&s.Agent, &s.SessionID, &subject, &committedAt,
			&sourcePath, &gitRemote, &cwdRaw); err != nil {
			return nil, err
		}
		if id, ok := MarkerTicketID(subject.String); !ok || !wanted[id] {
			continue
		}
		var at time.Time
		if committedAt.Valid {
			at, _ = time.Parse(time.RFC3339Nano, committedAt.String)
		}
		// One session commonly lands several commits for the same ticket; keep
		// the earliest, so the order below is the order the work happened in.
		if i, ok := idx[SessionKey(s.Agent, s.SessionID)]; ok {
			if !at.IsZero() && (hits[i].first.IsZero() || at.Before(hits[i].first)) {
				hits[i].first = at
			}
			continue
		}
		s.SourcePath = sourcePath.String
		s.GitRemote = gitRemote.String
		s.CwdRaw = cwdRaw.String
		idx[SessionKey(s.Agent, s.SessionID)] = len(hits)
		hits = append(hits, hit{src: s, first: at})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// committed_at can be NULL or unparseable, so the session id breaks the tie:
	// the order matters less than it being the same on every run.
	sort.Slice(hits, func(i, j int) bool {
		if !hits[i].first.Equal(hits[j].first) {
			return hits[i].first.Before(hits[j].first)
		}
		return hits[i].src.SessionID < hits[j].src.SessionID
	})

	var out []SessionSource
	for _, h := range hits {
		out = append(out, h.src)
	}
	return out, nil
}

// MarkerTicketID returns the ticket id named by a commit subject's `[<id>]`
// marker. Two conventions are in use, so both are read: a marker that opens
// the subject (`[<id>] Do the thing`) and one that ends it (`Do the thing
// [<id>]`). A bracket in the middle of a subject is not a marker — no commit
// convention puts one there, and text like `Fix [x] parsing` would false-match.
// A tk id never contains `]`, so the first one closes an opening marker. A
// marker in either position must be non-empty and hold no whitespace or `]`,
// since a tk id never does and tags like `[skip ci]` sit in both positions; an
// opening marker that fails this leaves the trailing one to be read.
func MarkerTicketID(subject string) (string, bool) {
	if strings.HasPrefix(subject, "[") {
		if end := strings.IndexByte(subject, ']'); end > 0 {
			if id := subject[1:end]; id != "" && !strings.ContainsAny(id, " \t") {
				return id, true
			}
		}
	}
	if !strings.HasSuffix(subject, "]") {
		return "", false
	}
	start := strings.LastIndexByte(subject, '[')
	if start < 0 {
		return "", false
	}
	id := subject[start+1 : len(subject)-1]
	if id == "" || strings.ContainsAny(id, " \t]") {
		return "", false
	}
	return id, true
}

// SessionSpan is one summarized session: where it ran and when. Start and End
// are zero when the row carries no parseable time.
type SessionSpan struct {
	Agent     string
	SessionID string
	CwdRaw    string // sidecar-captured raw cwd
	Cwd       string // parsed from JSONL
	Start     time.Time
	End       time.Time
}

// SessionCommit is one commit a session landed. TicketID is the id its
// subject's `[<id>]` marker names, "" for an unmarked commit. CommittedAt is
// zero when the row carries no parseable time.
type SessionCommit struct {
	Agent       string
	SessionID   string
	Hash        string
	Branch      string
	Subject     string
	CommittedAt time.Time
	TicketID    string
}

// LoadSessionsAndCommits returns every summarized session and every commit
// those sessions landed. Unlike LoadSessionsForTickets, a missing summaries.db
// is an error as well as one predating the commits table: this read's empty
// answer is "no session worked on anything", and a DB that is absent or
// cannot hold commits would read exactly like that.
func LoadSessionsAndCommits() ([]SessionSpan, []SessionCommit, error) {
	dbPath := filepath.Join(config.Home(), "summaries.db")
	if _, err := os.Stat(dbPath); err != nil {
		return nil, nil, fmt.Errorf("summaries.db: %w — run `loom summarize`", err)
	}

	dsn := fmt.Sprintf("file:%s?mode=ro&_pragma=busy_timeout(2000)", dbPath)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, nil, fmt.Errorf("open summaries.db: %w", err)
	}
	defer db.Close()

	if v := schemaVersionOf(db); v < commitsSchemaVersion {
		return nil, nil, fmt.Errorf("summaries.db is at schema %d and predates the commits table (want %d) — run `loom summarize --rebuild`", v, commitsSchemaVersion)
	}

	rows, err := db.Query(`
		SELECT agent, session_id, cwd_raw, cwd, start_time, end_time
		FROM sessions
	`)
	if err != nil {
		return nil, nil, fmt.Errorf("query sessions: %w", err)
	}
	defer rows.Close()

	var spans []SessionSpan
	for rows.Next() {
		var (
			s           SessionSpan
			cwdRaw, cwd sql.NullString
			start, end  sql.NullString
		)
		if err := rows.Scan(&s.Agent, &s.SessionID, &cwdRaw, &cwd, &start, &end); err != nil {
			return nil, nil, err
		}
		s.CwdRaw, s.Cwd = cwdRaw.String, cwd.String
		s.Start, _ = time.Parse(time.RFC3339Nano, start.String)
		s.End, _ = time.Parse(time.RFC3339Nano, end.String)
		spans = append(spans, s)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	crows, err := db.Query(`
		SELECT agent, session_id, commit_hash, branch, subject, committed_at
		FROM commits
		ORDER BY agent, session_id, seq
	`)
	if err != nil {
		return nil, nil, fmt.Errorf("query commits: %w", err)
	}
	defer crows.Close()

	var commits []SessionCommit
	for crows.Next() {
		var (
			c                            SessionCommit
			branch, subject, committedAt sql.NullString
		)
		if err := crows.Scan(&c.Agent, &c.SessionID, &c.Hash, &branch, &subject, &committedAt); err != nil {
			return nil, nil, err
		}
		c.Branch, c.Subject = branch.String, subject.String
		c.CommittedAt, _ = time.Parse(time.RFC3339Nano, committedAt.String)
		c.TicketID, _ = MarkerTicketID(c.Subject)
		commits = append(commits, c)
	}
	return spans, commits, crows.Err()
}

// ActivityView is the rolling-window rollup the "loom ui" activity screen
// renders: repos touched, sessions started, and commits landed since Since.
// Available is false when summaries.db is missing; Outdated is true when the
// DB predates the commits table (schema < 4) and needs a --rebuild.
type ActivityView struct {
	Available bool
	Outdated  bool
	Since     time.Time
	Repos     []RepoActivity
	Sessions  []SessionActivity
	Commits   []CommitActivity
}

// RepoActivity is one repo's per-window session and commit counts. Repo is the
// raw identity handle (git remote, else cwd, else slug); display shortening is
// the caller's job.
type RepoActivity struct {
	Repo     string
	Sessions int
	Commits  int
}

// SessionActivity is one session that started inside the window.
type SessionActivity struct {
	Agent  string
	Repo   string
	Start  time.Time
	Turns  int
	Tools  int
	Errors int
}

// CommitActivity is one commit that landed inside the window, newest-first.
type CommitActivity struct {
	CommittedAt time.Time
	Repo        string
	Hash        string
	Subject     string
}

// LoadActivity opens the summary DB read-only and materializes the rolling
// window [now-window, now). Returns ActivityView{Available: false} when the DB
// is missing and {Outdated: true} when it predates the commits table, so the
// caller can degrade without panicking — mirrors Load().
func LoadActivity(window time.Duration) (*ActivityView, error) {
	dbPath := filepath.Join(config.Home(), "summaries.db")
	if _, err := os.Stat(dbPath); err != nil {
		return &ActivityView{Available: false}, nil
	}

	dsn := fmt.Sprintf("file:%s?mode=ro&_pragma=busy_timeout(2000)", dbPath)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open summaries.db: %w", err)
	}
	defer db.Close()

	av := &ActivityView{Since: time.Now().Add(-window)}

	// The commits table arrived in schema v4. A v3 DB can't answer this view;
	// flag it so the screen prompts a rebuild instead of erroring.
	if schemaVersionOf(db) < schemaVersion {
		av.Outdated = true
		return av, nil
	}
	av.Available = true

	cutoff := av.Since.UTC().Format(time.RFC3339Nano)
	if err := loadActivitySessions(db, av, cutoff); err != nil {
		return nil, err
	}
	if err := loadActivityCommits(db, av, cutoff); err != nil {
		return nil, err
	}
	av.buildRepos()
	return av, nil
}

func schemaVersionOf(db *sql.DB) int {
	var v sql.NullString
	if err := db.QueryRow(`SELECT value FROM schema_meta WHERE key = 'schema_version'`).Scan(&v); err != nil {
		return 0
	}
	if !v.Valid {
		return 0
	}
	n, err := strconv.Atoi(v.String)
	if err != nil {
		return 0
	}
	return n
}

func loadActivitySessions(db *sql.DB, av *ActivityView, cutoff string) error {
	rows, err := db.Query(`
		SELECT agent, git_remote, cwd_raw, cwd, project, start_time,
		       turn_count, tool_call_count, error_count
		FROM sessions
		WHERE start_time IS NOT NULL AND start_time >= ?
		ORDER BY start_time DESC
	`, cutoff)
	if err != nil {
		return fmt.Errorf("query activity sessions: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			sa                              SessionActivity
			gitRemote, cwdRaw, cwd, project sql.NullString
			start                           sql.NullString
		)
		if err := rows.Scan(&sa.Agent, &gitRemote, &cwdRaw, &cwd, &project,
			&start, &sa.Turns, &sa.Tools, &sa.Errors); err != nil {
			return err
		}
		sa.Repo = firstNonEmpty(gitRemote, cwdRaw, cwd, project)
		if start.Valid {
			sa.Start, _ = time.Parse(time.RFC3339Nano, start.String)
		}
		av.Sessions = append(av.Sessions, sa)
	}
	return rows.Err()
}

func loadActivityCommits(db *sql.DB, av *ActivityView, cutoff string) error {
	rows, err := db.Query(`
		SELECT committed_at, git_remote, cwd, commit_hash, subject
		FROM commits
		WHERE committed_at IS NOT NULL AND committed_at >= ?
		ORDER BY committed_at DESC
	`, cutoff)
	if err != nil {
		return fmt.Errorf("query activity commits: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			ca             CommitActivity
			committedAt    sql.NullString
			gitRemote, cwd sql.NullString
			hash, subject  sql.NullString
		)
		if err := rows.Scan(&committedAt, &gitRemote, &cwd, &hash, &subject); err != nil {
			return err
		}
		if committedAt.Valid {
			ca.CommittedAt, _ = time.Parse(time.RFC3339Nano, committedAt.String)
		}
		ca.Repo = firstNonEmpty(gitRemote, cwd)
		ca.Hash = hash.String
		ca.Subject = subject.String
		av.Commits = append(av.Commits, ca)
	}
	return rows.Err()
}

// buildRepos rolls the in-window sessions and commits up to one row per repo,
// ordered by commits then sessions then name so the busiest repo leads.
func (av *ActivityView) buildRepos() {
	idx := map[string]*RepoActivity{}
	get := func(repo string) *RepoActivity {
		if repo == "" {
			repo = "unknown"
		}
		r, ok := idx[repo]
		if !ok {
			r = &RepoActivity{Repo: repo}
			idx[repo] = r
		}
		return r
	}
	for _, s := range av.Sessions {
		get(s.Repo).Sessions++
	}
	for _, c := range av.Commits {
		get(c.Repo).Commits++
	}
	for _, r := range idx {
		av.Repos = append(av.Repos, *r)
	}
	sort.Slice(av.Repos, func(i, j int) bool {
		if av.Repos[i].Commits != av.Repos[j].Commits {
			return av.Repos[i].Commits > av.Repos[j].Commits
		}
		if av.Repos[i].Sessions != av.Repos[j].Sessions {
			return av.Repos[i].Sessions > av.Repos[j].Sessions
		}
		return av.Repos[i].Repo < av.Repos[j].Repo
	})
}

// firstNonEmpty returns the first valid, non-blank value, mirroring the
// dashboard's GitRemote > CwdRaw > Cwd > slug identity precedence.
func firstNonEmpty(vals ...sql.NullString) string {
	for _, v := range vals {
		if v.Valid && v.String != "" {
			return v.String
		}
	}
	return ""
}
