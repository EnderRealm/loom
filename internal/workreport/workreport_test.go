package workreport

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loom/internal/parse/summary"
	"loom/internal/summaries"
)

// base is the wall clock every fixture run hangs off, so the range tests have
// something stable to bound.
var base = time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)

// fixture is a summaries.db written the way the summarizer writes one: whole
// sessions folded through summaries.Store, never hand-inserted rows.
type fixture struct {
	t    *testing.T
	path string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	return &fixture{t: t, path: filepath.Join(t.TempDir(), "summaries.db")}
}

func (f *fixture) add(sum *summary.SessionSummary) {
	f.t.Helper()
	st, err := summaries.Open(f.path)
	if err != nil {
		f.t.Fatal(err)
	}
	defer st.Close()
	if err := st.WriteSummary(context.Background(), sum, summaries.SourceInfo{Project: "loom"}); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) load(since, until time.Time) *Report {
	f.t.Helper()
	rep, err := Load(f.path, since, until)
	if err != nil {
		f.t.Fatal(err)
	}
	return rep
}

// only asserts the report holds exactly one run and returns it.
func only(t *testing.T, rep *Report) Run {
	t.Helper()
	if len(rep.Runs) != 1 {
		t.Fatalf("report holds %d runs, want 1: %+v", len(rep.Runs), rep.Runs)
	}
	return rep.Runs[0]
}

// fenced wraps a verdict body in the json code fence every lens answers in.
func fenced(body string) string {
	return "```json\n" + body + "\n```"
}

// workInvocation is the prompt Claude's /work skill expands into.
func workInvocation(ticket string) string {
	return "<command-message>work</command-message>\n<command-name>/work</command-name>\n<command-args>" + ticket + "</command-args>"
}

// skillBlock is the block Codex's harness expands a skill invocation into: the
// name, the path, and the whole skill body.
func skillBlock(name string) string {
	return "<skill>\n<name>" + name + "</name>\n<path>/Users/steve/.codex/skills/" + name + "/SKILL.md</path>\n" +
		"---\nname: " + name + "\n---\n\n# Work: Orchestrate a Ticket to Done\n\n" +
		"Drive a ticket from `open` to `done` through an implement → review loop.\n</skill>"
}

// skillInvocation is that block with the typed invocation trailing it, which is
// where the ticket id survives.
func skillInvocation(ticket string) string {
	return skillBlock("work") + "\n#work " + ticket
}

// notification is the shape a Claude lens verdict comes back in: a task
// completion message on the user side of the transcript.
func notification(body string) string {
	return "<task-notification><result>" + fenced(body) + "</result></task-notification>"
}

// commitResult is git's confirmation line, which is where summaries.db's
// commits rows come from.
func commitResult(subject string) string {
	return "[main abc1234] " + subject + "\n 2 files changed, 20 insertions(+)"
}

// compliantSession is a Claude run that did the whole thing: two review rounds,
// lens subagents plus the routed security lens, a contract verdict leaving one
// criterion unverified, two ticket status edits, and a commit. Its verdicts
// arrive the asynchronous way, as task notifications on a later turn;
// syncLensSession covers the other delivery shape.
func compliantSession() *summary.SessionSummary {
	return &summary.SessionSummary{
		SessionID: "compliant",
		Agent:     summary.AgentClaude,
		StartTime: base,
		EndTime:   base.Add(time.Hour),
		Turns: []summary.Turn{
			{
				Idx:           0,
				UserMessage:   workInvocation("loom/compliant-1111"),
				AssistantText: "Plan is clear. Dispatching the coder.\ndispatching (loom/compliant-1111 round 1): contract, quality, security",
				StartedAt:     base,
			},
			{
				Idx: 1,
				UserMessage: notification(`{"lens": "contract", "verdict": "findings", "summary": "Two criteria not met.",
					"criteria": [{"id": "AC1", "status": "pass"}, {"id": "AC2", "status": "fail"}]}`),
				AssistantText: "Round 2 diff is ready.\ndispatching (loom/compliant-1111 round 2): contract, quality, security",
				StartedAt:     base.Add(10 * time.Minute),
			},
			{
				Idx: 2,
				UserMessage: notification(`{"lens": "contract", "verdict": "satisfied", "summary": "All criteria met.",
					"criteria": [{"id": "AC1", "status": "pass"}, {"id": "AC2", "status": "unverified"}]}`) +
					notification(`{"lens": "security", "verdict": "satisfied", "summary": "No injection or authz exposure found."}`),
				// The merge text says this routinely; it must not be read as a
				// contamination report.
				AssistantText: "All three lenses returned, no contamination reports. Committing.",
				StartedAt:     base.Add(30 * time.Minute),
			},
		},
		ToolCalls: []summary.ToolCall{
			{TurnIdx: 0, Kind: summary.KindMCP, ToolName: "mcp__tk__ticket_edit", KeyArg: "loom/compliant-1111", StartedAt: base.Add(time.Minute)},
			{TurnIdx: 0, Kind: summary.KindTask, ToolName: "Agent", KeyArg: "Contract lens review", StartedAt: base.Add(2 * time.Minute)},
			{TurnIdx: 0, Kind: summary.KindTask, ToolName: "Agent", KeyArg: "Quality lens review", StartedAt: base.Add(2 * time.Minute)},
			{TurnIdx: 0, Kind: summary.KindBash, ToolName: "Bash", KeyArg: "~/.claude/codex-lens.sh --lens security --payload /tmp/p", StartedAt: base.Add(3 * time.Minute)},
			{TurnIdx: 1, Kind: summary.KindTask, ToolName: "Agent", KeyArg: "Contract lens round 2", StartedAt: base.Add(12 * time.Minute)},
			{TurnIdx: 1, Kind: summary.KindTask, ToolName: "Agent", KeyArg: "Quality lens round 2", StartedAt: base.Add(12 * time.Minute)},
			{TurnIdx: 2, Kind: summary.KindBash, ToolName: "Bash", KeyArg: "git commit", StartedAt: base.Add(40 * time.Minute),
				ResultSummary: commitResult("[loom/compliant-1111] Do the thing")},
			{TurnIdx: 2, Kind: summary.KindMCP, ToolName: "mcp__tk__ticket_edit", KeyArg: "loom/compliant-1111", StartedAt: base.Add(31 * time.Minute)},
		},
	}
}

func TestCompliantRun(t *testing.T) {
	f := newFixture(t)
	f.add(compliantSession())

	run := only(t, f.load(time.Time{}, time.Time{}))
	if run.Classification != ClassCompliant {
		t.Fatalf("classification = %q, want compliant", run.Classification)
	}
	if run.Runtime != RuntimeClaude {
		t.Fatalf("runtime = %q, want claude", run.Runtime)
	}
	if run.Ticket != "loom/compliant-1111" {
		t.Fatalf("ticket = %q, want loom/compliant-1111", run.Ticket)
	}
	if !run.FanOutDispatched {
		t.Fatal("fan_out_dispatched = false, want true")
	}
	if run.ReviewIterations == nil || *run.ReviewIterations != 2 {
		t.Fatalf("review_iterations = %v, want 2", run.ReviewIterations)
	}
	if run.CriteriaUnverified == nil || *run.CriteriaUnverified != 1 {
		t.Fatalf("criteria_unverified = %v, want 1 (from the last contract verdict)", run.CriteriaUnverified)
	}
	if run.ContaminationReports != 0 {
		t.Fatalf("contamination_reports = %d, want 0 (the merge prose is not a verdict)", run.ContaminationReports)
	}
	if run.OpenToDoneMs == nil || *run.OpenToDoneMs != 30*60*1000 {
		t.Fatalf("open_to_done_ms = %v, want 1800000", run.OpenToDoneMs)
	}
	if !run.Committed {
		t.Fatal("committed = false, want true")
	}
	if run.InvokedAt != base.Format(time.RFC3339) {
		t.Fatalf("invoked_at = %q, want %q", run.InvokedAt, base.Format(time.RFC3339))
	}
}

func TestSubagentRowsNamedTaskAreDispatchEvidence(t *testing.T) {
	f := newFixture(t)
	// The same dispatch is recorded as "Task" by one client and "Agent" by
	// another; both normalize to the task kind, which is what is matched.
	sum := compliantSession()
	sum.SessionID = "task-named"
	for i := range sum.ToolCalls {
		if sum.ToolCalls[i].ToolName == "Agent" {
			sum.ToolCalls[i].ToolName = "Task"
		}
	}
	f.add(sum)

	run := only(t, f.load(time.Time{}, time.Time{}))
	if run.Classification != ClassCompliant {
		t.Fatalf("classification = %q, want compliant", run.Classification)
	}
	if !run.FanOutDispatched {
		t.Fatal("fan_out_dispatched = false, want true")
	}
}

// toolResultLimit mirrors the 800-char cut claudeparse applies to a tool
// result, which is what truncates a verdict that came back synchronously.
const toolResultLimit = 800

// truncatedContractVerdict is a contract verdict as it survives that cut: the
// criteria list runs past 800 chars, so only the leading fields are
// recoverable. The ellipsis is the one claudeparse appends after the cut.
func truncatedContractVerdict() string {
	var b strings.Builder
	b.WriteString(`{"lens": "contract", "verdict": "findings", ` +
		`"summary": "The lens was handed the coder's transcript, so its context was shared.", "criteria": [`)
	for i := 1; i <= 40; i++ {
		fmt.Fprintf(&b, `{"id": "AC%d", "status": "unverified"}, `, i)
	}
	b.WriteString(`{"id": "AC41", "status": "pass"}]}`)
	return fenced(b.String())[:toolResultLimit] + "…"
}

// syncLensSession is the other Claude delivery shape: the lens subagents were
// dispatched synchronously, so each verdict is that call's own tool result
// rather than a task notification on a later turn.
func syncLensSession() *summary.SessionSummary {
	return &summary.SessionSummary{
		SessionID: "sync-lenses",
		Agent:     summary.AgentClaude,
		StartTime: base,
		EndTime:   base.Add(time.Hour),
		Turns: []summary.Turn{{
			Idx:           0,
			UserMessage:   workInvocation("loom/sync-8888"),
			AssistantText: "Plan is clear.\ndispatching (loom/sync-8888 round 1): contract, quality, security",
			StartedAt:     base,
		}},
		ToolCalls: []summary.ToolCall{
			{TurnIdx: 0, Kind: summary.KindTask, ToolName: "Task", KeyArg: "Contract lens review", StartedAt: base.Add(2 * time.Minute),
				ResultSummary: truncatedContractVerdict()},
			{TurnIdx: 0, Kind: summary.KindTask, ToolName: "Task", KeyArg: "Quality lens review", StartedAt: base.Add(2 * time.Minute),
				ResultSummary: fenced(`{"lens": "quality", "verdict": "satisfied",
					"summary": "No contamination: the lens saw the diff and the ticket only."}`)},
			{TurnIdx: 0, Kind: summary.KindBash, ToolName: "Bash", KeyArg: "git commit", StartedAt: base.Add(10 * time.Minute),
				ResultSummary: commitResult("[loom/sync-8888] Do the thing")},
		},
	}
}

func TestSyncLensVerdictsAreReadOffToolResults(t *testing.T) {
	f := newFixture(t)
	f.add(syncLensSession())

	run := only(t, f.load(time.Time{}, time.Time{}))
	if run.Classification != ClassCompliant {
		t.Fatalf("classification = %q, want compliant", run.Classification)
	}
	// Recovered from the truncated block's summary field; the clean lens's
	// negated phrasing must not add a second.
	if run.ContaminationReports != 1 {
		t.Fatalf("contamination_reports = %d, want 1", run.ContaminationReports)
	}
	// The cut landed inside criteria[], so there is no whole contract verdict
	// to count. Null, never an undercount.
	if run.CriteriaUnverified != nil {
		t.Fatalf("criteria_unverified = %d, want null: a truncated block must not be counted", *run.CriteriaUnverified)
	}
}

func TestSkippedFanOutRun(t *testing.T) {
	f := newFixture(t)
	f.add(&summary.SessionSummary{
		SessionID: "skipped",
		Agent:     summary.AgentClaude,
		StartTime: base,
		EndTime:   base.Add(time.Hour),
		Turns: []summary.Turn{{
			Idx:           0,
			UserMessage:   workInvocation("loom/skipped-2222"),
			AssistantText: "Small change, implemented and committed.",
			StartedAt:     base,
		}},
		ToolCalls: []summary.ToolCall{
			{TurnIdx: 0, Kind: summary.KindEdit, ToolName: "Edit", KeyArg: "internal/thing.go", StartedAt: base.Add(time.Minute)},
			{TurnIdx: 0, Kind: summary.KindBash, ToolName: "Bash", KeyArg: "git commit", StartedAt: base.Add(5 * time.Minute),
				ResultSummary: commitResult("[loom/skipped-2222] Do the thing")},
		},
	})

	run := only(t, f.load(time.Time{}, time.Time{}))
	if run.Classification != ClassSkippedFanOut {
		t.Fatalf("classification = %q, want skipped_fanout", run.Classification)
	}
	if run.FanOutDispatched {
		t.Fatal("fan_out_dispatched = true, want false")
	}
	// The run wrote no commitment line, so there is no round count to report —
	// a zero would read as "the fan-out ran zero rounds".
	if run.ReviewIterations != nil {
		t.Fatalf("review_iterations = %d, want null", *run.ReviewIterations)
	}
	if run.CriteriaUnverified != nil {
		t.Fatalf("criteria_unverified = %v, want null (no contract verdict was parsed)", *run.CriteriaUnverified)
	}
	if run.OpenToDoneMs != nil {
		t.Fatalf("open_to_done_ms = %v, want null", *run.OpenToDoneMs)
	}
	if !run.Committed {
		t.Fatal("committed = false, want true")
	}
}

func TestUnclassifiableRunIsUnknownAndNeverCompliant(t *testing.T) {
	f := newFixture(t)
	// A run that talks like a compliant one but whose rounds cannot be counted.
	f.add(&summary.SessionSummary{
		SessionID: "unclear",
		Agent:     summary.AgentClaude,
		StartTime: base,
		EndTime:   base.Add(time.Hour),
		Turns: []summary.Turn{{
			Idx:           0,
			UserMessage:   workInvocation("loom/unclear-3333"),
			AssistantText: "dispatching (loom/unclear-3333 round N): contract, quality, security",
			StartedAt:     base,
		}},
		ToolCalls: []summary.ToolCall{
			{TurnIdx: 0, Kind: summary.KindTask, ToolName: "Agent", KeyArg: "Contract lens review", StartedAt: base.Add(time.Minute)},
			{TurnIdx: 0, Kind: summary.KindTask, ToolName: "Agent", KeyArg: "Quality lens review", StartedAt: base.Add(time.Minute)},
			{TurnIdx: 0, Kind: summary.KindBash, ToolName: "Bash", KeyArg: "git commit", StartedAt: base.Add(5 * time.Minute),
				ResultSummary: commitResult("[loom/unclear-3333] Do the thing")},
		},
	})

	rep := f.load(time.Time{}, time.Time{})
	run := only(t, rep)
	if run.Classification != ClassUnknown {
		t.Fatalf("classification = %q, want unknown", run.Classification)
	}
	if rep.Totals.Unknown != 1 || rep.Totals.Compliant != 0 {
		t.Fatalf("totals = %+v, want the run counted unknown and never compliant", rep.Totals)
	}
}

func TestInvocationWithNothingAfterItIsUnknown(t *testing.T) {
	f := newFixture(t)
	f.add(&summary.SessionSummary{
		SessionID: "empty",
		Agent:     summary.AgentClaude,
		StartTime: base,
		EndTime:   base.Add(time.Minute),
		Turns: []summary.Turn{{
			Idx:         0,
			UserMessage: workInvocation("loom/empty-4444"),
			StartedAt:   base,
		}},
	})

	if got := only(t, f.load(time.Time{}, time.Time{})).Classification; got != ClassUnknown {
		t.Fatalf("classification = %q, want unknown", got)
	}
}

func TestRunWithoutCommitOrFanOutIsIncomplete(t *testing.T) {
	f := newFixture(t)
	f.add(&summary.SessionSummary{
		SessionID: "blocked",
		Agent:     summary.AgentClaude,
		StartTime: base,
		EndTime:   base.Add(time.Hour),
		Turns: []summary.Turn{{
			Idx:           0,
			UserMessage:   workInvocation("loom/blocked-5555"),
			AssistantText: "The ticket names no success criteria. Stopping to ask rather than guessing.",
			StartedAt:     base,
		}},
	})

	if got := only(t, f.load(time.Time{}, time.Time{})).Classification; got != ClassIncomplete {
		t.Fatalf("classification = %q, want incomplete", got)
	}
}

// codexSession is the same compliance shape in Codex's form: no subagent rows
// at all, the lens passes inlined and routed through the lens script.
func codexSession() *summary.SessionSummary {
	return &summary.SessionSummary{
		SessionID: "codex",
		Agent:     summary.AgentCodex,
		StartTime: base,
		EndTime:   base.Add(time.Hour),
		Turns: []summary.Turn{{
			Idx:         0,
			UserMessage: "#work loom/codex-6666",
			AssistantText: "Reviewing as the three lenses.\ndispatching (loom/codex-6666 round 1): contract, quality, security\n" +
				fenced(`{"lens": "contract", "verdict": "satisfied", "summary": "All criteria met.",
					"criteria": [{"id": "AC1", "status": "pass"}]}`) + "\n" +
				fenced(`{"lens": "quality", "verdict": "findings", "summary": "The lens saw the coder's transcript, so its context was contaminated."}`),
			StartedAt: base,
		}},
		ToolCalls: []summary.ToolCall{
			{TurnIdx: 0, Kind: summary.KindCustom, ToolName: "exec", KeyArg: `const r = await tools.exec_command({"cmd":"/Users/steve/.codex/codex-lens.sh --lens security"})`, StartedAt: base.Add(time.Minute)},
			{TurnIdx: 0, Kind: summary.KindBash, ToolName: "exec_command", KeyArg: "git commit", StartedAt: base.Add(5 * time.Minute),
				ResultSummary: commitResult("[loom/codex-6666] Do the thing")},
		},
	}
}

func TestCodexRunIsCompliantWithoutSubagentRows(t *testing.T) {
	f := newFixture(t)
	f.add(codexSession())

	run := only(t, f.load(time.Time{}, time.Time{}))
	if run.Runtime != RuntimeCodex {
		t.Fatalf("runtime = %q, want codex", run.Runtime)
	}
	if run.Classification != ClassCompliant {
		t.Fatalf("classification = %q, want compliant: an inlined fan-out leaves no Agent rows", run.Classification)
	}
	if run.ContaminationReports != 1 {
		t.Fatalf("contamination_reports = %d, want 1", run.ContaminationReports)
	}
	if run.CriteriaUnverified == nil || *run.CriteriaUnverified != 0 {
		t.Fatalf("criteria_unverified = %v, want 0", run.CriteriaUnverified)
	}
}

func TestCodexInlinedPassesAreDispatchEvidenceOnTheirOwn(t *testing.T) {
	f := newFixture(t)
	sum := codexSession()
	// Drop the lens router call: the inlined verdict blocks are all that is
	// left, and they are this runtime's dispatch shape.
	sum.ToolCalls = sum.ToolCalls[1:]
	f.add(sum)

	if got := only(t, f.load(time.Time{}, time.Time{})).Classification; got != ClassCompliant {
		t.Fatalf("classification = %q, want compliant", got)
	}
}

func TestCodexSkillBlockIsAnInvocation(t *testing.T) {
	f := newFixture(t)
	sum := codexSession()
	sum.SessionID = "codex-skill"
	sum.Turns[0].UserMessage = skillInvocation("loom/codex-6666")
	f.add(sum)

	run := only(t, f.load(time.Time{}, time.Time{}))
	if run.Runtime != RuntimeCodex || run.Classification != ClassCompliant {
		t.Fatalf("run = %+v, want a compliant codex run", run)
	}
	if run.Ticket != "loom/codex-6666" {
		t.Fatalf("ticket = %q, want the one the typed invocation named", run.Ticket)
	}
}

func TestQuotedVerdictBlocksAreNotFanOutEvidence(t *testing.T) {
	f := newFixture(t)
	// A run that names a fan-out and quotes two blocks carrying a "lens" field,
	// with nothing behind either: transcript content the run wrote itself must
	// not be able to forge the evidence this report measures.
	f.add(&summary.SessionSummary{
		SessionID: "quoted-blocks",
		Agent:     summary.AgentCodex,
		StartTime: base,
		EndTime:   base.Add(time.Hour),
		Turns: []summary.Turn{{
			Idx:         0,
			UserMessage: "#work loom/quoted-9999",
			AssistantText: "The lenses answer in this shape:\n" +
				fenced(`{"lens": "<contract|quality|security>", "verdict": "<satisfied|findings>"}`) + "\n" +
				fenced(`{"lens": "tbd", "summary": "to be filled in"}`) + "\n" +
				"dispatching (loom/quoted-9999 round 1): contract, quality, security",
			StartedAt: base,
		}},
		ToolCalls: []summary.ToolCall{
			{TurnIdx: 0, Kind: summary.KindBash, ToolName: "exec_command", KeyArg: "git commit", StartedAt: base.Add(5 * time.Minute),
				ResultSummary: commitResult("[loom/quoted-9999] Do the thing")},
		},
	})

	run := only(t, f.load(time.Time{}, time.Time{}))
	if run.FanOutDispatched {
		t.Fatal("fan_out_dispatched = true: blocks that are not verdicts are not evidence")
	}
	if run.Classification == ClassCompliant {
		t.Fatal("classification = compliant off quoted blocks alone")
	}
}

func TestOneLensInlinedTwiceIsNotFanOutEvidence(t *testing.T) {
	f := newFixture(t)
	sum := codexSession()
	sum.SessionID = "one-lens-twice"
	// Two real verdict blocks, one lens: a merge re-quoting a verdict is not a
	// second pass.
	contract := fenced(`{"lens": "contract", "verdict": "satisfied", "summary": "All criteria met."}`)
	sum.Turns[0].AssistantText = "dispatching (loom/codex-6666 round 1): contract, quality, security\n" +
		contract + "\nand again in the merge:\n" + contract
	sum.ToolCalls = sum.ToolCalls[1:] // drop the router call; the blocks are all that is left
	f.add(sum)

	run := only(t, f.load(time.Time{}, time.Time{}))
	if run.FanOutDispatched {
		t.Fatal("fan_out_dispatched = true: one lens quoted twice is not two lenses")
	}
}

func TestEchoOfTheLensScriptIsNotRouterEvidence(t *testing.T) {
	f := newFixture(t)
	sum := codexSession()
	sum.SessionID = "echo"
	// One inlined lens is short of the inlined bar, so this run stands on the
	// router call alone — and an echo of the whole invocation, matching command
	// and all, still routed no lens: nothing came back.
	sum.Turns[0].AssistantText = "dispatching (loom/codex-6666 round 1): contract, quality, security\n" +
		fenced(`{"lens": "contract", "verdict": "satisfied", "summary": "All criteria met."}`)
	sum.ToolCalls[0].KeyArg = `echo "codex-lens.sh --lens security routes the security lens"`
	f.add(sum)

	run := only(t, f.load(time.Time{}, time.Time{}))
	if run.FanOutDispatched {
		t.Fatal("fan_out_dispatched = true: an echo naming the router is not a call to it")
	}
	if run.Classification == ClassCompliant {
		t.Fatal("classification = compliant off an echo")
	}
}

func TestRouterCallCountsWhenItsOutputCarriesAVerdict(t *testing.T) {
	f := newFixture(t)
	sum := codexSession()
	sum.SessionID = "router"
	// The same one-inlined-lens shape, so the run stands on the router call —
	// which this time carries back the lens it routed.
	sum.Turns[0].AssistantText = "dispatching (loom/codex-6666 round 1): contract, quality, security\n" +
		fenced(`{"lens": "contract", "verdict": "satisfied", "summary": "All criteria met."}`)
	sum.ToolCalls[0].ResultSummary = fenced(
		`{"lens": "security", "verdict": "satisfied", "summary": "No injection or authz exposure found."}`)
	f.add(sum)

	run := only(t, f.load(time.Time{}, time.Time{}))
	if !run.FanOutDispatched || run.Classification != ClassCompliant {
		t.Fatalf("run = %+v, want a compliant run off the routed security verdict", run)
	}
}

func TestSummarizerSessionYieldsNoRun(t *testing.T) {
	f := newFixture(t)
	f.add(&summary.SessionSummary{
		SessionID: "summarizer",
		Agent:     summary.AgentClaude,
		StartTime: base,
		EndTime:   base.Add(time.Hour),
		Turns: []summary.Turn{{
			Idx: 0,
			UserMessage: "# Session Summarizer\n\nYou are a compression agent. The transcript quotes the skill:\n\n" +
				workInvocation("loom/quoted-7777") + "\n\nSummarize it.",
			AssistantText: "dispatching (loom/quoted-7777 round 1): contract, quality, security",
			StartedAt:     base,
		}},
	})

	rep := f.load(time.Time{}, time.Time{})
	if len(rep.Runs) != 0 || rep.Totals.Runs != 0 {
		t.Fatalf("report holds %d runs, want 0: quoting the tags mid-prompt is not an invocation", len(rep.Runs))
	}
}

func TestRunsSplitAtTheNextInvocation(t *testing.T) {
	f := newFixture(t)
	f.add(&summary.SessionSummary{
		SessionID: "two-runs",
		Agent:     summary.AgentClaude,
		StartTime: base,
		EndTime:   base.Add(2 * time.Hour),
		Turns: []summary.Turn{
			{
				Idx:           0,
				UserMessage:   workInvocation("loom/first-1111"),
				AssistantText: "dispatching (loom/first-1111 round 1): contract, quality, security",
				StartedAt:     base,
			},
			{
				Idx:           1,
				UserMessage:   "carry on",
				AssistantText: "Merged and committed.",
				StartedAt:     base.Add(20 * time.Minute),
			},
			{
				Idx:           2,
				UserMessage:   workInvocation("loom/second-2222"),
				AssistantText: "Small change, implemented and committed.",
				StartedAt:     base.Add(time.Hour),
			},
		},
		ToolCalls: []summary.ToolCall{
			{TurnIdx: 0, Kind: summary.KindTask, ToolName: "Agent", KeyArg: "Contract lens review", StartedAt: base.Add(time.Minute)},
			{TurnIdx: 0, Kind: summary.KindTask, ToolName: "Agent", KeyArg: "Quality lens review", StartedAt: base.Add(time.Minute)},
			{TurnIdx: 1, Kind: summary.KindBash, ToolName: "Bash", KeyArg: "git commit", StartedAt: base.Add(25 * time.Minute),
				ResultSummary: commitResult("[loom/first-1111] Do the first thing")},
			{TurnIdx: 2, Kind: summary.KindBash, ToolName: "Bash", KeyArg: "git commit", StartedAt: base.Add(70 * time.Minute),
				ResultSummary: commitResult("[loom/second-2222] Do the second thing")},
		},
	})

	rep := f.load(time.Time{}, time.Time{})
	if len(rep.Runs) != 2 {
		t.Fatalf("report holds %d runs, want 2", len(rep.Runs))
	}
	if rep.Runs[0].Classification != ClassCompliant {
		t.Fatalf("first run = %q, want compliant", rep.Runs[0].Classification)
	}
	// The second run must not inherit the first run's lens calls or its commit.
	if rep.Runs[1].Classification != ClassSkippedFanOut {
		t.Fatalf("second run = %q, want skipped_fanout", rep.Runs[1].Classification)
	}
	if rep.Totals != (Totals{Runs: 2, Compliant: 1, SkippedFanOut: 1}) {
		t.Fatalf("totals = %+v, want 2 runs split 1/1", rep.Totals)
	}
}

func TestCommitStampedWithTheSessionStartStaysWithItsOwnRun(t *testing.T) {
	f := newFixture(t)
	// The second run's commit came off a bash row with no parseable timestamp,
	// so the store backfilled the session start — which falls inside the first
	// run's window. Only its subject places it, and it names the second ticket.
	f.add(&summary.SessionSummary{
		SessionID: "backfilled-commit",
		Agent:     summary.AgentClaude,
		StartTime: base,
		EndTime:   base.Add(2 * time.Hour),
		Turns: []summary.Turn{
			{
				Idx:           0,
				UserMessage:   workInvocation("loom/first-1111"),
				AssistantText: "The ticket names no success criteria. Stopping to ask rather than guessing.",
				StartedAt:     base,
			},
			{
				Idx:           1,
				UserMessage:   workInvocation("loom/second-2222"),
				AssistantText: "Small change, implemented and committed.",
				StartedAt:     base.Add(time.Hour),
			},
		},
		ToolCalls: []summary.ToolCall{
			{TurnIdx: 1, Kind: summary.KindBash, ToolName: "Bash", KeyArg: "git commit",
				ResultSummary: commitResult("[loom/second-2222] Do the second thing")},
		},
	})

	rep := f.load(time.Time{}, time.Time{})
	if len(rep.Runs) != 2 {
		t.Fatalf("report holds %d runs, want 2", len(rep.Runs))
	}
	if rep.Runs[0].Committed {
		t.Fatal("first run committed = true: a session-start stamp is not evidence the first run committed")
	}
	if rep.Runs[0].Classification != ClassIncomplete {
		t.Fatalf("first run = %q, want incomplete", rep.Runs[0].Classification)
	}
	if !rep.Runs[1].Committed || rep.Runs[1].Classification != ClassSkippedFanOut {
		t.Fatalf("second run = %+v, want a committed skipped_fanout run", rep.Runs[1])
	}
}

func TestDateRangeFiltersRuns(t *testing.T) {
	f := newFixture(t)
	f.add(compliantSession())
	old := compliantSession()
	old.SessionID = "older"
	for i := range old.Turns {
		old.Turns[i].StartedAt = old.Turns[i].StartedAt.AddDate(0, 0, -10)
	}
	f.add(old)

	rep := f.load(base.AddDate(0, 0, -1), time.Time{})
	run := only(t, rep)
	if run.SessionID != "compliant" {
		t.Fatalf("session_id = %q, want the in-range run", run.SessionID)
	}
	if rep.Since != base.AddDate(0, 0, -1).Format(time.RFC3339) || rep.Until != "" {
		t.Fatalf("range echo = %q/%q, want the since bound echoed and until empty", rep.Since, rep.Until)
	}

	// until is exclusive: a run invoked at the bound is out.
	if got := len(f.load(time.Time{}, base).Runs); got != 1 {
		t.Fatalf("runs before %s = %d, want 1", base, got)
	}
	if got := len(f.load(base.AddDate(0, 0, 1), time.Time{}).Runs); got != 0 {
		t.Fatalf("runs after both = %d, want 0", got)
	}
}

func TestMissingDatabaseIsAnError(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "summaries.db"), time.Time{}, time.Time{}); err == nil {
		t.Fatal("Load succeeded on a missing DB; an empty report reads as 'nobody ran /work'")
	}
}
