// Package friction ranks the harness-friction rows the summarizer stored —
// the hook asks and denies, refused permissions, tool errors and user
// interrupts a session's transcript carried — by signature. It is a counter
// and a ranked view: nothing here has a threshold, triggers extraction or
// calls a model. Every threshold is a guess until a baseline exists, so the
// view is pulled by a human. The kinds and the signature normalization the
// parsers apply live in internal/parse/friction.
package friction

import (
	"database/sql"
	"fmt"
	"io"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"loom/internal/workreport"

	_ "modernc.org/sqlite"
)

// schemaVersion is the summaries.db schema that added the friction table.
const schemaVersion = 10

// Event is one friction row as the ranking reads it.
type Event struct {
	SessionID string
	Kind      string
	Signature string
	Time      time.Time
}

// Row is one signature in the ranked view.
type Row struct {
	Signature  string
	Kind       string
	Events     int
	FirstSeen  time.Time
	LastSeen   time.Time
	ActiveDays int
	// Sessions is every session id carrying the signature, sorted and unique,
	// so a knowledge pass can be pointed at them.
	Sessions []string
}

// PerActiveDay is the ranking key: events over the distinct UTC calendar
// days on which any fired. Absolute count sorts chronic noise first; density
// separates a new spike from it.
func (r Row) PerActiveDay() float64 {
	if r.ActiveDays == 0 {
		return 0
	}
	return float64(r.Events) / float64(r.ActiveDays)
}

// Rank groups events by (kind, signature) and orders the groups by events
// per active day, then events, then signature. An event with no timestamp
// counts toward Events but toward no day.
func Rank(events []Event) []Row {
	type group struct {
		row      Row
		days     map[string]bool
		sessions map[string]bool
	}
	groups := map[[2]string]*group{}
	var order [][2]string
	for _, e := range events {
		key := [2]string{e.Kind, e.Signature}
		g := groups[key]
		if g == nil {
			g = &group{
				row:      Row{Signature: e.Signature, Kind: e.Kind},
				days:     map[string]bool{},
				sessions: map[string]bool{},
			}
			groups[key] = g
			order = append(order, key)
		}
		g.row.Events++
		if e.SessionID != "" {
			g.sessions[e.SessionID] = true
		}
		if e.Time.IsZero() {
			continue
		}
		t := e.Time.UTC()
		g.days[t.Format("2006-01-02")] = true
		if g.row.FirstSeen.IsZero() || t.Before(g.row.FirstSeen) {
			g.row.FirstSeen = t
		}
		if t.After(g.row.LastSeen) {
			g.row.LastSeen = t
		}
	}

	rows := make([]Row, 0, len(order))
	for _, key := range order {
		g := groups[key]
		g.row.ActiveDays = len(g.days)
		for id := range g.sessions {
			g.row.Sessions = append(g.row.Sessions, id)
		}
		sort.Strings(g.row.Sessions)
		rows = append(rows, g.row)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if pa, pb := a.PerActiveDay(), b.PerActiveDay(); pa != pb {
			return pa > pb
		}
		if a.Events != b.Events {
			return a.Events > b.Events
		}
		if a.Signature != b.Signature {
			return a.Signature < b.Signature
		}
		return a.Kind < b.Kind
	})
	return rows
}

// Load reads every friction row out of dbPath, or those at or after since
// when since is set. It opens read-only and refuses a database from before
// the friction table rather than reporting it as empty.
func Load(dbPath string, since time.Time) ([]Event, error) {
	if _, err := os.Stat(dbPath); err != nil {
		return nil, fmt.Errorf("summaries.db not found at %s — run `loom summarize`", dbPath)
	}
	// mode=ro keeps us out of the summarizer's way; it holds the only writer.
	// A file: URI reads '#' and '?' as fragment and query delimiters, so a
	// path carrying either would be cut short — and mode=ro dropped with it —
	// and a database opened writable somewhere else; the escaped form is
	// decoded by SQLite's URI parser.
	dsn := "file:" + (&url.URL{Path: dbPath}).EscapedPath() + "?mode=ro&_pragma=busy_timeout(2000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open summaries.db: %w", err)
	}
	defer db.Close()

	if v := workreport.SchemaVersionOf(db); v < schemaVersion {
		return nil, fmt.Errorf("summaries.db is at schema %d and predates the friction table (want %d) — run `loom summarize --rebuild`", v, schemaVersion)
	}

	q := `SELECT session_id, kind, signature, ts FROM friction`
	var args []any
	if !since.IsZero() {
		q += ` WHERE ts >= ?`
		args = append(args, since.UTC().Format(time.RFC3339Nano))
	}
	q += ` ORDER BY ts, session_id, seq`
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("load friction: %w", err)
	}
	defer rows.Close()

	var events []Event
	for rows.Next() {
		var e Event
		var ts sql.NullString
		if err := rows.Scan(&e.SessionID, &e.Kind, &e.Signature, &ts); err != nil {
			return nil, fmt.Errorf("scan friction: %w", err)
		}
		if ts.Valid {
			e.Time, _ = time.Parse(time.RFC3339Nano, ts.String)
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

// Render prints rows as an aligned table, the first top rows when top is
// positive. With withSessions each row is followed by an indented line
// naming its session ids. Every transcript-derived field passes through
// sanitize on the way out.
func Render(w io.Writer, rows []Row, top int, withSessions bool) {
	if top > 0 && len(rows) > top {
		rows = rows[:top]
	}
	const format = "%8s %7s %5s %8s %10s %10s %-28s %s\n"
	fmt.Fprintf(w, format, "per-day", "events", "days", "sessions", "first", "last", "kind", "signature")
	for _, r := range rows {
		fmt.Fprintf(w, format,
			fmt.Sprintf("%.1f", r.PerActiveDay()),
			fmt.Sprint(r.Events),
			fmt.Sprint(r.ActiveDays),
			fmt.Sprint(len(r.Sessions)),
			day(r.FirstSeen), day(r.LastSeen),
			sanitize(r.Kind), sanitize(r.Signature))
		if withSessions {
			ids := make([]string, len(r.Sessions))
			for i, id := range r.Sessions {
				ids[i] = sanitize(id)
			}
			fmt.Fprintf(w, "         %s\n", strings.Join(ids, ", "))
		}
	}
}

// sanitize spells every C0 and C1 control and DEL as its \x escape. A tool
// error is text a tool returned, so an OSC or CSI sequence in it would
// otherwise reach the terminal the ranking is read on and drive it.
func sanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
			fmt.Fprintf(&b, `\x%02x`, r)
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func day(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Format("2006-01-02")
}
