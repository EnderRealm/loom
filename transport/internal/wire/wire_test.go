package wire

import (
	"encoding/json"
	"testing"
)

func TestIngestErrorZeroExpectedFrom(t *testing.T) {
	zero := int64(0)

	// Zero value must appear in JSON (not omitted).
	e := IngestError{Error: "gap", ExpectedFrom: &zero}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	if got != `{"error":"gap","expected_from":0}` {
		t.Fatalf("zero expected_from missing or wrong: %s", got)
	}

	// Round-trip: decode must recover the pointer.
	var decoded IngestError
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.ExpectedFrom == nil {
		t.Fatal("ExpectedFrom is nil after decode")
	}
	if *decoded.ExpectedFrom != 0 {
		t.Fatalf("ExpectedFrom = %d, want 0", *decoded.ExpectedFrom)
	}
}

func TestIngestErrorNilExpectedFrom(t *testing.T) {
	// Nil pointer: field should be null in JSON.
	e := IngestError{Error: "other"}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	if got != `{"error":"other","expected_from":null}` {
		t.Fatalf("nil expected_from encoding wrong: %s", got)
	}

	// Decode: pointer stays nil.
	var decoded IngestError
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.ExpectedFrom != nil {
		t.Fatalf("ExpectedFrom should be nil, got %d", *decoded.ExpectedFrom)
	}
}

// TestIngestRequestLegacyClient checks that a payload from a pre-identity
// client (no project_identity field) round-trips cleanly.
func TestIngestRequestLegacyClient(t *testing.T) {
	legacy := []byte(`{"agent":"claude-code","project":"-tmp-foo","session_id":"abc","from_offset":0,"to_offset":10,"lines":["x"]}`)
	var req IngestRequest
	if err := json.Unmarshal(legacy, &req); err != nil {
		t.Fatal(err)
	}
	if req.ProjectIdentity != nil {
		t.Errorf("ProjectIdentity should be nil for legacy payload, got %+v", req.ProjectIdentity)
	}
	if req.Project != "-tmp-foo" {
		t.Errorf("Project = %q, want %q", req.Project, "-tmp-foo")
	}

	// Marshal a request without identity and confirm the field is omitted.
	out, err := json.Marshal(IngestRequest{Agent: "a", Project: "p", SessionID: "s"})
	if err != nil {
		t.Fatal(err)
	}
	if string(out) == "" {
		t.Fatal("empty marshal")
	}
	if got := string(out); got != `{"agent":"a","project":"p","session_id":"s","from_offset":0,"to_offset":0,"lines":null}` {
		t.Fatalf("unexpected marshal: %s", got)
	}
}

// TestIngestRequestWithIdentity round-trips an identity-bearing payload.
func TestIngestRequestWithIdentity(t *testing.T) {
	req := IngestRequest{
		Agent:     "claude-code",
		Project:   "-Users-steve-code-loom",
		SessionID: "abc",
		ProjectIdentity: &ProjectIdentity{
			GitRemote: "git@github.com:EnderRealm/loom.git",
			Cwd:       "/Users/steve/code/loom",
			RootSlug:  "-Users-steve-code-loom",
		},
	}
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var decoded IngestRequest
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.ProjectIdentity == nil {
		t.Fatal("ProjectIdentity nil after decode")
	}
	if decoded.ProjectIdentity.GitRemote != req.ProjectIdentity.GitRemote {
		t.Errorf("GitRemote = %q, want %q", decoded.ProjectIdentity.GitRemote, req.ProjectIdentity.GitRemote)
	}
	if decoded.ProjectIdentity.Cwd != req.ProjectIdentity.Cwd {
		t.Errorf("Cwd = %q, want %q", decoded.ProjectIdentity.Cwd, req.ProjectIdentity.Cwd)
	}
}

func TestResyncErrorMessage(t *testing.T) {
	e := &ResyncError{ExpectedFrom: 0, Detail: "gap: server expected lower from_offset"}
	msg := e.Error()
	if msg == "" {
		t.Fatal("empty error message")
	}
	// Should contain the offset value.
	if got := msg; got != "resync needed: server expects offset 0 (gap: server expected lower from_offset)" {
		t.Fatalf("unexpected message: %s", got)
	}
}
