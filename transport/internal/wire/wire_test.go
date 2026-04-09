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
