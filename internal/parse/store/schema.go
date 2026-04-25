package store

// schemaSQL is the canonical schema for ~/.loom/summaries.db. We keep all
// tables agent-agnostic; an `agent` column on every top-level row lets us
// slice cleanly across producers.
//
// Bump schemaVersion whenever this changes. Migrations are not yet
// auto-generated — the summarizer is idempotent and can rebuild from raw
// JSONL, so a clean drop is the current upgrade path.
const schemaVersion = 1

const schemaSQL = `
CREATE TABLE IF NOT EXISTS schema_meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS sessions (
    session_id        TEXT PRIMARY KEY,
    agent             TEXT NOT NULL,
    project           TEXT,
    cwd               TEXT,
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
    summarized_at     TEXT
);
CREATE INDEX IF NOT EXISTS idx_sessions_agent ON sessions(agent);
CREATE INDEX IF NOT EXISTS idx_sessions_project ON sessions(project);
CREATE INDEX IF NOT EXISTS idx_sessions_start ON sessions(start_time);

CREATE TABLE IF NOT EXISTS turns (
    session_id        TEXT NOT NULL,
    idx               INTEGER NOT NULL,
    turn_id           TEXT,
    user_message      TEXT,
    assistant_text    TEXT,
    reasoning_chars   INTEGER,
    stop_reason       TEXT,
    completion_status TEXT,
    input_tokens      INTEGER,
    output_tokens     INTEGER,
    cache_read_tokens INTEGER,
    started_at        TEXT,
    ended_at          TEXT,
    wall_clock_ms     INTEGER,
    PRIMARY KEY (session_id, idx)
);
CREATE INDEX IF NOT EXISTS idx_turns_session ON turns(session_id);

CREATE TABLE IF NOT EXISTS tool_calls (
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
    PRIMARY KEY (session_id, seq)
);
CREATE INDEX IF NOT EXISTS idx_tool_calls_kind ON tool_calls(tool_kind);
CREATE INDEX IF NOT EXISTS idx_tool_calls_name ON tool_calls(tool_name);
CREATE INDEX IF NOT EXISTS idx_tool_calls_session ON tool_calls(session_id);

CREATE TABLE IF NOT EXISTS errors (
    session_id TEXT NOT NULL,
    seq        INTEGER NOT NULL,
    turn_idx   INTEGER,
    source     TEXT,
    message    TEXT,
    ts         TEXT,
    PRIMARY KEY (session_id, seq)
);
CREATE INDEX IF NOT EXISTS idx_errors_source ON errors(source);

CREATE TABLE IF NOT EXISTS compactions (
    session_id    TEXT NOT NULL,
    seq           INTEGER NOT NULL,
    ts            TEXT,
    anchor        TEXT,
    tokens_before INTEGER,
    tokens_after  INTEGER,
    PRIMARY KEY (session_id, seq)
);

CREATE TABLE IF NOT EXISTS token_counts (
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
    PRIMARY KEY (session_id, seq)
);

CREATE TABLE IF NOT EXISTS files_touched (
    session_id TEXT NOT NULL,
    path       TEXT NOT NULL,
    op         TEXT NOT NULL,
    count      INTEGER,
    PRIMARY KEY (session_id, path, op)
);
CREATE INDEX IF NOT EXISTS idx_files_path ON files_touched(path);

CREATE TABLE IF NOT EXISTS subagents (
    session_id      TEXT NOT NULL,
    seq             INTEGER NOT NULL,
    parent_turn_idx INTEGER,
    agent_type      TEXT,
    prompt          TEXT,
    result_summary  TEXT,
    duration_ms     INTEGER,
    error_count     INTEGER,
    PRIMARY KEY (session_id, seq)
);

CREATE TABLE IF NOT EXISTS unknown_records (
    session_id TEXT NOT NULL,
    agent      TEXT NOT NULL,
    type       TEXT NOT NULL,
    subtype    TEXT NOT NULL,
    count      INTEGER NOT NULL,
    first_seen TEXT,
    PRIMARY KEY (session_id, agent, type, subtype)
);
CREATE INDEX IF NOT EXISTS idx_unknown_type ON unknown_records(agent, type, subtype);
`
