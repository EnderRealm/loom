// Package wire defines the protocol between loom-shipper and loom-receiver.
// Both sides import this package so the contract is defined in exactly one place.
package wire

import "fmt"

// IngestRequest is the payload the shipper POSTs to the receiver for each
// session delta. FromOffset/ToOffset are byte offsets into the source JSONL
// file; the (SessionID, FromOffset) pair is the idempotency key.
//
// Project is the legacy slug-encoded path segment used as the storage
// directory; ProjectIdentity carries the authoritative identity fields
// (git remote, raw cwd) that downstream readers use to group sessions
// across machines. ProjectIdentity is optional for backward compatibility
// — a request from a pre-identity client just leaves it zero.
type IngestRequest struct {
	Agent      string   `json:"agent"`
	Project    string   `json:"project"`
	SessionID  string   `json:"session_id"`
	FromOffset int64    `json:"from_offset"`
	ToOffset   int64    `json:"to_offset"`
	Lines      []string `json:"lines"`

	ProjectIdentity *ProjectIdentity `json:"project_identity,omitempty"`
}

// ProjectIdentity carries the authoritative identity for a session's
// project. GitRemote is the canonical handle when present (origin URL,
// normalized); Cwd is the raw, unsanitized working directory the agent
// reported; RootSlug echoes IngestRequest.Project so the receiver can
// re-derive the same value without parsing the slug.
type ProjectIdentity struct {
	GitRemote string `json:"git_remote,omitempty"`
	Cwd       string `json:"cwd,omitempty"`
	RootSlug  string `json:"root_slug,omitempty"`
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
