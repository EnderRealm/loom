// Package drift holds the Unknown-record vocabulary the Claude and Codex
// parsers share for a transcript that has moved ahead of what they model. Both
// count such drift into the summary rather than failing the file, and a reader
// of either parser must see the same contract, so the markers and the subtype
// composition live here rather than being copied into each.
package drift

import (
	"encoding/json"
	"errors"
	"regexp"
)

// MalformedLineMarker is bumped into Unknown when a single line fails to
// decode. Parsing continues so partial corruption doesn't lose the rest of
// the session.
const MalformedLineMarker = "__malformed__"

// UnmodeledPayloadMarker is bumped into Unknown as the subtype of the
// record's own type when the line is valid JSON but its payload does not fit
// our structs — the producer changed a shape we model (codex-cli 0.153.4
// turned session_meta.source from a string into an object). Distinct from
// MalformedLineMarker, which occupies the type slot because a line that fails
// to decode names no record type, so the unknown_records table separates "we
// are behind the producer", which is actionable drift, from "this line is
// corrupt".
//
// The subtype composes as "__unmodeled_payload__:<field>" when the decoder
// names the field that drifted — session_meta::__unmodeled_payload__:source —
// and is the bare marker when it does not: a syntax error, or a type error with
// no field path. Since the parse no longer fails, that field name is the only
// trace of which shape moved.
const UnmodeledPayloadMarker = "__unmodeled_payload__"

// unmodeledFieldMax bounds the field name composed into the subtype. Real
// paths are short ("source", "git.branch"); the cap exists to keep an absurd
// one out of the column, not to fit any of them.
const unmodeledFieldMax = 64

// unmodeledFieldDisallowed matches every rune outside the conservative name
// grammar the knowledge store already holds its interpolated fields to
// (logFieldDisallowed, internal/tui/candidate.go). A decoder field path is
// usually one of our own struct tags, but a map key decoded from the transcript
// can reach it, and transcript text is data with no authority over what renders
// it (docs/transcript-trust-and-redaction.md).
var unmodeledFieldDisallowed = regexp.MustCompile(`[^A-Za-z0-9._-]`)

// UnmodeledSubtype names the drifted field in the subtype when the decoder
// identified one and the name fits the grammar above. A name that does not fit
// degrades to the bare marker rather than being rewritten or truncated: a
// subtype is a grouping key, so a mangled one would read as a field that
// nothing actually drifted on.
func UnmodeledSubtype(err error) string {
	var te *json.UnmarshalTypeError
	if !errors.As(err, &te) || te.Field == "" {
		return UnmodeledPayloadMarker
	}
	if len(te.Field) > unmodeledFieldMax ||
		unmodeledFieldDisallowed.MatchString(te.Field) {
		return UnmodeledPayloadMarker
	}
	return UnmodeledPayloadMarker + ":" + te.Field
}
