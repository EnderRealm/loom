package cursorparse

import (
	"fmt"
	"io"

	"loom/internal/parse/summary"
)

// Transcript retains ordered, unredacted conversation records for the local
// Python preprocessor. Tool results must reach its redactor before truncation.
type Transcript struct {
	SessionID string                  `json:"session_id"`
	Records   []map[string]any        `json:"records"`
	Unknown   []summary.UnknownRecord `json:"diagnostics"`
}

func ReadTranscript(r io.Reader) (*Transcript, error) {
	t := &Transcript{Records: []map[string]any{}}
	s, err := parse(r, t)
	if err != nil {
		return nil, err
	}
	if len(s.Turns) == 0 || s.SessionID == "" {
		return nil, fmt.Errorf("Cursor journal has no identifiable conversation: %v", s.Unknown)
	}
	t.SessionID, t.Unknown = s.SessionID, s.Unknown
	return t, nil
}

func (st *state) appendRecord(role string, content any) {
	if st.transcript != nil {
		st.transcript.Records = append(st.transcript.Records, map[string]any{
			"type": role, "message": map[string]any{"content": content},
		})
	}
}

func (st *state) appendTool(call summary.ToolCall, ce callEvidence) {
	if st.transcript == nil {
		return
	}
	name := call.ToolName
	if call.Kind == summary.KindBash {
		name = "Bash"
	}
	if ce.hasArgs {
		st.appendRecord("assistant", []map[string]any{{
			"type": "tool_use", "id": call.CallID, "name": name, "input": ce.args,
		}})
	}
	if ce.hasResult {
		st.appendRecord("user", []map[string]any{{
			"type": "tool_result", "tool_use_id": call.CallID,
			"content": ce.result, "is_error": ce.isError,
		}})
	}
}
