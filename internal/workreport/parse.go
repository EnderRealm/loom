package workreport

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"

	"loom/internal/parse/summary"
)

// Runtime is the agent runtime a /work run executed on. The fan-out's shape
// differs per runtime — Claude dispatches real subagents, Codex and Cursor
// inline the lens passes — so a report that did not distinguish them would read
// every Codex run as non-compliant by construction.
type Runtime string

const (
	RuntimeClaude  Runtime = "claude"
	RuntimeCodex   Runtime = "codex"
	RuntimeCursor  Runtime = "cursor"
	RuntimeUnknown Runtime = "unknown"
)

// runtimeOf maps a summaries.db `agent` value onto a runtime. Anything the
// parser does not recognize is unknown, which alone disqualifies a run from
// being counted compliant.
func runtimeOf(agent string) Runtime {
	switch {
	case agent == string(summary.AgentClaude):
		return RuntimeClaude
	case agent == string(summary.AgentCodex):
		return RuntimeCodex
	case strings.Contains(agent, "cursor"):
		return RuntimeCursor
	default:
		return RuntimeUnknown
	}
}

const (
	claudeCommandMessage = "<command-message>work</command-message>"
	claudeCommandName    = "<command-name>/work</command-name>"
	commandArgsOpen      = "<command-args>"
	commandArgsClose     = "</command-args>"
	reminderOpen         = "<system-reminder>"
	reminderClose        = "</system-reminder>"
	skillOpen            = "<skill>"
	skillClose           = "</skill>"
	skillNameOpen        = "<name>"
	skillNameClose       = "</name>"
	workSkill            = "work"
)

// codexInvocationRe matches the typed Codex invocation, `#work <ticket>` or
// `$work`. `\b` keeps `#workaround` from reading as an invocation. Current
// rollouts record the expanded skill block instead — see codexSkillInvocation —
// and this form survives in the older ones.
var codexInvocationRe = regexp.MustCompile(`^[#$]work\b(?:[ \t]+(\S+))?`)

// invocation reports whether a turn's user message *is* a /work invocation, and
// the ticket id it named. Runs are identified this way rather than from a marker
// the run writes, so the report measures transcripts nobody instrumented.
//
// Every known form is tried whatever the runtime: an agent this parser cannot
// name still has its runs found and counted unknown, which is more honest than
// leaving them out of the report entirely.
//
// The forms are judged on what the message *starts* with, after any leading
// system-reminder blocks: a summarizer session quotes the whole /work skill body
// — tags included — mid-prompt, and a substring test would count every one of
// those as a run. The reminders are stripped once here so every form is judged
// against the same message.
func invocation(userMessage string) (ticket string, ok bool) {
	msg := strings.TrimSpace(stripReminders(userMessage))
	if ticket, ok := claudeInvocation(msg); ok {
		return ticket, true
	}
	if ticket, ok := codexSkillInvocation(msg); ok {
		return ticket, true
	}
	if m := codexInvocationRe.FindStringSubmatch(msg); m != nil {
		return m[1], true
	}
	return "", false
}

// codexSkillInvocation matches Codex's current form: the harness expands the
// skill into the user message, so the turn opens with a <skill> block naming the
// skill and carrying its whole body, and whatever the human typed follows it.
//
// A lens session gets the same skill body pushed at it mid-turn, so the <name>
// element is read at the head of the message rather than searched for.
func codexSkillInvocation(msg string) (string, bool) {
	rest := msg
	if !strings.HasPrefix(rest, skillOpen) {
		return "", false
	}
	rest = strings.TrimSpace(strings.TrimPrefix(rest, skillOpen))
	if !strings.HasPrefix(rest, skillNameOpen) {
		return "", false
	}
	rest = strings.TrimPrefix(rest, skillNameOpen)
	end := strings.Index(rest, skillNameClose)
	if end < 0 || strings.TrimSpace(rest[:end]) != workSkill {
		return "", false
	}
	// The typed invocation trails the expanded body, and carries the ticket id
	// when the human named one. Nothing trails a bare `#work`.
	body := rest[end+len(skillNameClose):]
	i := strings.Index(body, skillClose)
	if i < 0 {
		return "", true
	}
	if m := codexInvocationRe.FindStringSubmatch(strings.TrimSpace(body[i+len(skillClose):])); m != nil {
		return m[1], true
	}
	return "", true
}

func claudeInvocation(msg string) (string, bool) {
	rest := msg
	if !strings.HasPrefix(rest, claudeCommandMessage) {
		return "", false
	}
	rest = strings.TrimSpace(strings.TrimPrefix(rest, claudeCommandMessage))
	if !strings.HasPrefix(rest, claudeCommandName) {
		return "", false
	}
	// The args tag is optional: `/work` with no argument picks its own ticket.
	rest = strings.TrimSpace(strings.TrimPrefix(rest, claudeCommandName))
	if !strings.HasPrefix(rest, commandArgsOpen) {
		return "", true
	}
	rest = strings.TrimPrefix(rest, commandArgsOpen)
	end := strings.Index(rest, commandArgsClose)
	if end < 0 {
		return "", true
	}
	return strings.TrimSpace(rest[:end]), true
}

// stripReminders drops the system-reminder blocks the harness prepends to a
// user message, so the command tags can be tested for at the start.
func stripReminders(s string) string {
	for {
		s = strings.TrimSpace(s)
		if !strings.HasPrefix(s, reminderOpen) {
			return s
		}
		end := strings.Index(s, reminderClose)
		if end < 0 {
			return s
		}
		s = s[end+len(reminderClose):]
	}
}

// dispatchLineRe matches the fan-out commitment line both runtimes write, e.g.
// `dispatching (loom/foo-1234 round 2): contract, quality, security`. The
// parenthesized part is captured whole because it varies in the wild — the
// ticket id is sometimes absent and the final round carries a `, final` suffix.
// Case-insensitive: the same line at the start of a sentence is capitalized.
var dispatchLineRe = regexp.MustCompile(`(?i)dispatching \(([^)\n]*)\)\s*:`)

// roundRe pulls the round number out of a commitment line's parenthetical.
var roundRe = regexp.MustCompile(`(?i)round (\d+)`)

// dispatchLines is what a run's assistant text says about its fan-out: how many
// commitment lines it wrote, the highest round any of them named, and whether
// one of them named no parseable round at all. The last is a fail-closed signal:
// a run whose rounds cannot be counted is unknown, not compliant.
type dispatchLines struct {
	count       int
	maxRound    int
	unparseable bool
}

func (d *dispatchLines) scan(text string) {
	for _, m := range dispatchLineRe.FindAllStringSubmatch(text, -1) {
		d.count++
		r := roundRe.FindStringSubmatch(m[1])
		if r == nil {
			d.unparseable = true
			continue
		}
		n, err := strconv.Atoi(r[1])
		if err != nil {
			d.unparseable = true
			continue
		}
		if n > d.maxRound {
			d.maxRound = n
		}
	}
}

// verdict is one lens's structured answer, as parsed out of a fenced json block.
// parsed is false when the block was truncated (a lens verdict reaching us
// through a tool result is cut at 800 chars) and only the leading fields could
// be recovered.
type verdict struct {
	lens     string
	summary  string
	criteria []criterion
	parsed   bool
}

type criterion struct {
	Status string `json:"status"`
}

type verdictJSON struct {
	Lens     string      `json:"lens"`
	Verdict  string      `json:"verdict"`
	Summary  string      `json:"summary"`
	Criteria []criterion `json:"criteria"`
}

const (
	lensContract  = "contract"
	lensQuality   = "quality"
	lensSecurity  = "security"
	statusUnverif = "unverified"
	jsonFence     = "```json"
	fence         = "```"
)

// knownLenses and knownVerdicts are the values a real lens answer carries. A
// fenced block that misses them is not counted — as a metric or as fan-out
// evidence — because a transcript can hold anything: a quoted verdict template,
// a junk block, prose about this report. Evidence a transcript's own content can
// forge is not evidence.
var (
	knownLenses   = map[string]bool{lensContract: true, lensQuality: true, lensSecurity: true}
	knownVerdicts = map[string]bool{"satisfied": true, "findings": true}
)

var (
	lensFieldRe    = regexp.MustCompile(`"lens"\s*:\s*"([^"]*)"`)
	summaryFieldRe = regexp.MustCompile(`"summary"\s*:\s*"((?:[^"\\]|\\.)*)"`)
)

// parseVerdicts pulls every lens verdict out of one piece of transcript text.
// The same block shape appears in three places — a Claude task notification, a
// codex-lens tool result, and a Codex inlined pass — so one lenient parser
// serves all three. Blocks that do not parse to a plausible verdict are dropped
// rather than returned: see knownLenses.
func parseVerdicts(text string) []verdict {
	var out []verdict
	rest := text
	for {
		i := strings.Index(rest, jsonFence)
		if i < 0 {
			return out
		}
		body := rest[i+len(jsonFence):]
		if end := strings.Index(body, fence); end >= 0 {
			rest = body[end+len(fence):]
			body = body[:end]
		} else {
			// No closing fence: the text was truncated mid-block. Parse what
			// is here and stop.
			rest = ""
		}
		if !strings.Contains(body, `"lens"`) {
			if rest == "" {
				return out
			}
			continue
		}
		if v, ok := parseVerdictBody(body); ok {
			out = append(out, v)
		}
		if rest == "" {
			return out
		}
	}
}

// parseVerdictBody reads one fenced block, reporting whether it is a verdict at
// all. A whole block has to name a known lens and a known verdict; a truncated
// one is cut before its criteria and often before its verdict, so the lens alone
// stands for it.
func parseVerdictBody(body string) (verdict, bool) {
	var vj verdictJSON
	if err := json.Unmarshal([]byte(body), &vj); err == nil {
		if !knownLenses[vj.Lens] || !knownVerdicts[vj.Verdict] {
			return verdict{}, false
		}
		return verdict{lens: vj.Lens, summary: vj.Summary, criteria: vj.Criteria, parsed: true}, true
	}
	// Truncated block: recover the fields that precede the cut. Criteria are
	// deliberately not recovered — a partial list would under-count unverified
	// criteria, and a wrong count is worse than none.
	v := verdict{}
	if m := lensFieldRe.FindStringSubmatch(body); m != nil {
		v.lens = m[1]
	}
	if !knownLenses[v.lens] {
		return verdict{}, false
	}
	if m := summaryFieldRe.FindStringSubmatch(body); m != nil {
		if s, err := strconv.Unquote(`"` + m[1] + `"`); err == nil {
			v.summary = s
		} else {
			v.summary = m[1]
		}
	}
	return v, true
}

// contaminationRe matches a lens summary that reports contaminated review
// context. Matched against parsed verdict summaries only: the merge text a run
// writes says "no contamination reports" routinely, and matching raw transcript
// would count every one of those.
var contaminationRe = regexp.MustCompile(`(?i)contaminat|forbidden input|shared context|context was shared`)

// contaminationNegatedRe matches the phrasings a clean lens uses — "no
// contamination", "not contaminated", "no shared context". They carry the same
// stems as a real report, so they are excluded rather than counted.
//
// The gap is bounded at clause punctuation as well as at the sentence end: a
// negation of something else — "no must_fix findings, but the context was
// shared" — must not swallow the report in the next clause.
var contaminationNegatedRe = regexp.MustCompile(`(?i)\b(?:no|not|never|without|zero)\b[^.;,:]{0,40}?(?:contaminat|forbidden input|shared context|context was shared)`)

// reportsContamination reports whether one lens summary says the review context
// was contaminated.
func reportsContamination(s string) bool {
	return contaminationRe.MatchString(s) && !contaminationNegatedRe.MatchString(s)
}

// subjectNamesTicket reports whether a commit subject's leading `[<id>]` marker
// names this run's ticket. The run's id may be bare where the commit's is
// namespaced, so a project prefix is allowed.
func subjectNamesTicket(subject, ticket string) bool {
	if ticket == "" || !strings.HasPrefix(subject, "[") {
		return false
	}
	end := strings.Index(subject, "]")
	if end < 0 {
		return false
	}
	id := subject[1:end]
	return id == ticket || strings.HasSuffix(id, "/"+ticket)
}
