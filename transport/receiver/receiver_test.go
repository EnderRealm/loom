package receiver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

// ingest drives one request through the handler and returns the recorder.
func ingest(t *testing.T, srv *server, req wire.IngestRequest) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/v1/ingest", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.handleIngest(w, r)
	return w
}

func subagentRequest(sessionID string, from, to int64, lines []string) wire.IngestRequest {
	return wire.IngestRequest{
		Agent:      "claude-code",
		Project:    "-Users-steve-code-loom",
		SessionID:  sessionID,
		FromOffset: from,
		ToOffset:   to,
		Lines:      lines,
		Subagent: &wire.Subagent{
			ParentSessionID: "195f819e-1e11-4e08-8c16-a340f512f892",
			AgentType:       "reviewer",
			Description:     "Contract lens round 3",
			ToolUseID:       "toolu_015BC3bRz7V19vyVDAMXf5FX",
			SpawnDepth:      1,
		},
	}
}

// A subagent transcript lands under its parent, not beside it, and its
// dispatch metadata lands with it.
func TestIngestSubagentPathAndSidecar(t *testing.T) {
	storage := t.TempDir()
	srv := &server{storage: storage}

	w := ingest(t, srv, subagentRequest("agent-a0e0c89b977fd6273", 0, 3, []string{"{}"}))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %q", w.Code, w.Body.String())
	}

	dir := filepath.Join(storage, "claude-code", "-Users-steve-code-loom",
		"195f819e-1e11-4e08-8c16-a340f512f892", "subagents")
	data, err := os.ReadFile(filepath.Join(dir, "agent-a0e0c89b977fd6273.jsonl"))
	if err != nil {
		t.Fatalf("read subagent transcript: %v", err)
	}
	if string(data) != "{}\n" {
		t.Errorf("transcript = %q, want %q", data, "{}\n")
	}
	off, err := os.ReadFile(filepath.Join(dir, "agent-a0e0c89b977fd6273.offset"))
	if err != nil {
		t.Fatalf("read offset: %v", err)
	}
	if string(off) != "3" {
		t.Errorf("offset = %q, want %q", off, "3")
	}

	meta, err := os.ReadFile(filepath.Join(dir, "agent-a0e0c89b977fd6273.subagent.json"))
	if err != nil {
		t.Fatalf("read subagent sidecar: %v", err)
	}
	var sub wire.Subagent
	if err := json.Unmarshal(meta, &sub); err != nil {
		t.Fatal(err)
	}
	want := *subagentRequest("x", 0, 0, nil).Subagent
	if sub != want {
		t.Errorf("sidecar = %+v, want %+v", sub, want)
	}
}

// A subagent's parent id names a directory, so it goes through the same
// guard as every other identifier — nothing written on rejection.
func TestIngestRejectsUnsafeParentSessionID(t *testing.T) {
	cases := []struct {
		name   string
		parent string
	}{
		{name: "empty", parent: ""},
		{name: "traversal", parent: "../../../../etc"},
		{name: "dotdot", parent: ".."},
		{name: "separator", parent: "a/b"},
		{name: "control character", parent: "s1\nskip claude-code/s2: extracted"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			storage := t.TempDir()
			srv := &server{storage: storage}

			req := subagentRequest("agent-a0e0c89b977fd6273", 0, 3, []string{"{}"})
			req.Subagent.ParentSessionID = tc.parent
			w := ingest(t, srv, req)

			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", w.Code)
			}
			entries, err := os.ReadDir(storage)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatalf("storage holds %v, want nothing written for a rejected request", entries)
			}
		})
	}
}

// Delta shipping for a subagent behaves as it does for a session: the
// (session, from_offset) key makes a replay a no-op and a gap a 409.
func TestIngestSubagentDeltaAndReplay(t *testing.T) {
	storage := t.TempDir()
	srv := &server{storage: storage}
	const sid = "agent-a0e0c89b977fd6273"

	if w := ingest(t, srv, subagentRequest(sid, 0, 3, []string{"{}"})); w.Code != http.StatusOK {
		t.Fatalf("first batch: status = %d body = %q", w.Code, w.Body.String())
	}
	if w := ingest(t, srv, subagentRequest(sid, 3, 8, []string{`{"a":1}`})); w.Code != http.StatusOK {
		t.Fatalf("second batch: status = %d body = %q", w.Code, w.Body.String())
	}
	// Replay of the first batch converges rather than duplicating lines.
	w := ingest(t, srv, subagentRequest(sid, 0, 3, []string{"{}"}))
	if w.Code != http.StatusOK {
		t.Fatalf("replay: status = %d body = %q", w.Code, w.Body.String())
	}
	var resp wire.IngestResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.AcceptedToOffset != 8 {
		t.Errorf("accepted_to_offset = %d, want 8", resp.AcceptedToOffset)
	}
	// A gap must resync rather than write.
	if w := ingest(t, srv, subagentRequest(sid, 20, 25, []string{"{}"})); w.Code != http.StatusConflict {
		t.Fatalf("gap: status = %d, want 409", w.Code)
	}

	path := filepath.Join(storage, "claude-code", "-Users-steve-code-loom",
		"195f819e-1e11-4e08-8c16-a340f512f892", "subagents", sid+".jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "{}\n{\"a\":1}\n" {
		t.Errorf("transcript = %q, want the two batches exactly once", data)
	}
}

// A request with no subagent field must land byte-identically to today.
func TestIngestSessionPathUnchanged(t *testing.T) {
	storage := t.TempDir()
	srv := &server{storage: storage}

	w := ingest(t, srv, wire.IngestRequest{
		Agent:      "claude-code",
		Project:    "-Users-steve-code-loom",
		SessionID:  "195f819e-1e11-4e08-8c16-a340f512f892",
		FromOffset: 0,
		ToOffset:   3,
		Lines:      []string{"{}"},
		ProjectIdentity: &wire.ProjectIdentity{
			GitRemote: "git@github.com:EnderRealm/loom.git",
			Cwd:       "/Users/steve/code/loom",
			RootSlug:  "-Users-steve-code-loom",
		},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %q", w.Code, w.Body.String())
	}
	dir := filepath.Join(storage, "claude-code", "-Users-steve-code-loom")
	for _, name := range []string{
		"195f819e-1e11-4e08-8c16-a340f512f892.jsonl",
		"195f819e-1e11-4e08-8c16-a340f512f892.offset",
		"195f819e-1e11-4e08-8c16-a340f512f892.meta.json",
	} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("missing %s: %v", name, err)
		}
	}
}

// The execution-record registry ships under its own agent with the host as
// the project and no identity or subagent metadata; the receiver is generic
// over the agent segment, so it lands like any transcript.
func TestIngestExecutionRegistryLandsUnderItsAgent(t *testing.T) {
	storage := t.TempDir()
	srv := &server{storage: storage}

	line := `{"v":1,"kind":"run","run_id":"r1"}`
	w := ingest(t, srv, wire.IngestRequest{
		Agent:      "loom-executions",
		Project:    "steves-mbp_local",
		SessionID:  "executions",
		FromOffset: 0,
		ToOffset:   int64(len(line) + 1),
		Lines:      []string{line},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %q", w.Code, w.Body.String())
	}
	dir := filepath.Join(storage, "loom-executions", "steves-mbp_local")
	data, err := os.ReadFile(filepath.Join(dir, "executions.jsonl"))
	if err != nil {
		t.Fatalf("read registry: %v", err)
	}
	if string(data) != line+"\n" {
		t.Errorf("registry = %q, want %q", data, line+"\n")
	}
	if _, err := os.Stat(filepath.Join(dir, "executions.meta.json")); !os.IsNotExist(err) {
		t.Errorf("identity sidecar written for a registry with no project identity: %v", err)
	}
}
