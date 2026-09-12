package summaries

// schemaSQL is the canonical schema for ~/.loom/summaries.db. We keep all
// tables agent-agnostic; an `agent` column on every top-level row lets us
// slice cleanly across producers.
//
// schemaVersion 8: sessions gains parent_session_id and spawn_depth — the
// spawning thread a Codex subagent transcript names in session_meta, NULL for
// a top-level session and for every Claude session — and four tables for the
// execution records docs/execution-records.md describes: runs, executions,
// execution_diagnostics and execution_imports. Every v7 database has the
// columns absent and the tables empty, and the watch-mode summarizer skips
// sessions whose file is unchanged, so v7 reads as outdated until a
// `loom summarize --rebuild` folds the transcripts and records in.
//
// schemaVersion 7: turns gains cache_creation_tokens, cache_creation_1h_tokens,
// speed and usage_mixed; subagents gains model, speed, input_tokens,
// output_tokens, cache_read_tokens, cache_creation_tokens,
// cache_creation_1h_tokens and usage_mixed — the dispatch's own usage off its
// own transcript, all NULL where no transcript was folded. Every v6 database has those columns absent, and the watch-mode
// summarizer skips sessions whose file is unchanged, so v6 reads as outdated
// until a `loom summarize --rebuild` folds the transcripts in.
//
// schemaVersion 6: turns gains model, effort and cli_version — the conditions
// in force for each turn, NULL where the transcript carried none. Every v5
// database has those columns absent, and the watch-mode summarizer skips
// sessions whose file is unchanged, so v5 reads as outdated until a
// `loom summarize --rebuild` folds the transcripts in.
//
// schemaVersion 5: the subagents table is populated — subagent durations now
// come from the dispatched transcript's own records instead of the parent's
// acknowledgement. Every v4 database has that table empty, and the watch-mode
// summarizer skips sessions whose parent file is unchanged, so v4 reads as
// outdated until a `loom summarize --rebuild` folds the transcripts in.
//
// schemaVersion 4: adds the commits table, derived from git's commit
// confirmation line in bash tool output. Existing v3 databases lack the
// table, so the 24h activity view treats them as outdated until a
// `loom summarize --rebuild` repopulates them.
//
// schemaVersion 3: sessions gains git_remote and cwd_raw columns sourced
// from the per-session meta sidecar (~/.loom/received/<agent>/<project>/
// <session>.meta.json) the receiver writes when the wire payload carries
// project identity. These are the authoritative project handle —
// downstream readers should prefer git_remote over cwd_raw over slug.
//
// schemaVersion 2: session identity is composite (agent, session_id) end to
// end. Earlier versions used session_id alone as the PK, which disagreed with
// every read-side join in the TUI. The summary DB is permanently disposable —
// `loom summarize --rebuild` drops and rebuilds from ~/.loom/received/.
const schemaVersion = 8

// commitsSchemaVersion is the version that introduced the commits table.
// Deliberately pinned rather than tracked to schemaVersion: readers that need
// commits gate on this, so bumping schemaVersion must not start rejecting
// databases that already hold every commit those readers query.
const commitsSchemaVersion = 4

const schemaSQL = `
CREATE TABLE IF NOT EXISTS schema_meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS sessions (
    agent             TEXT NOT NULL,
    session_id        TEXT NOT NULL,
    project           TEXT,
    cwd               TEXT,
    cwd_raw           TEXT,
    git_remote        TEXT,
    git_branch        TEXT,
    cli_version       TEXT,
    model_provider    TEXT,
    model             TEXT,
    personality       TEXT,
    custom_title      TEXT,
    agent_name        TEXT,
    pr_url            TEXT,
    start_time        TEXT,
    end_time          TEXT,
    duration_ms       INTEGER,
    turn_count        INTEGER,
    tool_call_count   INTEGER,
    error_count       INTEGER,
    compacted         INTEGER,
    input_tokens      INTEGER,
    output_tokens     INTEGER,
    cache_read_tokens INTEGER,
    source_path       TEXT,
    source_size       INTEGER,
    source_mtime      TEXT,
    summarized_at     TEXT,
    parent_session_id TEXT,
    spawn_depth       INTEGER,
    PRIMARY KEY (agent, session_id)
);
CREATE INDEX IF NOT EXISTS idx_sessions_project ON sessions(project);
CREATE INDEX IF NOT EXISTS idx_sessions_start ON sessions(start_time);
CREATE INDEX IF NOT EXISTS idx_sessions_git_remote ON sessions(git_remote);
CREATE INDEX IF NOT EXISTS idx_sessions_parent ON sessions(parent_session_id);

CREATE TABLE IF NOT EXISTS turns (
    agent             TEXT NOT NULL,
    session_id        TEXT NOT NULL,
    idx               INTEGER NOT NULL,
    turn_id           TEXT,
    user_message      TEXT,
    assistant_text    TEXT,
    reasoning_chars   INTEGER,
    stop_reason       TEXT,
    completion_status TEXT,
    model             TEXT,
    effort            TEXT,
    cli_version       TEXT,
    input_tokens      INTEGER,
    output_tokens     INTEGER,
    cache_read_tokens INTEGER,
    cache_creation_tokens    INTEGER,
    cache_creation_1h_tokens INTEGER,
    speed             TEXT,
    usage_mixed       INTEGER,
    started_at        TEXT,
    ended_at          TEXT,
    wall_clock_ms     INTEGER,
    PRIMARY KEY (agent, session_id, idx)
);

CREATE TABLE IF NOT EXISTS tool_calls (
    agent          TEXT NOT NULL,
    session_id     TEXT NOT NULL,
    turn_idx       INTEGER,
    seq            INTEGER NOT NULL,
    call_id        TEXT,
    tool_kind      TEXT,
    tool_name      TEXT,
    key_arg        TEXT,
    started_at     TEXT,
    duration_ms    INTEGER,
    exit_code      INTEGER,
    is_error       INTEGER,
    result_summary TEXT,
    PRIMARY KEY (agent, session_id, seq)
);
CREATE INDEX IF NOT EXISTS idx_tool_calls_kind ON tool_calls(tool_kind);
CREATE INDEX IF NOT EXISTS idx_tool_calls_name ON tool_calls(tool_name);

CREATE TABLE IF NOT EXISTS errors (
    agent      TEXT NOT NULL,
    session_id TEXT NOT NULL,
    seq        INTEGER NOT NULL,
    turn_idx   INTEGER,
    source     TEXT,
    message    TEXT,
    ts         TEXT,
    PRIMARY KEY (agent, session_id, seq)
);
CREATE INDEX IF NOT EXISTS idx_errors_source ON errors(source);

CREATE TABLE IF NOT EXISTS compactions (
    agent         TEXT NOT NULL,
    session_id    TEXT NOT NULL,
    seq           INTEGER NOT NULL,
    ts            TEXT,
    anchor        TEXT,
    tokens_before INTEGER,
    tokens_after  INTEGER,
    PRIMARY KEY (agent, session_id, seq)
);

CREATE TABLE IF NOT EXISTS token_counts (
    agent             TEXT NOT NULL,
    session_id        TEXT NOT NULL,
    seq               INTEGER NOT NULL,
    turn_idx          INTEGER,
    ts                TEXT,
    input             INTEGER,
    output            INTEGER,
    cached            INTEGER,
    reasoning         INTEGER,
    limit_id          TEXT,
    limit_used_pct    REAL,
    PRIMARY KEY (agent, session_id, seq)
);

CREATE TABLE IF NOT EXISTS files_touched (
    agent      TEXT NOT NULL,
    session_id TEXT NOT NULL,
    path       TEXT NOT NULL,
    op         TEXT NOT NULL,
    count      INTEGER,
    PRIMARY KEY (agent, session_id, path, op)
);
CREATE INDEX IF NOT EXISTS idx_files_path ON files_touched(path);

CREATE TABLE IF NOT EXISTS subagents (
    agent           TEXT NOT NULL,
    session_id      TEXT NOT NULL,
    seq             INTEGER NOT NULL,
    parent_turn_idx INTEGER,
    agent_type      TEXT,
    prompt          TEXT,
    result_summary  TEXT,
    duration_ms     INTEGER,
    error_count     INTEGER,
    model           TEXT,
    speed           TEXT,
    input_tokens    INTEGER,
    output_tokens   INTEGER,
    cache_read_tokens        INTEGER,
    cache_creation_tokens    INTEGER,
    cache_creation_1h_tokens INTEGER,
    usage_mixed     INTEGER,
    PRIMARY KEY (agent, session_id, seq)
);

CREATE TABLE IF NOT EXISTS unknown_records (
    agent      TEXT NOT NULL,
    session_id TEXT NOT NULL,
    type       TEXT NOT NULL,
    subtype    TEXT NOT NULL,
    count      INTEGER NOT NULL,
    first_seen TEXT,
    PRIMARY KEY (agent, session_id, type, subtype)
);
CREATE INDEX IF NOT EXISTS idx_unknown_type ON unknown_records(agent, type, subtype);

CREATE TABLE IF NOT EXISTS commits (
    agent         TEXT NOT NULL,
    session_id    TEXT NOT NULL,
    seq           INTEGER NOT NULL,
    committed_at  TEXT,
    git_remote    TEXT,
    cwd           TEXT,
    commit_hash   TEXT NOT NULL,
    branch        TEXT,
    subject       TEXT,
    files_changed INTEGER,
    PRIMARY KEY (agent, session_id, seq)
);
CREATE INDEX IF NOT EXISTS idx_commits_committed ON commits(committed_at);

CREATE TABLE IF NOT EXISTS runs (
    run_id           TEXT PRIMARY KEY,
    ticket           TEXT,
    runtime          TEXT,
    agent            TEXT,
    session_id       TEXT,
    producer         TEXT,
    started_at       TEXT,
    ended_at         TEXT,
    outcome          TEXT,
    reporting_cutoff TEXT,
    recorded_at      TEXT,
    source_path      TEXT NOT NULL,
    source_line      INTEGER NOT NULL,
    first_seen       TEXT NOT NULL,
    last_seen        TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_runs_session ON runs(agent, session_id);
CREATE INDEX IF NOT EXISTS idx_runs_started ON runs(started_at);

CREATE TABLE IF NOT EXISTS executions (
    execution_id        TEXT PRIMARY KEY,
    run_id              TEXT NOT NULL,
    parent_execution_id TEXT,
    execution_kind      TEXT,
    agent               TEXT,
    session_id          TEXT,
    dispatch_id         TEXT,
    stage               TEXT,
    stage_occurrence    INTEGER,
    lens                TEXT,
    round               INTEGER,
    attempt             INTEGER,
    started_at          TEXT,
    ended_at            TEXT,
    outcome             TEXT,
    producer            TEXT,
    recorded_at         TEXT,
    source_path         TEXT NOT NULL,
    source_line         INTEGER NOT NULL,
    first_seen          TEXT NOT NULL,
    last_seen           TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_executions_run ON executions(run_id);
CREATE INDEX IF NOT EXISTS idx_executions_session ON executions(agent, session_id);

CREATE TABLE IF NOT EXISTS execution_diagnostics (
    source_path  TEXT NOT NULL,
    source_line  INTEGER NOT NULL,
    code         TEXT NOT NULL,
    run_id       TEXT,
    execution_id TEXT,
    detail       TEXT,
    PRIMARY KEY (source_path, source_line, code)
);
CREATE INDEX IF NOT EXISTS idx_execution_diagnostics_run ON execution_diagnostics(run_id);

CREATE TABLE IF NOT EXISTS execution_imports (
    source_path TEXT PRIMARY KEY,
    size        INTEGER,
    mtime       TEXT,
    imported_at TEXT
);
`
