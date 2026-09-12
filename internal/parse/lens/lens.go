// Package lens extracts review-lens verdict blocks from transcript text. The
// same fenced ```json shape reaches a transcript by several routes — a Claude
// task notification, a subagent's tool result, a codex-lens.sh call's output,
// a Codex inlined pass — so one extractor serves every parser and every test,
// and what it recovers from a block that does not parse is decided once.
package lens

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

// Lens names a verdict can carry.
const (
	Contract = "contract"
	Quality  = "quality"
	Security = "security"
)

// Status values of a Block.
const (
	StatusParsed    = "parsed"
	StatusMalformed = "malformed"
)

// Reason values of a malformed Block.
const (
	ReasonUnterminated    = "unterminated"
	ReasonInvalidJSON     = "invalid_json"
	ReasonNotObject       = "not_object"
	ReasonUnknownLens     = "unknown_lens"
	ReasonUnknownVerdict  = "unknown_verdict"
	ReasonInvalidContext  = "invalid_context"
	ReasonInvalidCriteria = "invalid_criteria"
	ReasonInvalidFindings = "invalid_findings"
)

// ContextKind values: structured when the block carries a context object,
// historical when it predates that field and reports its context in prose.
const (
	ContextStructured = "structured"
	ContextHistorical = "historical"
)

// StateContaminated is the context.state a lens reports when its envelope
// carried what its Input section bans.
const StateContaminated = "contaminated"

// KnownLenses and KnownVerdicts are the values a real lens answer carries. A
// block that misses them is malformed rather than parsed — as a metric or as
// fan-out evidence — because a transcript can hold anything: a quoted verdict
// template, a junk block, prose about a report. Evidence a transcript's own
// content can forge is not evidence. KnownStates, knownStatuses and
// knownSeverities are the verdict schema's other enums, held to the same rule
// where a block supplies the field.
var (
	KnownLenses     = map[string]bool{Contract: true, Quality: true, Security: true}
	KnownVerdicts   = map[string]bool{"satisfied": true, "findings": true}
	KnownStates     = map[string]bool{"clean": true, "shared": true, StateContaminated: true}
	knownStatuses   = map[string]bool{"pass": true, "fail": true, "unverified": true}
	knownSeverities = map[string]bool{"must_fix": true, "defer": true, "suggestion": true}
)

const (
	jsonFence = "```json"
	fence     = "```"
)

// Context is a verdict's structured report of what its review context held.
type Context struct {
	State    string   `json:"state"`
	Received []string `json:"received"`
}

// Criterion and Finding are the verdict schema's array items, typed as far as
// the normalized store reads them. A block whose arrays do not decode to these
// shapes is malformed: its raw JSON is kept, and nothing is counted from it.
type Criterion struct {
	ID       string `json:"id"`
	Text     string `json:"text"`
	Status   string `json:"status"`
	Evidence string `json:"evidence"`
}

type Finding struct {
	File        string  `json:"file"`
	Line        *int    `json:"line"`
	Severity    string  `json:"severity"`
	Category    string  `json:"category"`
	Criterion   *string `json:"criterion"`
	Description string  `json:"description"`
	Fix         string  `json:"fix"`
}

// Block is one fenced json block naming a lens, whole. Raw is the fenced body
// untruncated; the typed fields are what it decoded to, and are recovered by
// regex where a malformed block still carries them before the cut.
type Block struct {
	// Ordinal is the block's index among the lens blocks in its text; Offset
	// is where its opening fence sits in that text, so a reader can place it
	// against other marks in the same text. Neither is content.
	Ordinal int
	Offset  int
	Raw     string
	// Unterminated is true when no closing fence followed the block: the
	// text was cut mid-block.
	Unterminated bool

	Lens     string
	Verdict  string
	Summary  string
	Criteria json.RawMessage
	Findings json.RawMessage
	// Context is nil when the block carries no valid context object.
	Context *Context

	// Status is parsed when the body is a JSON object naming a known lens
	// and a known verdict whose context, criteria and findings, where
	// supplied, fit the verdict schema; else malformed with Reason saying
	// why.
	Status string
	Reason string
	// ContextKind is structured when Context is set, else historical: a
	// block that predates the context field, or one cut or invalid before
	// its context could be read.
	ContextKind string
}

var (
	lensFieldRe    = regexp.MustCompile(`"lens"\s*:\s*"([^"]*)"`)
	summaryFieldRe = regexp.MustCompile(`"summary"\s*:\s*"((?:[^"\\]|\\.)*)"`)
)

// Extract returns every fenced ```json block in text whose body names a lens
// field, in order. Blocks that do not parse are returned malformed rather than
// dropped: a malformed response is evidence of what the orchestrator saw.
func Extract(text string) []Block {
	var out []Block
	rest := text
	for {
		i := strings.Index(rest, jsonFence)
		if i < 0 {
			return out
		}
		offset := len(text) - len(rest) + i
		body := rest[i+len(jsonFence):]
		unterminated := false
		if end := strings.Index(body, fence); end >= 0 {
			rest = body[end+len(fence):]
			body = body[:end]
		} else {
			// No closing fence: the text was cut mid-block. Read what is
			// here and stop.
			rest = ""
			unterminated = true
		}
		if strings.Contains(body, `"lens"`) {
			b := parse(strings.TrimSpace(body))
			b.Ordinal = len(out)
			b.Offset = offset
			b.Unterminated = unterminated
			if unterminated {
				b.Status, b.Reason = StatusMalformed, ReasonUnterminated
			}
			out = append(out, b)
		}
		if rest == "" {
			return out
		}
	}
}

// parse reads one fenced body. A block that is not a JSON object is still
// mined by regex for the lens and summary that precede the cut; criteria are
// deliberately not recovered from such a block, since a partial list would
// under-count. Fields are read one at a time off the object, and the lens,
// verdict and summary are kept whatever the rest decodes to, so a malformed
// block still says what it was.
func parse(body string) Block {
	b := Block{Raw: body, Status: StatusParsed, ContextKind: ContextHistorical}
	if !json.Valid([]byte(body)) {
		b.Status, b.Reason = StatusMalformed, ReasonInvalidJSON
		if m := lensFieldRe.FindStringSubmatch(body); m != nil {
			b.Lens = m[1]
		}
		if m := summaryFieldRe.FindStringSubmatch(body); m != nil {
			if s, err := strconv.Unquote(`"` + m[1] + `"`); err == nil {
				b.Summary = s
			} else {
				b.Summary = m[1]
			}
		}
		return b
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &obj); err != nil {
		b.Status, b.Reason = StatusMalformed, ReasonNotObject
		return b
	}
	b.Lens = stringField(obj["lens"])
	b.Verdict = stringField(obj["verdict"])
	b.Summary = stringField(obj["summary"])
	b.Criteria, b.Findings = obj["criteria"], obj["findings"]
	ctx, ctxOK := readContext(obj)
	if ctx != nil {
		b.Context = ctx
		b.ContextKind = ContextStructured
	}
	switch {
	case !KnownLenses[b.Lens]:
		b.Status, b.Reason = StatusMalformed, ReasonUnknownLens
	case !KnownVerdicts[b.Verdict]:
		b.Status, b.Reason = StatusMalformed, ReasonUnknownVerdict
	case !ctxOK:
		b.Status, b.Reason = StatusMalformed, ReasonInvalidContext
	case !validCriteria(b.Criteria):
		b.Status, b.Reason = StatusMalformed, ReasonInvalidCriteria
	case !validFindings(b.Findings):
		b.Status, b.Reason = StatusMalformed, ReasonInvalidFindings
	}
	return b
}

// readContext reads a block's context field: nil and true where the block
// predates the field and reports its context in prose, the decoded object
// where it supplies one, and false where what it supplies is not a context
// object carrying both a known state and a received array of strings. The
// fields are read off the object one at a time rather than decoded into
// Context, which would take a missing or null received for an empty one.
func readContext(obj map[string]json.RawMessage) (*Context, bool) {
	raw, ok := obj["context"]
	if !ok {
		return nil, true
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return nil, false
	}
	ctx := Context{State: stringField(fields["state"])}
	if !KnownStates[ctx.State] || !isArray(fields["received"]) || json.Unmarshal(fields["received"], &ctx.Received) != nil {
		return nil, false
	}
	return &ctx, true
}

// isArray reports whether a supplied field is a JSON array. An absent field
// is not one, and neither is null, which a slice would decode from as empty.
func isArray(raw json.RawMessage) bool {
	s := strings.TrimSpace(string(raw))
	return strings.HasPrefix(s, "[")
}

// validCriteria reports whether a supplied criteria array decodes to the
// schema's items with a known status. An absent array is not supplied; a
// supplied one must be an array, so null does not pass as empty.
func validCriteria(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return true
	}
	var cs []Criterion
	if !isArray(raw) || json.Unmarshal(raw, &cs) != nil {
		return false
	}
	for _, c := range cs {
		if !knownStatuses[c.Status] {
			return false
		}
	}
	return true
}

// validFindings reports whether a supplied findings array decodes to the
// schema's items with a known severity, on the same terms as validCriteria.
func validFindings(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return true
	}
	var fs []Finding
	if !isArray(raw) || json.Unmarshal(raw, &fs) != nil {
		return false
	}
	for _, f := range fs {
		if !knownSeverities[f.Severity] {
			return false
		}
	}
	return true
}

func stringField(raw json.RawMessage) string {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
}

// contaminationRe matches a lens summary that reports contaminated review
// context. Matched against verdict summaries only: the merge text a run writes
// says "no contamination reports" routinely, and matching raw transcript would
// count every one of those.
var contaminationRe = regexp.MustCompile(`(?i)contaminat|forbidden input|shared context|context was shared`)

// contaminationNegatedRe matches the phrasings a clean lens uses — "no
// contamination", "not contaminated", "no shared context". They carry the same
// stems as a real report, so they are excluded rather than counted.
//
// The gap is bounded at clause punctuation as well as at the sentence end: a
// negation of something else — "no must_fix findings, but the context was
// shared" — must not swallow the report in the next clause.
var contaminationNegatedRe = regexp.MustCompile(`(?i)\b(?:no|not|never|without|zero)\b[^.;,:]{0,40}?(?:contaminat|forbidden input|shared context|context was shared)`)

// ReportsContamination reports whether one lens summary says the review
// context was contaminated. This is the historical reading, for a verdict that
// predates the structured context field.
func ReportsContamination(s string) bool {
	return contaminationRe.MatchString(s) && !contaminationNegatedRe.MatchString(s)
}

// Contaminated reports whether a block says its review context was
// contaminated: the structured state when the block carries one, else the
// prose reading of its summary.
func Contaminated(b Block) bool {
	if b.Context != nil {
		return b.Context.State == StateContaminated
	}
	return ReportsContamination(b.Summary)
}
