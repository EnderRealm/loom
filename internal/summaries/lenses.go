package summaries

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"loom/internal/parse/lens"
	"loom/internal/parse/summary"
)

// lensResponseID derives a response's identity from where it was read: the
// session, the record line, the record's origin, the dispatch it answers and
// the block's position in the record. Nothing in the id comes from the
// response's content or from wall clock, so a re-fold of the same transcript
// yields the same id and the rows keyed by it are replaced, not duplicated.
func lensResponseID(agent, sessionID string, r summary.LensResponse) string {
	key := strings.Join([]string{
		agent, sessionID, strconv.Itoa(r.SourceLine), r.Origin, r.DispatchID,
		strconv.Itoa(r.Ordinal),
	}, "|")
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// clearLenses removes a session's lens rows. The child tables are keyed by
// response_id alone, so they are cleared through the parent before it goes.
func clearLenses(ctx context.Context, tx *sql.Tx, agent, sessionID string) error {
	for _, table := range []string{"lens_criteria", "lens_findings"} {
		if _, err := tx.ExecContext(ctx,
			"DELETE FROM "+table+" WHERE response_id IN (SELECT response_id FROM lens_responses WHERE agent = ? AND session_id = ?)",
			agent, sessionID); err != nil {
			return fmt.Errorf("clear %s: %w", table, err)
		}
	}
	if _, err := tx.ExecContext(ctx,
		"DELETE FROM lens_responses WHERE agent = ? AND session_id = ?",
		agent, sessionID); err != nil {
		return fmt.Errorf("clear lens_responses: %w", err)
	}
	return nil
}

// writeLenses inserts one lens_responses row per response, and the normalized
// criteria and findings rows for a parsed response. A malformed response
// leaves its raw JSON on the response row alone: what the lens returned is
// kept either way, and only a response that fits the schema is counted.
func writeLenses(ctx context.Context, tx *sql.Tx,
	sum *summary.SessionSummary, source SourceInfo) error {
	agent := string(sum.Agent)
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO lens_responses (
		    agent, session_id, seq, response_id, turn_idx, origin, dispatch_id,
		    source_path, source_line, ordinal, at, lens, verdict, summary,
		    status, malformed_reason, context_kind, context_state,
		    context_received, criteria_json, findings_json, raw
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	criteria, err := tx.PrepareContext(ctx, `
		INSERT INTO lens_criteria (response_id, ordinal, id, text, status, evidence)
		VALUES (?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer criteria.Close()
	findings, err := tx.PrepareContext(ctx, `
		INSERT INTO lens_findings (response_id, ordinal, file, line, severity,
		    category, criterion, description, fix)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer findings.Close()

	for i, r := range sum.LensResponses {
		id := lensResponseID(agent, sum.SessionID, r)
		var state, received any
		if r.Context != nil {
			state = r.Context.State
			list, err := json.Marshal(r.Context.Received)
			if err != nil {
				return fmt.Errorf("encode lens response %d context: %w", i, err)
			}
			received = string(list)
		}
		if _, err := stmt.ExecContext(ctx,
			agent, sum.SessionID, i, id, r.TurnIdx, r.Origin, strOrNull(r.DispatchID),
			strOrNull(source.Path), r.SourceLine, r.Ordinal, isoOrNull(r.At),
			strOrNull(r.Lens), strOrNull(r.Verdict), r.Summary,
			r.Status, strOrNull(r.Reason), r.ContextKind, state,
			received, rawOrNull(r.Criteria), rawOrNull(r.Findings), r.Raw,
		); err != nil {
			return fmt.Errorf("insert lens response %d: %w", i, err)
		}
		if r.Status != lens.StatusParsed {
			continue
		}
		var cs []lens.Criterion
		if len(r.Criteria) > 0 && json.Unmarshal(r.Criteria, &cs) == nil {
			for j, c := range cs {
				if _, err := criteria.ExecContext(ctx, id, j, c.ID, c.Text, c.Status, c.Evidence); err != nil {
					return fmt.Errorf("insert lens criterion %d/%d: %w", i, j, err)
				}
			}
		}
		var fs []lens.Finding
		if len(r.Findings) > 0 && json.Unmarshal(r.Findings, &fs) == nil {
			for j, f := range fs {
				var line, criterion any
				if f.Line != nil {
					line = *f.Line
				}
				if f.Criterion != nil {
					criterion = *f.Criterion
				}
				if _, err := findings.ExecContext(ctx, id, j, f.File, line, f.Severity,
					f.Category, criterion, f.Description, f.Fix); err != nil {
					return fmt.Errorf("insert lens finding %d/%d: %w", i, j, err)
				}
			}
		}
	}
	return nil
}

func rawOrNull(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	return string(raw)
}
