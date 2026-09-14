package source

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	_ "modernc.org/sqlite"
)

const CursorAgent = "cursor-cli"
const CursorStoreFormat = "cursor-store-v1"

// SnapshotAdapter reads a mutable source as records rather than byte offsets.
// The staging journal retains successive values and owns restart recovery.
type SnapshotAdapter interface {
	Adapter
	ReadSnapshot(Session) ([]SnapshotRecord, error)
}

// SnapshotRecord preserves SQLite values (including opaque/encrypted blobs)
// and source files as base64 bytes. Kind and Key identify the mutable record;
// Deleted retires its current value without erasing the archived evidence.
type SnapshotRecord struct {
	Format    string `json:"format"`
	Kind      string `json:"kind"`
	Key       string `json:"key"`
	ValueType string `json:"value_type"`
	Value     []byte `json:"value"`
	Deleted   bool   `json:"deleted,omitempty"`
}

type cursorAdapter struct{}

func (cursorAdapter) Agent() string { return CursorAgent }

type cursorMetadata struct {
	AgentID  string `json:"agentId"`
	Subagent *struct {
		Parent string `json:"parentAgentId"`
		Root   string `json:"rootParentAgentId"`
		ToolID string `json:"toolCallId"`
		Type   string `json:"typeName"`
	} `json:"subagentInfo"`
}

// Cursor CLI 2026.09.10-fd3934a stores chats/<workspace hash>/<uuid>/store.db.
// Children share the workspace directory; their meta row names their parent.
func (cursorAdapter) List() ([]Session, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	base := filepath.Join(home, ".cursor", "chats")
	var sessions []Session
	var failures []error
	err = filepath.WalkDir(base, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if path == base && errors.Is(walkErr, os.ErrNotExist) {
				return nil
			}
			failures = append(failures, walkErr)
			return nil
		}
		if d.IsDir() || d.Name() != "store.db" {
			return nil
		}
		if !d.Type().IsRegular() {
			failures = append(failures, fmt.Errorf("cursor %s: store.db is not a regular file", path))
			return nil
		}
		sid := filepath.Base(filepath.Dir(path))
		if !looksLikeUUID(sid) {
			failures = append(failures, fmt.Errorf("cursor %s: unsupported session id", path))
			return nil
		}
		meta, err := readCursorMetadata(path)
		if err != nil {
			failures = append(failures, fmt.Errorf("cursor %s: %w", path, err))
			return nil
		}
		cwd, err := cursorCwd(path, meta, map[string]bool{})
		if err != nil {
			failures = append(failures, fmt.Errorf("cursor %s: %w", path, err))
			return nil
		}
		s := Session{Project: encodeProjectPath(cwd), SessionID: sid, Path: path, Cwd: cwd}
		if meta.Subagent != nil {
			s.Subagent = &Subagent{ParentSessionID: meta.Subagent.Parent, AgentType: meta.Subagent.Type, ToolUseID: meta.Subagent.ToolID}
		}
		sessions = append(sessions, s)
		return nil
	})
	return sessions, errors.Join(append(failures, err)...)
}

func openCursorStore(path string) (*sql.DB, error) {
	u := url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

func decodeCursorMetadata(raw string, sid string) (cursorMetadata, error) {
	var meta cursorMetadata
	data, err := hex.DecodeString(raw)
	if err != nil {
		return meta, fmt.Errorf("unsupported meta encoding: %w", err)
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return meta, fmt.Errorf("unsupported meta JSON: %w", err)
	}
	if meta.AgentID != sid {
		return meta, fmt.Errorf("metadata agentId does not match session directory")
	}
	if meta.Subagent != nil && (!looksLikeUUID(meta.Subagent.Parent) || meta.Subagent.Parent == sid) {
		return meta, fmt.Errorf("invalid parentAgentId")
	}
	return meta, nil
}

func readCursorMetadata(path string) (cursorMetadata, error) {
	db, err := openCursorStore(path)
	if err != nil {
		return cursorMetadata{}, err
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var raw string
	if err := db.QueryRowContext(ctx, "SELECT value FROM meta WHERE key = '0'").Scan(&raw); err != nil {
		return cursorMetadata{}, fmt.Errorf("read meta: %w", err)
	}
	return decodeCursorMetadata(raw, filepath.Base(filepath.Dir(path)))
}

func cursorCwd(path string, meta cursorMetadata, seen map[string]bool) (string, error) {
	if seen[path] {
		return "", fmt.Errorf("cyclic parent relationship")
	}
	seen[path] = true
	data, err := os.ReadFile(filepath.Join(filepath.Dir(path), "meta.json"))
	if err == nil {
		var sidecar struct {
			Schema int    `json:"schemaVersion"`
			Cwd    string `json:"cwd"`
		}
		if err := json.Unmarshal(data, &sidecar); err != nil {
			return "", fmt.Errorf("parse meta.json: %w", err)
		}
		if sidecar.Schema != 1 {
			return "", fmt.Errorf("unsupported meta.json schema %d", sidecar.Schema)
		}
		if strings.ContainsFunc(sidecar.Cwd, unicode.IsControl) {
			return "", fmt.Errorf("invalid control character in project cwd")
		}
		if filepath.IsAbs(sidecar.Cwd) {
			return sidecar.Cwd, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if meta.Subagent == nil {
		return "", fmt.Errorf("project cwd unavailable in meta.json")
	}
	parent := filepath.Join(filepath.Dir(filepath.Dir(path)), meta.Subagent.Parent, "store.db")
	parentMeta, err := readCursorMetadata(parent)
	if err != nil {
		return "", fmt.Errorf("read parent metadata: %w", err)
	}
	return cursorCwd(parent, parentMeta, seen)
}

func (cursorAdapter) ReadSnapshot(s Session) ([]SnapshotRecord, error) {
	db, err := openCursorStore(s.Path)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var version int
	if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return nil, err
	}
	if version != 1 {
		return nil, fmt.Errorf("unsupported Cursor store user_version %d", version)
	}
	// Refuse schema drift rather than successfully archiving only known tables.
	rows, err := tx.QueryContext(ctx, "SELECT name FROM sqlite_master WHERE type = 'table' ORDER BY name")
	if err != nil {
		return nil, err
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return nil, err
		}
		tables = append(tables, name)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(tables) != 2 || tables[0] != "blobs" || tables[1] != "meta" {
		return nil, fmt.Errorf("unsupported Cursor store tables %q", tables)
	}
	var records []SnapshotRecord
	hasMetadata := false
	for _, table := range []struct{ name, key, value string }{{"blobs", "id", "data"}, {"meta", "key", "value"}} {
		columns, err := tx.QueryContext(ctx, "SELECT * FROM "+table.name+" LIMIT 0")
		if err != nil {
			return nil, err
		}
		names, err := columns.Columns()
		columns.Close()
		if err != nil || len(names) != 2 || names[0] != table.key || names[1] != table.value {
			return nil, fmt.Errorf("unsupported Cursor %s columns %q", table.name, names)
		}
		rows, err := tx.QueryContext(ctx, "SELECT "+table.key+", typeof("+table.value+"), "+table.value+" FROM "+table.name+" ORDER BY "+table.key)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			r := SnapshotRecord{Format: CursorStoreFormat, Kind: table.name}
			if err := rows.Scan(&r.Key, &r.ValueType, &r.Value); err != nil {
				rows.Close()
				return nil, err
			}
			if r.ValueType != "blob" && r.ValueType != "text" && r.ValueType != "null" {
				rows.Close()
				return nil, fmt.Errorf("unsupported Cursor %s value type %q", table.name, r.ValueType)
			}
			if r.Kind == "meta" && r.Key == "0" {
				hasMetadata = true
				meta, err := decodeCursorMetadata(string(r.Value), s.SessionID)
				if err != nil {
					rows.Close()
					return nil, err
				}
				parent := ""
				if meta.Subagent != nil {
					parent = meta.Subagent.Parent
				}
				if parent != s.ParentID() {
					rows.Close()
					return nil, fmt.Errorf("parent changed during capture; retry next tick")
				}
			}
			records = append(records, r)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	if !hasMetadata {
		return nil, fmt.Errorf("Cursor store has no session metadata")
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	for _, name := range []string{"meta.json", "prompt_history.json"} {
		data, err := os.ReadFile(filepath.Join(filepath.Dir(s.Path), name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !json.Valid(data) {
			return nil, fmt.Errorf("invalid %s; retry next tick", name)
		}
		records = append(records, SnapshotRecord{Format: CursorStoreFormat, Kind: "file", Key: name, ValueType: "blob", Value: data})
	}
	return records, nil
}
