package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/EnderRealm/ticket/v8/pkg/ticket"
)

// The catalog every cross-project case here runs against: Root activated,
// foreign parents activated, and the three namespaces a Root epic's children
// are spread across. A case that needs a catalogued namespace with no
// directory appends to it.
const crossProjectCatalog = "required_features: [root-namespace, cross-project-parents]\n" +
	"namespaces:\n  _root: {kind: root}\n  loom: {kind: project}\n  warp: {kind: project}\n"

// newCentralStore is a throwaway tk central store in the production layout —
// tickets under <root>/tickets/<namespace>, the catalog beside that directory
// — with TK_STORE_ROOT pointing at it so both loom's own reads and the
// library's project resolution land there and never on the machine's store.
// HOME is moved too: tk keeps its lock directory under the user cache.
func newCentralStore(t *testing.T, catalog string, namespaces ...string) (string, *ticket.MultiStore) {
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
	return root, ticket.NewMultiStore(filepath.Join(root, "tickets"))
}

// registerRepo binds a checkout to a namespace the way `tk init` does, split
// across the two config halves tk merges: the path in the store root's own
// .ticket/config.yaml (the local half under TK_STORE_ROOT), the registration
// in the shared <root>/config.yaml. ResolveStoreForRepo reads the merge.
func registerRepo(t *testing.T, root, ns, repoDir string) {
	t.Helper()
	local := filepath.Join(root, ".ticket", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(local, []byte("central_root: "+root+"\nprojects:\n  "+ns+":\n    path: "+repoDir+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "config.yaml"), []byte("projects:\n  "+ns+":\n    store: central\n"), 0o644); err != nil {
		t.Fatal(err)
	}
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

// setStatus moves a leaf through tk's own write path, which stamps Completed
// on the way into done and clears it on the way out.
func setStatus(t *testing.T, ms *ticket.MultiStore, id string, status ticket.Status) {
	t.Helper()
	if _, err := ticket.Mutate(ms, id, func(tk *ticket.Ticket) error {
		tk.Status = status
		return nil
	}); err != nil {
		t.Fatalf("Mutate %s → %s: %v", id, status, err)
	}
}

func mustSnapshot(t *testing.T) *ticket.Snapshot {
	t.Helper()
	snap, err := LoadTicketSnapshot()
	if err != nil {
		t.Fatalf("LoadTicketSnapshot: %v", err)
	}
	return snap
}

func findChange(cs []TicketChange, id string) (TicketChange, bool) {
	for _, c := range cs {
		if c.ID == id {
			return c, true
		}
	}
	return TicketChange{}, false
}

// A Root epic has no repository and so no session ever runs "in" it. Its
// activity still shows, attributed to _root and to nothing else — loom must
// not invent a repository for it.
func TestTicketActivityShowsRootWithoutARepoOrSession(t *testing.T) {
	_, ms := newCentralStore(t, crossProjectCatalog, "_root", "loom", "warp")
	mustCreate(t, ms,
		mkTicket("_root/unified-0001", ticket.TypeEpic, ticket.StatusBacklog, ""),
		mkTicket("loom/half-0002", ticket.TypeFeature, ticket.StatusDone, "_root/unified-0001"),
		mkTicket("warp/half-0003", ticket.TypeFeature, ticket.StatusOpen, "_root/unified-0001"),
	)

	ta := LoadTicketActivity(24 * time.Hour)
	if !ta.Available || !ta.Complete {
		t.Fatalf("Available=%v Complete=%v diagnostics=%v, want an available, complete rollup", ta.Available, ta.Complete, ta.Diagnostics)
	}
	if !contains(ta.Namespaces, "_root") {
		t.Errorf("Namespaces = %v, want _root read in full", ta.Namespaces)
	}
	epic, ok := findChange(ta.Created, "_root/unified-0001")
	if !ok {
		t.Fatalf("created = %v, want the Root epic", changeIDs(ta.Created))
	}
	if epic.Project != "_root" {
		t.Errorf("Project = %q, want _root", epic.Project)
	}
	if epic.Status != string(ticket.StatusOpen) {
		t.Errorf("Status = %q, want open derived from one done and one open child", epic.Status)
	}
	if _, closed := findChange(ta.Closed, "_root/unified-0001"); closed {
		t.Error("the epic reads closed while a child is still open")
	}

	setStatus(t, ms, "warp/half-0003", ticket.StatusDone)
	ta = LoadTicketActivity(24 * time.Hour)
	epic, ok = findChange(ta.Closed, "_root/unified-0001")
	if !ok {
		t.Fatalf("closed = %v, want the Root epic once both children are done", changeIDs(ta.Closed))
	}
	if epic.Status != string(ticket.StatusDone) || epic.Project != "_root" {
		t.Errorf("closed epic = %+v, want done under _root", epic)
	}
}

// The project rollup and the all-project activity are both filters over one
// snapshot, so an epic reads the same status and Completed in each, and the
// project's counts cover its own namespace alone.
func TestProjectSummaryAndActivityAgreeOnAnEpic(t *testing.T) {
	root, ms := newCentralStore(t, crossProjectCatalog, "_root", "loom", "warp")
	repo := t.TempDir()
	registerRepo(t, root, "loom", repo)
	mustCreate(t, ms,
		mkTicket("_root/unified-0001", ticket.TypeEpic, ticket.StatusBacklog, ""),
		mkTicket("loom/half-0002", ticket.TypeFeature, ticket.StatusOpen, "_root/unified-0001"),
		mkTicket("warp/half-0003", ticket.TypeFeature, ticket.StatusOpen, "_root/unified-0001"),
		mkTicket("loom/own-epic-0004", ticket.TypeEpic, ticket.StatusBacklog, ""),
		mkTicket("loom/own-leaf-0005", ticket.TypeFeature, ticket.StatusReady, "loom/own-epic-0004"),
	)
	setStatus(t, ms, "loom/half-0002", ticket.StatusDone)
	setStatus(t, ms, "warp/half-0003", ticket.StatusDone)

	snap := mustSnapshot(t)
	since := time.Now().Add(-24 * time.Hour)
	summary := ticketSummaryFrom(snap, repo)
	activity := ticketActivityFrom(snap, since)

	if summary == nil {
		t.Fatal("ticketSummaryFrom = nil, want the loom namespace resolved from its registered checkout")
	}
	if summary.ProjectName != "loom" || summary.Dir != filepath.Join(root, "tickets", "loom") {
		t.Errorf("summary = %s at %s, want loom at its store directory", summary.ProjectName, summary.Dir)
	}
	if !summary.Complete || len(summary.Diagnostics) != 0 {
		t.Errorf("Complete=%v Diagnostics=%v, want a complete read", summary.Complete, summary.Diagnostics)
	}
	// loom's own three tickets and nothing from _root or warp. The loom epic
	// reads backlog: its one child is ready, not open.
	if summary.Total != 3 || summary.Status["done"] != 1 || summary.Status["ready"] != 1 || summary.Status["backlog"] != 1 {
		t.Errorf("counts = total %d %v, want 3 loom tickets: done, ready, and the backlog epic", summary.Total, summary.Status)
	}
	if len(summary.OpenTop) != 1 {
		t.Errorf("OpenTop = %d tickets, want the ready leaf alone", len(summary.OpenTop))
	}
	for _, tk := range summary.OpenTop {
		if ns, _ := ticket.ParseNamespacedID(tk.ID); ns != "loom" {
			t.Errorf("OpenTop carries %s from outside loom", tk.ID)
		}
	}

	// The Root epic reads done with the latest child's Completed in the
	// activity view, and the epic in loom's namespace reads the same way in
	// the project rollup — both derived once, by the same snapshot.
	rootEpic, ok := snap.Get("_root/unified-0001")
	if !ok {
		t.Fatal("snapshot lost the Root epic")
	}
	warpChild, _ := snap.Get("warp/half-0003")
	if rootEpic.Status != ticket.StatusDone || !rootEpic.Completed.Equal(warpChild.Completed) || rootEpic.Completed.IsZero() {
		t.Fatalf("Root epic = %s completed %v, want done at the warp child's %v", rootEpic.Status, rootEpic.Completed, warpChild.Completed)
	}
	closed, ok := findChange(activity.Closed, "_root/unified-0001")
	if !ok {
		t.Fatalf("activity closed = %v, want the Root epic", changeIDs(activity.Closed))
	}
	if closed.Status != string(rootEpic.Status) || !closed.When.Equal(rootEpic.Completed) {
		t.Errorf("activity reports %s at %v, snapshot derives %s at %v", closed.Status, closed.When, rootEpic.Status, rootEpic.Completed)
	}
	loomEpic, _ := snap.Get("loom/own-epic-0004")
	if loomEpic.Status != ticket.StatusBacklog || !loomEpic.Completed.IsZero() {
		t.Errorf("loom epic = %s completed %v, want backlog with no completion from its ready child", loomEpic.Status, loomEpic.Completed)
	}
	if created, ok := findChange(activity.Created, "loom/own-epic-0004"); !ok || created.Status != string(loomEpic.Status) {
		t.Errorf("activity created = %+v, want the loom epic reading %s as the project view does", created, loomEpic.Status)
	}
}

// A catalogued namespace with no directory is a ticket set the snapshot cannot
// see. Both views say so, and no epic reads done while it stands: the missing
// tickets could be its children.
func TestTicketViewsSurfaceAnIncompleteSnapshot(t *testing.T) {
	root, ms := newCentralStore(t, crossProjectCatalog+"  ticket: {kind: project}\n", "_root", "loom", "warp")
	repo := t.TempDir()
	registerRepo(t, root, "loom", repo)
	mustCreate(t, ms,
		mkTicket("_root/unified-0001", ticket.TypeEpic, ticket.StatusBacklog, ""),
		mkTicket("loom/half-0002", ticket.TypeFeature, ticket.StatusOpen, "_root/unified-0001"),
	)
	setStatus(t, ms, "loom/half-0002", ticket.StatusDone)

	snap := mustSnapshot(t)
	summary := ticketSummaryFrom(snap, repo)
	activity := ticketActivityFrom(snap, time.Now().Add(-24*time.Hour))

	if summary == nil || summary.Complete || len(summary.Diagnostics) == 0 {
		t.Fatalf("summary = %+v, want an incomplete read with its diagnostics", summary)
	}
	if activity.Complete || len(activity.Diagnostics) == 0 {
		t.Fatalf("activity Complete=%v Diagnostics=%v, want incomplete with its diagnostics", activity.Complete, activity.Diagnostics)
	}
	for _, diags := range [][]string{summary.Diagnostics, activity.Diagnostics} {
		if !strings.Contains(strings.Join(diags, "\n"), `namespace "ticket"`) {
			t.Errorf("diagnostics %v do not name the missing namespace", diags)
		}
	}
	if contains(activity.Namespaces, "ticket") {
		t.Errorf("Namespaces = %v lists a namespace that could not be read", activity.Namespaces)
	}
	epic, _ := snap.Get("_root/unified-0001")
	if epic.Status == ticket.StatusDone || !epic.Completed.IsZero() {
		t.Errorf("epic = %s completed %v, want no completion while the store is incomplete", epic.Status, epic.Completed)
	}
	if _, closed := findChange(activity.Closed, "_root/unified-0001"); closed {
		t.Error("activity closes the epic over an incomplete read")
	}
	if summary.Status["done"] != 1 {
		t.Errorf("loom counts = %v, want the done leaf still counted as done", summary.Status)
	}
}

// An epic's status and Completed are derived on read from its children in
// every namespace; the epic's own file is never rewritten for a child's
// change, so a foreign child completing or reopening moves the epic without a
// mutation anywhere near it.
func TestForeignChildCompletionMovesTheEpicWithoutTouchingItsFile(t *testing.T) {
	root, ms := newCentralStore(t, crossProjectCatalog, "_root", "loom", "warp")
	mustCreate(t, ms,
		mkTicket("_root/unified-0001", ticket.TypeEpic, ticket.StatusBacklog, ""),
		mkTicket("loom/half-0002", ticket.TypeFeature, ticket.StatusOpen, "_root/unified-0001"),
		mkTicket("warp/half-0003", ticket.TypeFeature, ticket.StatusOpen, "_root/unified-0001"),
	)
	epicFile := filepath.Join(root, "tickets", "_root", "unified-0001.md")
	before, err := os.ReadFile(epicFile)
	if err != nil {
		t.Fatalf("read epic file: %v", err)
	}
	since := time.Now().Add(-24 * time.Hour)

	setStatus(t, ms, "loom/half-0002", ticket.StatusDone)
	setStatus(t, ms, "warp/half-0003", ticket.StatusDone)
	snap := mustSnapshot(t)
	epic, _ := snap.Get("_root/unified-0001")
	warpChild, _ := snap.Get("warp/half-0003")
	if epic.Status != ticket.StatusDone || epic.Completed.IsZero() || !epic.Completed.Equal(warpChild.Completed) {
		t.Fatalf("epic = %s completed %v, want done at the last child's %v", epic.Status, epic.Completed, warpChild.Completed)
	}
	if _, ok := findChange(ticketActivityFrom(snap, since).Closed, "_root/unified-0001"); !ok {
		t.Error("activity does not close the epic after its foreign child completed")
	}

	setStatus(t, ms, "warp/half-0003", ticket.StatusOpen)
	snap = mustSnapshot(t)
	epic, _ = snap.Get("_root/unified-0001")
	if epic.Status != ticket.StatusOpen || !epic.Completed.IsZero() {
		t.Fatalf("epic = %s completed %v after the warp child reopened, want open with no completion", epic.Status, epic.Completed)
	}
	if _, ok := findChange(ticketActivityFrom(snap, since).Closed, "_root/unified-0001"); ok {
		t.Error("activity still closes the epic after its foreign child reopened")
	}

	after, err := os.ReadFile(epicFile)
	if err != nil {
		t.Fatalf("read epic file: %v", err)
	}
	if string(before) != string(after) {
		t.Errorf("the epic's file changed while only its children were written:\n--- before\n%s\n--- after\n%s", before, after)
	}
}
