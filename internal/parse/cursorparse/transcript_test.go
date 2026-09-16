package cursorparse

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
)

func TestTranscriptRepeatedArguments(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(map[bool]string{false: "reordered", true: "conflicting"}[conflict], func(t *testing.T) {
			raw, err := os.ReadFile("testdata/parent.jsonl")
			if err != nil {
				t.Fatal(err)
			}
			var duplicate []byte
			raw = mutateBlob(t, raw, func(data []byte) ([]byte, bool) {
				var msg jsonMessage
				if json.Unmarshal(data, &msg) != nil || msg.Role != "assistant" {
					return nil, false
				}
				var parts []jsonPart
				if json.Unmarshal(msg.Content, &parts) != nil {
					return nil, false
				}
				for i, part := range parts {
					if part.ToolCallID != "cursor-lens-call-2" {
						continue
					}
					parts[i].Args = json.RawMessage(`{"command":"git status","description":"check"}`)
					msg.Content, _ = json.Marshal(parts)
					original, _ := json.Marshal(msg)
					command := "git status"
					if conflict {
						command = "git diff"
					}
					parts[i].Args = json.RawMessage(`{ "description" : "check", "command" : "` + command + `" }`)
					msg.Content, _ = json.Marshal(parts)
					duplicate, _ = json.Marshal(msg)
					return original, true
				}
				return nil, false
			})
			id := bytes.Repeat([]byte{0xfe}, 32)
			raw = mutateBlob(t, raw, func(data []byte) ([]byte, bool) {
				fields, ok := decodeWire(data)
				if !ok || len(fieldBytes(fields, 8)) == 0 || len(fieldBytes(fields, 1)) == 0 {
					return nil, false
				}
				// Another reachable model message, alongside the existing call.
				return append(append(data, 0x0a, 32), id...), true
			})
			row, _ := json.Marshal(journalRecord{Format: journalFormat, Kind: "blobs", Key: hex.EncodeToString(id), ValueType: "blob", Value: base64.StdEncoding.EncodeToString(duplicate)})
			raw = append(append(raw, row...), '\n')
			s, err := Parse(bytes.NewReader(raw))
			if err != nil {
				t.Fatal(err)
			}
			call := s.ToolCalls[len(s.ToolCalls)-1]
			if conflict {
				if call.KeyArg != "" || call.ResultSummary != "" {
					t.Fatalf("conflicting evidence survived: %+v", call)
				}
			} else if call.KeyArg != "git status" || call.ResultSummary == "" {
				t.Fatalf("equivalent evidence lost: %+v", call)
			}
			transcript, err := ReadTranscript(bytes.NewReader(raw))
			if err != nil {
				t.Fatal(err)
			}
			encoded, _ := json.Marshal(transcript.Records)
			if bytes.Contains(encoded, []byte("git status")) == conflict {
				t.Fatalf("unexpected extraction records for conflict=%t", conflict)
			}
		})
	}
}
