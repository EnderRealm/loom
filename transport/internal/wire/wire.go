// Package wire defines the protocol between loom-shipper and loom-receiver.
// Both sides import this package so the contract is defined in exactly one place.
package wire

import "fmt"

// IngestRequest is the payload the shipper POSTs to the receiver for each
// session delta. FromOffset/ToOffset are byte offsets into the source JSONL
// file; the (SessionID, FromOffset) pair is the idempotency key.
type IngestRequest struct {
	Agent      string   `json:"agent"`
	Project    string   `json:"project"`
	SessionID  string   `json:"session_id"`
	FromOffset int64    `json:"from_offset"`
	ToOffset   int64    `json:"to_offset"`
	Lines      []string `json:"lines"`
}

// IngestResponse tells the shipper which offset the server has accepted up to.
// The shipper writes this value (not its own math) as the new cursor, so a
// partial or replayed batch converges on the next tick.
type IngestResponse struct {
	AcceptedToOffset int64 `json:"accepted_to_offset"`
}

// IngestError is returned with 4xx responses so the client can distinguish
// a real failure from a recoverable state mismatch.
type IngestError struct {
	Error        string `json:"error"`
	ExpectedFrom *int64 `json:"expected_from"`
}

// ResyncError wraps a 409 conflict from the receiver. The shipper should
// reset its cursor to ExpectedFrom and retry on the next tick.
type ResyncError struct {
	ExpectedFrom int64
	Detail       string
}

func (e *ResyncError) Error() string {
	return fmt.Sprintf("resync needed: server expects offset %d (%s)", e.ExpectedFrom, e.Detail)
}
