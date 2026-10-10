package codexparse

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

// ExternalImportTurnPrefix opens the turn_id of the task_started record in a
// rollout Codex Desktop wrote as a copy of another agent's session (it also
// appends an "<EXTERNAL SESSION IMPORTED>" agent_message). The original is
// captured under claude-code, so loom neither ships nor folds the copy; see
// docs/attribution-stamps.md. A genuine session's turn ids carry no prefix.
const ExternalImportTurnPrefix = "external-import-turn-"

var taskStarted = []byte(`"task_started"`)

// ExternalImport reads the record that follows a rollout's session_meta line
// from r, which must be positioned just past that line, and reports whether
// it is an import's task_started. Codex Desktop writes the copy in one go
// with the marker straight after the meta. Deciding on that one record, not
// on a scan for the first task_started, keeps capture of a genuine session
// independent of task_started being written at all. decided is false while
// the record is missing or half-written; the caller checks back later.
func ExternalImport(r io.Reader) (imported, decided bool, err error) {
	line, err := bufio.NewReader(r).ReadBytes('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, false, err
	}
	if len(line) == 0 || line[len(line)-1] != '\n' {
		return false, false, nil
	}
	// The substring test settles every genuine session without the decoder.
	if !bytes.Contains(line, taskStarted) {
		return false, true, nil
	}
	var rec struct {
		Type    string `json:"type"`
		Payload struct {
			Type   string `json:"type"`
			TurnID string `json:"turn_id"`
		} `json:"payload"`
	}
	if json.Unmarshal(line, &rec) != nil {
		return false, true, nil
	}
	return rec.Type == "event_msg" && rec.Payload.Type == "task_started" &&
		strings.HasPrefix(rec.Payload.TurnID, ExternalImportTurnPrefix), true, nil
}
