package receiver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"loom/transport/internal/wire"
)

func TestSafeComponent(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{name: "uuid session id", in: "195f819e-1e11-4e08-8c16-a340f512f892", want: true},
		{name: "agent", in: "claude-code", want: true},
		{name: "project slug", in: "-Users-steve-code-loom", want: true},
		{name: "empty", in: "", want: false},
		{name: "dot", in: ".", want: false},
		{name: "dotdot", in: "..", want: false},
		{name: "embedded traversal", in: "a..b", want: false},
		{name: "separator", in: "a/b", want: false},
		{name: "windows separator", in: `a\b`, want: false},
		// Control characters would land in the <session_id>.jsonl filename
		// and let a shipper forge log lines in the receiver's own audit log.
		{name: "newline", in: "abc\ndef", want: false},
		{name: "carriage return", in: "abc\rdef", want: false},
		{name: "ansi escape", in: "abc\x1b[31mdef", want: false},
		{name: "nul", in: "abc\x00def", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := safeComponent(tc.in); got != tc.want {
				t.Fatalf("safeComponent(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestIngestRejectsControlCharactersInIdentifiers(t *testing.T) {
	storage := t.TempDir()
	srv := &server{storage: storage}

	body, err := json.Marshal(wire.IngestRequest{
		Agent:      "claude-code",
		Project:    "proj",
		SessionID:  "s1\nskip claude-code/s2: extracted",
		FromOffset: 0,
		ToOffset:   3,
		Lines:      []string{"{}"},
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/ingest", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.handleIngest(w, req)

	if w.Code < 400 {
		t.Fatalf("status = %d, want a rejection", w.Code)
	}
	entries, err := os.ReadDir(storage)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("storage holds %v, want nothing written for a rejected request", entries)
	}
}
