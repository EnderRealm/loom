package workreport

import (
	"database/sql"
	"sort"
)

// CursorChild reconciles a parent's structured dispatch result with the
// child's metadata. The dispatch remains visible before its transcript arrives.
type CursorChild struct {
	SessionID, DispatchID, StartedAt, EndedAt string
	DispatchIDs                               []string
	TurnIdx                                   int
	Resolved, TranscriptMissing               bool
	DurationMs                                *int64
}

func CursorChildren(db *sql.DB, parentID string) ([]CursorChild, error) {
	// UNION brings in both observed dispatches and independently shipped child
	// metadata. The same child is folded once, with disagreements unresolved.
	rows, err := db.Query(`
		SELECT COALESCE(child_session_id, child_resume_id), call_id, child_duration_ms, NULL, NULL, NULL, child_resume_id, 1
		FROM tool_calls WHERE agent = 'cursor-cli' AND session_id = ? AND COALESCE(child_session_id, child_resume_id) <> ''
		UNION ALL
		SELECT session_id, parent_tool_call_id, NULL, start_time, end_time, parent_session_id, NULL, 0
		FROM sessions WHERE agent = 'cursor-cli' AND (parent_session_id = ? OR session_id IN (
		    SELECT COALESCE(child_session_id, child_resume_id) FROM tool_calls WHERE agent = 'cursor-cli' AND session_id = ?
		))`, parentID, parentID, parentID)
	if err != nil {
		return nil, err
	}
	type evidence struct {
		CursorChild
		conflict         bool
		creation         string
		metadataDispatch string
		resumes          map[string]bool
	}
	children := map[string]*evidence{}
	dispatches := map[string]*evidence{}
	for rows.Next() {
		var id string
		var dispatch, start, end, parent, resume sql.NullString
		var duration sql.NullInt64
		var declared bool
		if err := rows.Scan(&id, &dispatch, &duration, &start, &end, &parent, &resume, &declared); err != nil {
			rows.Close()
			return nil, err
		}
		c := children[id]
		if c == nil {
			c = &evidence{CursorChild: CursorChild{SessionID: id, TranscriptMissing: true}, resumes: map[string]bool{}}
			children[id] = c
		}
		if dispatch.String != "" {
			if other := dispatches[dispatch.String]; other != nil && other != c {
				other.conflict, c.conflict = true, true
			} else {
				dispatches[dispatch.String] = c
			}
			c.DispatchIDs = appendDistinct(c.DispatchIDs, dispatch.String)
		}
		if declared {
			if resume.String != "" {
				c.conflict = c.conflict || resume.String != id
				c.resumes[dispatch.String] = resume.String == id
			} else {
				c.conflict = c.conflict || (c.creation != "" && c.creation != dispatch.String)
				c.creation = dispatch.String
			}
			if duration.Valid {
				d := duration.Int64
				c.DurationMs = &d
			}
		} else {
			c.StartedAt, c.EndedAt, c.TranscriptMissing = start.String, end.String, false
			c.metadataDispatch = dispatch.String
			c.conflict = c.conflict || (parent.String != "" && parent.String != parentID)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	// A resumed child is one transcript. All its dispatches must fall within
	// the same invocation; sharing it across invocations needs a finer span
	// than the source provides, so that attribution remains unresolved.
	turns, err := db.Query(`SELECT idx, user_message FROM turns WHERE agent = 'cursor-cli' AND session_id = ? ORDER BY idx`, parentID)
	if err != nil {
		return nil, err
	}
	var boundaries []int
	for turns.Next() {
		var idx int
		var message sql.NullString
		if err := turns.Scan(&idx, &message); err != nil {
			turns.Close()
			return nil, err
		}
		if _, ok := invocation(message.String); ok {
			boundaries = append(boundaries, idx)
		}
	}
	err = turns.Err()
	turns.Close()
	if err != nil {
		return nil, err
	}
	out := make([]CursorChild, 0, len(children))
	for _, c := range children {
		// Metadata may identify the creation or an explicit resume. Another
		// existing call does not explain a disagreement with a creation result.
		if c.metadataDispatch != "" && c.creation != "" && c.metadataDispatch != c.creation && !c.resumes[c.metadataDispatch] {
			c.conflict = true
		}
		sort.Strings(c.DispatchIDs)
		c.Resolved = !c.conflict && len(c.DispatchIDs) > 0
		owner := -1
		for i, dispatch := range c.DispatchIDs {
			var count int
			var turn sql.NullInt64
			if err := db.QueryRow(`SELECT count(*), min(turn_idx) FROM tool_calls
				WHERE agent = 'cursor-cli' AND session_id = ? AND call_id = ?`, parentID, dispatch).Scan(&count, &turn); err != nil {
				return nil, err
			}
			invocation := sort.Search(len(boundaries), func(j int) bool { return boundaries[j] > int(turn.Int64) })
			c.Resolved = c.Resolved && count == 1 && turn.Valid && (i == 0 || owner == invocation)
			owner = invocation
			if i == 0 || dispatch == c.creation {
				c.DispatchID, c.TurnIdx = dispatch, int(turn.Int64)
			}
		}
		if len(c.DispatchIDs) > 1 {
			// Per-dispatch durations do not establish the resumed session's
			// duration. Prefer its own observed bounds below.
			c.DurationMs = nil
		}
		start, end := parseTime(sql.NullString{String: c.StartedAt, Valid: c.StartedAt != ""}), parseTime(sql.NullString{String: c.EndedAt, Valid: c.EndedAt != ""})
		if !start.IsZero() && !end.IsZero() && !end.Before(start) {
			d := end.Sub(start).Milliseconds()
			c.DurationMs = &d
		}
		out = append(out, c.CursorChild)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SessionID < out[j].SessionID })
	return out, nil
}
