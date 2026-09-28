package escapes

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"strings"

	"loom/internal/summaries"
	"loom/internal/workreport"

	_ "modernc.org/sqlite"
)

// schemaVersion is the summaries.db schema that added the runs table; the
// commits table and the sessions' model and cli_version columns are older.
const schemaVersion = 8

// Session is the loom session that made a commit, with the conditions it ran
// under and the /work runs recorded against it (none before 2026-09-12).
type Session struct {
	Agent      string   `json:"agent"`
	SessionID  string   `json:"session_id"`
	Model      string   `json:"model"`
	CLIVersion string   `json:"cli_version"`
	Runs       []string `json:"runs,omitempty"`
}

// loomCommit is one commits row with what matching it to a repo needs.
type loomCommit struct {
	hash      string // abbreviated, as git printed it at commit time
	gitRemote string // normalized: one repo is captured as both SSH and HTTPS remotes
	cwd       string
	session   *Session
}

// loadLoomCommits reads every commits row joined to its session and that
// session's runs, earliest commit first and undated rows last. It opens
// read-only and refuses a database from before the runs table rather than
// reporting every bug as not in loom.
func loadLoomCommits(dbPath string) ([]loomCommit, error) {
	if _, err := os.Stat(dbPath); err != nil {
		return nil, fmt.Errorf("summaries.db not found at %s — run `loom summarize`", dbPath)
	}
	// Escaped for the same reason as friction.Load: '#' or '?' in a path
	// would otherwise cut the file: URI short and drop mode=ro with it.
	dsn := "file:" + (&url.URL{Path: dbPath}).EscapedPath() + "?mode=ro&_pragma=busy_timeout(2000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open summaries.db: %w", err)
	}
	defer db.Close()

	if v := workreport.SchemaVersionOf(db); v < schemaVersion {
		return nil, fmt.Errorf("summaries.db is at schema %d and predates the runs table (want %d) — run `loom summarize --rebuild`", v, schemaVersion)
	}

	runs := map[[2]string][]string{}
	rrows, err := db.Query(`SELECT agent, session_id, run_id FROM runs WHERE agent IS NOT NULL AND session_id IS NOT NULL ORDER BY run_id`)
	if err != nil {
		return nil, fmt.Errorf("load runs: %w", err)
	}
	for rrows.Next() {
		var agent, sessionID, runID string
		if err := rrows.Scan(&agent, &sessionID, &runID); err != nil {
			rrows.Close()
			return nil, fmt.Errorf("scan runs: %w", err)
		}
		k := [2]string{agent, sessionID}
		runs[k] = append(runs[k], runID)
	}
	rrows.Close()
	if err := rrows.Err(); err != nil {
		return nil, fmt.Errorf("load runs: %w", err)
	}

	rows, err := db.Query(`
		SELECT c.agent, c.session_id, c.commit_hash, c.git_remote, c.cwd,
		       s.model, s.cli_version
		FROM commits c
		LEFT JOIN sessions s ON s.agent = c.agent AND s.session_id = c.session_id
		ORDER BY c.committed_at IS NULL, c.committed_at, c.agent, c.session_id, c.seq
	`)
	if err != nil {
		return nil, fmt.Errorf("load commits: %w", err)
	}
	defer rows.Close()

	sessions := map[[2]string]*Session{}
	var out []loomCommit
	for rows.Next() {
		var agent, sessionID, hash string
		var gitRemote, cwd, model, cliVersion sql.NullString
		if err := rows.Scan(&agent, &sessionID, &hash, &gitRemote, &cwd, &model, &cliVersion); err != nil {
			return nil, fmt.Errorf("scan commits: %w", err)
		}
		k := [2]string{agent, sessionID}
		s := sessions[k]
		if s == nil {
			s = &Session{Agent: agent, SessionID: sessionID, Model: model.String, CLIVersion: cliVersion.String, Runs: runs[k]}
			sessions[k] = s
		}
		out = append(out, loomCommit{hash: hash, gitRemote: summaries.NormalizeRemote(gitRemote.String), cwd: cwd.String, session: s})
	}
	return out, rows.Err()
}

// ownersIn maps each of repo's HEAD-reachable commits that loom recorded to
// the session that made it. A row belongs to the repo when its git remote is
// the repo's origin once both are normalized, or, for a row with no remote,
// when its cwd lies inside the checkout; the abbreviated hash must then
// resolve on HEAD. Both are required because a 7-character hash is unique
// within a repo, not across them. A commit recorded more than once — a retried echo, an amend — is
// owned by its first row in commits' order, the earliest.
func ownersIn(repo *Repo, commits []loomCommit) map[string]*Session {
	owners := map[string]*Session{}
	for _, c := range commits {
		if !inRepo(repo, c) {
			continue
		}
		full, ok := repo.Resolve(c.hash)
		if !ok || owners[full] != nil {
			continue
		}
		owners[full] = c.session
	}
	return owners
}

func inRepo(repo *Repo, c loomCommit) bool {
	if c.gitRemote != "" {
		return repo.Remote != "" && c.gitRemote == repo.Remote
	}
	return c.cwd == repo.Path || strings.HasPrefix(c.cwd, strings.TrimSuffix(repo.Path, "/")+"/")
}
