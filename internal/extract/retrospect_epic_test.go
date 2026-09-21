package extract

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/EnderRealm/ticket/v8/pkg/ticket"
)

// The catalog the epic cases run against: Root activated, foreign parents
// activated, and the namespaces an epic's children are spread across.
const crossProjectCatalog = "required_features: [root-namespace, cross-project-parents]\n" +
	"namespaces:\n  _root: {kind: root}\n  loom: {kind: project}\n  warp: {kind: project}\n"

// newTicketStore is a throwaway tk central store in the production layout,
// with TK_STORE_ROOT pointing at it so the expansion reads this store and
// never the machine's. HOME is moved with it: tk keeps its lock directory
// under the user cache.
func newTicketStore(t *testing.T, catalog string, namespaces ...string) *ticket.MultiStore {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	t.Setenv("TK_STORE_ROOT", root)
	if err := os.WriteFile(ticket.CatalogPath(root), []byte(catalog), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, ns := range namespaces {
		if err := os.MkdirAll(filepath.Join(root, "tickets", ns), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return ticket.NewMultiStore(filepath.Join(root, "tickets"))
}

func mkTicket(id string, typ ticket.TicketType, status ticket.Status, parent string) *ticket.Ticket {
	return &ticket.Ticket{
		ID:       id,
		Type:     typ,
		Status:   status,
		Parent:   parent,
		Priority: 2,
		Deps:     []string{},
		Links:    []string{},
		Created:  time.Now().Add(-time.Hour),
		Title:    "Ticket " + id,
		Body:     "\n",
	}
}

func mustCreate(t *testing.T, ms *ticket.MultiStore, tickets ...*ticket.Ticket) {
	t.Helper()
	for _, tk := range tickets {
		if err := ms.Create(tk); err != nil {
			t.Fatalf("Create %s: %v", tk.ID, err)
		}
	}
}

// A Root epic's children live in other projects. Its retrospect covers exactly
// those children — a session is selected once however many of them it landed
// for, files under the scope its own checkout resolves to, and a checkout that
// resolves to no scope in the store is skipped with the reason on record.
func TestRetrospectExpandsARootEpicToItsExactChildren(t *testing.T) {
	e := newRetroEnv(t, "loom", "warp")
	ms := newTicketStore(t, crossProjectCatalog, "_root", "loom", "warp")
	mustCreate(t, ms,
		mkTicket("_root/unified-0001", ticket.TypeEpic, ticket.StatusBacklog, ""),
		mkTicket("loom/half-0002", ticket.TypeFeature, ticket.StatusDone, "_root/unified-0001"),
		mkTicket("warp/half-0003", ticket.TypeFeature, ticket.StatusDone, "_root/unified-0001"),
		mkTicket("loom/bystander-0004", ticket.TypeFeature, ticket.StatusDone, ""),
	)

	// One checkout landed both halves: one session, one extraction per type.
	shared := e.addSessionWithCommits("shared", loomRemote,
		"[loom/half-0002] The loom half",
		"[warp/half-0003] The warp half, from the same checkout")
	// The warp half again, from a warp checkout: its own scope, not the epic's.
	warpOnly := e.addSessionWithCommits("warp-only", warpRemote, "[warp/half-0003] Finish the warp half")
	// A checkout whose remote names a scope the store has no directory for.
	e.addSessionWithCommits("weft", "https://github.com/EnderRealm/weft.git", "[loom/half-0002] From an unknown scope")
	// Not a child: a ticket in the same project that merely sits beside them.
	e.addSessionWithCommits("bystander", loomRemote, "[loom/bystander-0004] Unrelated")

	if err := Retrospect(RetrospectOptions{TicketID: "_root/unified-0001"}); err != nil {
		t.Fatalf("Retrospect: %v", err)
	}

	wantRuns := []string{"loom " + shared, "loom " + shared, "warp " + warpOnly, "warp " + warpOnly}
	if !reflect.DeepEqual(e.runs, wantRuns) {
		t.Fatalf("runs = %v, want %v (each session once, under its own checkout's scope)", e.runs, wantRuns)
	}
	logs := e.logs.String()
	if !strings.Contains(logs, "retrospect _root/unified-0001: epic — 2 child ticket(s) selected through tk") {
		t.Errorf("log does not record the expansion:\n%s", logs)
	}
	if !strings.Contains(logs, `skip claude-code/weft: unknown scope "weft"`) {
		t.Errorf("log does not record the unknown-scope skip:\n%s", logs)
	}
	if strings.Contains(logs, "tk store incomplete") {
		t.Errorf("a complete store was reported incomplete:\n%s", logs)
	}
}

// A project epic expands the same way, and an incomplete store — a catalogued
// namespace with no directory — still expands what it can see while saying
// what it could not, rather than silently narrowing the run.
func TestRetrospectExpandsAProjectEpicAndReportsAnIncompleteStore(t *testing.T) {
	e := newRetroEnv(t, "loom", "warp")
	ms := newTicketStore(t, crossProjectCatalog+"  ticket: {kind: project}\n", "_root", "loom", "warp")
	mustCreate(t, ms,
		mkTicket("loom/epic-0001", ticket.TypeEpic, ticket.StatusBacklog, ""),
		mkTicket("loom/half-0002", ticket.TypeFeature, ticket.StatusDone, "loom/epic-0001"),
		mkTicket("warp/half-0003", ticket.TypeFeature, ticket.StatusDone, "loom/epic-0001"),
	)
	loomHalf := e.addSessionWithCommits("loom-half", loomRemote, "[loom/half-0002] The loom half")
	warpHalf := e.addSessionWithCommits("warp-half", warpRemote, "[warp/half-0003] The warp half")

	if err := Retrospect(RetrospectOptions{TicketID: "loom/epic-0001"}); err != nil {
		t.Fatalf("Retrospect: %v", err)
	}

	wantRuns := []string{"loom " + loomHalf, "loom " + loomHalf, "warp " + warpHalf, "warp " + warpHalf}
	if !reflect.DeepEqual(e.runs, wantRuns) {
		t.Fatalf("runs = %v, want %v", e.runs, wantRuns)
	}
	logs := e.logs.String()
	if !strings.Contains(logs, "retrospect loom/epic-0001: epic — 2 child ticket(s) selected through tk") {
		t.Errorf("log does not record the expansion:\n%s", logs)
	}
	if !strings.Contains(logs, `retrospect loom/epic-0001: tk store incomplete: namespace "ticket"`) {
		t.Errorf("log does not name the namespace the store could not read:\n%s", logs)
	}
}

// The automatic trigger fires for the ticket that closed, which is always a
// leaf: an epic's status is derived, so completing its last child completes
// it without a write. A leaf retrospect therefore selects that leaf's sessions
// alone, however its epic reads — the sibling's transcript is not re-spent.
func TestRetrospectOfALeafSelectsOnlyItsOwnSessions(t *testing.T) {
	e := newRetroEnv(t, "loom", "warp")
	ms := newTicketStore(t, crossProjectCatalog, "_root", "loom", "warp")
	mustCreate(t, ms,
		mkTicket("_root/unified-0001", ticket.TypeEpic, ticket.StatusBacklog, ""),
		mkTicket("loom/half-0002", ticket.TypeFeature, ticket.StatusDone, "_root/unified-0001"),
		mkTicket("warp/half-0003", ticket.TypeFeature, ticket.StatusDone, "_root/unified-0001"),
	)
	snap, err := ms.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if epic, _ := snap.Get("_root/unified-0001"); epic.Status != ticket.StatusDone {
		t.Fatalf("epic reads %s, want done from its two done children", epic.Status)
	}
	loomHalf := e.addSessionWithCommits("loom-half", loomRemote, "[loom/half-0002] The loom half")
	e.addSessionWithCommits("warp-half", warpRemote, "[warp/half-0003] The warp half")

	if err := Retrospect(RetrospectOptions{TicketID: "loom/half-0002"}); err != nil {
		t.Fatalf("Retrospect: %v", err)
	}

	wantRuns := []string{"loom " + loomHalf, "loom " + loomHalf}
	if !reflect.DeepEqual(e.runs, wantRuns) {
		t.Fatalf("runs = %v, want %v (the leaf alone, not its epic's other children)", e.runs, wantRuns)
	}
	if strings.Contains(e.logs.String(), "child ticket(s) selected") {
		t.Errorf("a leaf was expanded:\n%s", e.logs.String())
	}
}

// A store loom cannot read the ticket from — here, one that has never heard
// of it — narrows the run to the id the operator named, and says so.
func TestRetrospectFallsBackToTheNamedIDWhenTheStoreLacksIt(t *testing.T) {
	e := newRetroEnv(t, "loom")
	input := e.addSessionWithCommits("s1", loomRemote, "["+retroTicket+"] Add the command")

	if err := Retrospect(RetrospectOptions{TicketID: retroTicket}); err != nil {
		t.Fatalf("Retrospect: %v", err)
	}
	if want := []string{"loom " + input, "loom " + input}; !reflect.DeepEqual(e.runs, want) {
		t.Fatalf("runs = %v, want %v", e.runs, want)
	}
	if !strings.Contains(e.logs.String(), "is not in the central store") || !strings.Contains(e.logs.String(), "selecting by the named id alone") {
		t.Errorf("log does not say the store could not answer:\n%s", e.logs.String())
	}
}

// The id gate: the reserved namespace as the exact literal, the existing
// charset and bounds on both halves.
func TestTicketIDPatternAdmitsRootAndKeepsItsBounds(t *testing.T) {
	sixtyOne := strings.Repeat("a", 61)
	sixtyTwo := strings.Repeat("a", 62)
	for id, want := range map[string]bool{
		"_root/ok-id":             true,
		"_root/" + sixtyOne:       true,
		"loom/ok-id":              true,
		sixtyOne + "/" + sixtyOne: true,
		"_root/" + sixtyTwo:       false,
		sixtyTwo + "/ok-id":       false,
		"loom/" + sixtyTwo:        false,
		"_rootx/ok-id":            false,
		"_other/ok-id":            false,
		"_root":                   false,
		"_root/":                  false,
		"_root/two/segments":      false,
		"_root/bad id":            false,
		"_ROOT/ok-id":             false,
		"_root/ok-id\n":           false,
	} {
		if got := ticketIDPattern.MatchString(id); got != want {
			t.Errorf("ticketIDPattern.MatchString(%q) = %v, want %v", id, got, want)
		}
	}
}
