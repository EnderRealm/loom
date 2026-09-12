package workreport

import (
	"regexp"
	"strconv"
	"strings"

	"loom/internal/parse/lens"
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

// harnessEnvelopeTags open the user messages a harness writes on the human's
// behalf. A message starting with one of them is not a human interaction.
var harnessEnvelopeTags = []string{
	// Slash-command envelope; either tag comes first in the wild.
	"<command-name>", "<command-message>",
	// Background task notification the harness posts as a user turn.
	"<task-notification>",
	// Output of a local command, written by the harness.
	"<local-command-stdout>", "<local-command-caveat>", "<bash-stdout>", "<bash-stderr>",
	// Codex's expanded skill invocation: the injected skill body with the
	// typed `$skill` line trailing it — the Codex counterpart of the
	// slash-command envelope.
	skillOpen,
	// Codex harness preamble.
	"<recommended_plugins>", "<environment_context>",
}

// humanInteraction reports whether a turn's stored user message is the human
// acting, as opposed to the harness writing on the user side of the transcript.
// It is judged on the turns table's user_message column, which both parsers
// fill the same way, so the rule is identical for Claude and Codex by
// construction.
//
// The rule: strip any leading system-reminder blocks and trim; an empty
// message is not an interaction; a message that then starts with one of
// harnessEnvelopeTags is not an interaction — a slash-command envelope, a
// background task notification, local command output, Codex's injected skill
// body, or Codex's preamble; a message invocation recognizes as /work is not
// an interaction whatever its form; anything else is. <bash-input> counts: the
// human ran a `!` command.
//
// The invocation is excluded by shape rather than by tag because a run's span
// starts at its invocation turn on every runtime: Claude's envelope and Codex's
// skill block already miss on the tags, but Codex's typed `#work` carries none,
// and counting it would add one to every Codex-typed run and none to a Claude
// run.
//
// Two exclusions are decided before a turn exists and so never reach this
// rule: a tool result never opens a turn in either parser (it attaches to the
// tool call it answers), and Claude's injected skill body is an isMeta user
// record the parser drops. What is left for text to decide is decided here.
func humanInteraction(userMessage string) bool {
	msg := strings.TrimSpace(stripReminders(userMessage))
	if msg == "" {
		return false
	}
	if _, ok := invocation(msg); ok {
		return false
	}
	for _, tag := range harnessEnvelopeTags {
		if strings.HasPrefix(msg, tag) {
			return false
		}
	}
	return true
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
// The rest of the line is the lenses the run committed to. Case-insensitive:
// the same line at the start of a sentence is capitalized.
var dispatchLineRe = regexp.MustCompile(`(?i)dispatching \(([^)\n]*)\)\s*:([^\n]*)`)

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

// commitment is one commitment line as the attempt model reads it: the round
// it named, the lenses it committed to, and where it sits in its text. A line
// whose round cannot be read places nothing and is skipped there; the
// compliance side already fails closed on it through dispatchLines.unparseable.
type commitment struct {
	round  int
	lenses []string
	offset int
}

// commitments reads every commitment line in one piece of assistant text, in
// order, dropping the ones naming no parseable round.
func commitments(text string) []commitment {
	var out []commitment
	for _, m := range dispatchLineRe.FindAllStringSubmatchIndex(text, -1) {
		r := roundRe.FindStringSubmatch(text[m[2]:m[3]])
		if r == nil {
			continue
		}
		n, err := strconv.Atoi(r[1])
		if err != nil {
			continue
		}
		c := commitment{round: n, offset: m[0]}
		tail := strings.ToLower(text[m[4]:m[5]])
		for _, name := range lensOrder {
			if strings.Contains(tail, name) {
				c.lenses = append(c.lenses, name)
			}
		}
		out = append(out, c)
	}
	return out
}

// lensOrder is the order lenses are reported in, which is the order a run
// dispatches them.
var lensOrder = []string{lens.Contract, lens.Quality, lens.Security}

const statusUnverif = "unverified"

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
