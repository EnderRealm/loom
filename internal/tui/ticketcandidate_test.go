package tui

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/EnderRealm/ticket/v8/pkg/ticket"

	"loom/internal/knowledge/store"
)

// ticketCatalog makes `ticket` a selectable namespace of the throwaway store.
const ticketCatalog = "namespaces:\n  ticket: {kind: project}\n"

// walkupSession is the session every walk-up fixture cites: the five corpus
// sources (1bdf4151, ace9a39d, cb3fe3ba, dc303a88, a220bca2) were extractor runs
// over one byte-identical summary of it.
const walkupSession = "91d979db-8c94-4f38-999b-90b028c5b543"

// seedTicketStores builds the two stores a filing touches, both throwaway: a
// knowledge store that is a git repo holding the candidates under
// _candidates/tickets/ticket/, and a tk central store (TK_STORE_ROOT, from
// newCentralStore) holding a few unrelated and nearly-related tickets, so the
// duplicate check has to rank rather than return the only ticket there is. The
// real tk binary does the writing — that is the path being verified — so the
// test needs it on PATH.
func seedTicketStores(t *testing.T, candidates map[string]string) (string, *ticket.MultiStore) {
	t.Helper()
	if _, err := exec.LookPath("tk"); err != nil {
		t.Skip("tk not on PATH")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	root := t.TempDir()
	t.Setenv("LOOM_KNOWLEDGE_ROOT", root)
	t.Setenv("LOOM_HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	dir := filepath.Join(root, "_candidates", "tickets", "ticket")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range candidates {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "log.md"), []byte(logFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	testGit(t, root, "init")
	testGit(t, root, "config", "user.email", "test@example.com")
	testGit(t, root, "config", "user.name", "loom test")
	testGit(t, root, "add", "-A")
	testGit(t, root, "commit", "-m", "seed store")

	_, ms := newCentralStore(t, ticketCatalog, "ticket")
	decoys := map[string]string{
		"ticket/tk-move-writes-0001":    "tk move writes to a hardcoded .tickets/ dir instead of the target's central project",
		"ticket/search-ranking-0002":    "Search ranks body hits above title hits",
		"ticket/verify-timeout-0003":    "verify_timeout is not read by tk serve",
		"ticket/inbox-blocked-0004":     "ticket_inbox reports tickets with unresolved deps as blocked",
		"ticket/central-store-cfg-0005": "Central store configuration and init",
	}
	for id, title := range decoys {
		tk := mkTicket(id, ticket.TypeBug, ticket.StatusDone, "")
		tk.Title = title
		mustCreate(t, ms, tk)
	}
	return root, ms
}

// walkupFixtures reads the ticket candidates the live re-extraction of the
// walk-up corpus produced, verbatim.
func walkupFixtures(t *testing.T) map[string]string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("testdata", "walkup-tickets", "*.md"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no walk-up fixtures: %v", err)
	}
	out := map[string]string{}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		out[filepath.Base(p)] = string(b)
	}
	return out
}

func keyPress(s string) tea.KeyMsg {
	switch s {
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

// msgOf returns the first message of type T a command fans out to.
func msgOf[T any](t *testing.T, cmd tea.Cmd) T {
	t.Helper()
	for _, msg := range drainCmd(cmd) {
		if m, ok := msg.(T); ok {
			return m
		}
	}
	var zero T
	t.Fatalf("no %T among the command's messages", zero)
	return zero
}

// promoteTicket drives one ticket candidate through the review screen's own
// gesture: p, the duplicate check, the chooser, enter, and the deferred record.
// choose picks the chooser row from the matches the check offered — the
// reviewer's decision, which is the one thing a test has to stand in for.
func promoteTicket(t *testing.T, a Artifact, choose func([]ticketMatch) int) ticketFiledMsg {
	t.Helper()
	var m knowledgeModel
	m.setArtifacts([]Artifact{a})
	m, cmd := m.promote()
	checked := msgOf[ticketCheckedMsg](t, cmd)
	if checked.err != nil {
		t.Fatalf("duplicate check: %v", checked.err)
	}
	m, _ = m.update(checked)
	if m.filing == nil {
		t.Fatal("the duplicate check opened no chooser")
	}
	row := choose(m.filing.check.matches)
	for i := 0; i < row; i++ {
		m, _ = m.update(keyPress("down"))
	}
	m, cmd = m.update(keyPress("enter"))
	if m.pendingCommits != 1 {
		t.Errorf("pendingCommits = %d after confirming, want 1", m.pendingCommits)
	}
	filed := msgOf[ticketFiledMsg](t, cmd)
	if filed.commit == nil {
		t.Fatalf("filing did not land: %s", filed.status)
	}
	if w := filed.commit(); w.NotCommitted != "" {
		t.Fatalf("filing was not recorded: %s", w.NotCommitted)
	}
	return filed
}

// TestWalkupCandidatesResolveToOneTicket is the corpus verification: the
// ticket_create walk-up defect, extracted four times from one summary, files
// once. The first candidate finds no duplicate and files a bug; each later one
// is offered that bug by the duplicate check and becomes a note on it. The
// filed ticket and every note cite the source session.
func TestWalkupCandidatesResolveToOneTicket(t *testing.T) {
	root, ms := seedTicketStores(t, walkupFixtures(t))
	before, err := ms.List()
	if err != nil {
		t.Fatal(err)
	}

	arts, err := LoadKnowledge()
	if err != nil {
		t.Fatal(err)
	}
	if len(arts) != 4 {
		t.Fatalf("loaded %d candidates, want the 4 walk-up tickets", len(arts))
	}
	filedID := ""
	for i, a := range arts {
		if a.Type != ticketType {
			t.Fatalf("%s loaded as %q, want a ticket candidate", a.ID, a.Type)
		}
		filed := promoteTicket(t, a, func(matches []ticketMatch) int {
			for rank, m := range matches {
				t.Logf("candidate %d (%s): offered #%d %s %q", i, filepath.Base(a.Path), rank+1, m.ID, m.Title)
			}
			if i == 0 {
				// No ticket for this defect exists yet: whatever decoys share a
				// term with the title are offered, and the reviewer files new.
				return 0
			}
			for j, m := range matches {
				if m.ID == filedID {
					return j + 1
				}
			}
			t.Fatalf("candidate %d: the duplicate check did not offer %s; offered %+v", i, filedID, matches)
			return 0
		})
		if i == 0 {
			filedID = strings.TrimSuffix(strings.TrimPrefix(filed.status, "filed "), " — archived to _filed/")
			if !strings.HasPrefix(filedID, "ticket/") {
				t.Fatalf("first filing status %q names no ticket", filed.status)
			}
		} else if filed.status != "noted on "+filedID+" — archived to _filed/" {
			t.Errorf("candidate %d status = %q, want a note on %s", i, filed.status, filedID)
		}
	}

	after, err := ms.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before)+1 {
		t.Fatalf("tk holds %d tickets after filing, want %d: one new ticket for four candidates", len(after), len(before)+1)
	}
	tk, err := ms.Get(filedID)
	if err != nil {
		t.Fatal(err)
	}
	if tk.Type != ticket.TypeBug || tk.Status != ticket.StatusBacklog {
		t.Errorf("filed %s/%s, want a backlog bug", tk.Type, tk.Status)
	}
	if !strings.Contains(tk.Body, walkupSession) || !strings.Contains(tk.Body, "## Problem") {
		t.Errorf("filed body does not carry the problem and cite %s:\n%s", walkupSession, tk.Body)
	}
	if len(tk.Notes) != 3 {
		t.Fatalf("%s carries %d notes, want one per later candidate (3)", filedID, len(tk.Notes))
	}
	for _, n := range tk.Notes {
		if !strings.Contains(n.Text, walkupSession) || !strings.Contains(n.Text, "Also reported by loom ticket candidate") {
			t.Errorf("note does not cite its candidate and session:\n%s", n.Text)
		}
	}

	// Every candidate left the review list for the filed archive, and log.md
	// records each landing against the one ticket.
	left, err := LoadKnowledge()
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Errorf("%d candidate(s) still listed after filing", len(left))
	}
	archived, _ := filepath.Glob(filepath.Join(root, "_candidates", "_filed", "tickets", "ticket", "*.md"))
	if len(archived) != 4 {
		t.Errorf("%d candidate(s) archived under _filed/, want 4", len(archived))
	}
	log, _ := os.ReadFile(filepath.Join(root, "log.md"))
	if got := strings.Count(string(log), "] file "); got != 1 {
		t.Errorf("log.md holds %d file entries, want 1:\n%s", got, log)
	}
	if got := strings.Count(string(log), "] note "); got != 3 {
		t.Errorf("log.md holds %d note entries, want 3:\n%s", got, log)
	}
	if got := strings.Count(string(log), "→ "+filedID); got != 4 {
		t.Errorf("log.md names %s %d times, want 4:\n%s", filedID, got, log)
	}
	if st := testGit(t, root, "status", "--porcelain"); st != "" {
		t.Errorf("filings left the store dirty:\n%s", st)
	}
}

// singleWalkup is one walk-up fixture, for the cases that need a candidate but
// not the corpus.
func singleWalkup(t *testing.T) map[string]string {
	t.Helper()
	for name, body := range walkupFixtures(t) {
		return map[string]string{name: body}
	}
	return nil
}

// TestTicketCandidateIsNotPromotedIntoTheStore: a ticket's validated home is
// tk, so the move promote performs for a truth must refuse one rather than
// invent a tickets/ tree.
func TestTicketCandidateIsNotPromotedIntoTheStore(t *testing.T) {
	root, _ := seedTicketStores(t, singleWalkup(t))
	arts, err := LoadKnowledge()
	if err != nil || len(arts) != 1 {
		t.Fatalf("LoadKnowledge: %d artifacts, %v", len(arts), err)
	}
	if _, _, err := promoteCandidate(arts[0]); err == nil {
		t.Fatal("promoteCandidate moved a ticket candidate")
	}
	if _, err := os.Stat(filepath.Join(root, "tickets")); !os.IsNotExist(err) {
		t.Errorf("a tickets/ tree exists after a refused promote: %v", err)
	}
}

// TestRejectTicketCandidateArchivesLikeATruth: reject is the same gesture for
// every destination, archived under its own type.
func TestRejectTicketCandidateArchivesLikeATruth(t *testing.T) {
	root, _ := seedTicketStores(t, singleWalkup(t))
	arts, _ := LoadKnowledge()
	dest, warn, err := rejectNow(arts[0])
	if err != nil || warn.NotCommitted != "" {
		t.Fatalf("reject: %v / %q", err, warn.NotCommitted)
	}
	want := filepath.Join(root, "_candidates", "_rejected", "tickets", "ticket", filepath.Base(arts[0].Path))
	if dest != want {
		t.Errorf("dest = %q, want %q", dest, want)
	}
}

// TestRefusedFilingLeavesTheCandidate: tk refusing the write — here a scope tk
// has no namespace for — files nothing and archives nothing, so the candidate
// is still there to re-scope or reject.
func TestRefusedFilingLeavesTheCandidate(t *testing.T) {
	root, ms := seedTicketStores(t, singleWalkup(t))
	arts, _ := LoadKnowledge()
	a := arts[0]
	a.Scope = "nosuch"
	before, _ := ms.List()

	id, commit, err := fileTicketCandidate(a, "")
	if err == nil || id != "" || commit != nil {
		t.Fatalf("fileTicketCandidate = %q, %v, %v; want tk's refusal and nothing landed", id, commit != nil, err)
	}
	if !strings.Contains(err.Error(), "tk:") {
		t.Errorf("error %q does not carry tk's own reason", err)
	}
	if _, err := os.Stat(a.Path); err != nil {
		t.Errorf("candidate gone after a refused filing: %v", err)
	}
	if after, _ := ms.List(); len(after) != len(before) {
		t.Errorf("tk holds %d tickets, want %d", len(after), len(before))
	}
	if log := readLog(t, root); strings.Contains(log, "] file ") {
		t.Errorf("log.md records a filing that did not happen:\n%s", log)
	}
}

// TestFilingWithoutATicketTypeNeverReachesTk: the type is what tk files as, and
// a candidate missing it is refused before any write rather than defaulted.
func TestFilingWithoutATicketTypeNeverReachesTk(t *testing.T) {
	_, ms := seedTicketStores(t, singleWalkup(t))
	arts, _ := LoadKnowledge()
	a := arts[0]
	a.TicketType = ""
	before, _ := ms.List()
	if _, _, err := fileTicketCandidate(a, ""); err == nil {
		t.Fatal("filed a candidate with no ticket_type")
	}
	if after, _ := ms.List(); len(after) != len(before) {
		t.Errorf("tk holds %d tickets, want %d", len(after), len(before))
	}
}

// TestTicketTextCitesEverySession: the filed ticket is the only link back to
// the evidence, so every session the candidate cites reaches it.
func TestTicketTextCitesEverySession(t *testing.T) {
	text := ticketText(Artifact{
		ID:            "ticket-x",
		Body:          "---\nid: ticket-x\n---\n\n## Problem\n\nIt breaks.\n",
		EvidencePaths: []string{"mcp.go"},
		Sessions:      []string{"aaaa-1111", "bbbb-2222"},
	}, "")
	for _, want := range []string{"## Problem\n\nIt breaks.", "- mcp.go", "`ticket-x`", "- aaaa-1111", "- bbbb-2222"} {
		if !strings.Contains(text, want) {
			t.Errorf("ticket text lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "id: ticket-x") {
		t.Errorf("ticket text carries the candidate's frontmatter:\n%s", text)
	}
}

// TestFileLogEntryBoundsFitTheLine mirrors the reject bound: every field at its
// bound still lands inside store.MessageMax, with the ticket id intact.
func TestFileLogEntryBoundsFitTheLine(t *testing.T) {
	ticketID := strings.Repeat("n", logFieldScopeMax) + "/" + strings.Repeat("t", logFieldTicketMax-logFieldScopeMax-1)
	entry := strings.TrimSpace(fileLogEntry("note", Artifact{
		ID:    strings.Repeat("a", logFieldIDMax),
		Scope: strings.Repeat("b", logFieldScopeMax),
		Path:  strings.Repeat("d", fileLogBaseMax),
	}, ticketID))
	if n := len([]rune(entry)); n != store.MessageMax {
		t.Errorf("worst-case entry is %d runes, want exactly %d:\n%q", n, store.MessageMax, entry)
	}
	if !strings.HasSuffix(entry, "→ "+ticketID) {
		t.Errorf("worst-case entry lost its ticket id:\n%q", entry)
	}
}

// TestFailedDuplicateCheckOpensNoChooser: filing without the check is what the
// standing rule forbids, so a check that could not run offers no way to file.
func TestFailedDuplicateCheckOpensNoChooser(t *testing.T) {
	var m knowledgeModel
	m, cmd := m.update(ticketCheckedMsg{err: os.ErrNotExist})
	if m.filing != nil {
		t.Error("a failed duplicate check opened the chooser")
	}
	if cmd == nil {
		t.Error("a failed duplicate check reported nothing")
	}
}

// TestChooserKeysStayInTheChooser: esc and q cancel the chooser rather than
// closing the overlay under it.
func TestChooserKeysStayInTheChooser(t *testing.T) {
	a := App{overlay: overlayKnowledge}
	a.knowledge.filing = &ticketFiling{art: Artifact{ID: "x", Type: ticketType}}

	m, _ := a.Update(keyPress("esc"))
	app := m.(App)
	if app.overlay != overlayKnowledge {
		t.Error("esc in the chooser closed the knowledge overlay")
	}
	if app.knowledge.filing != nil {
		t.Error("esc did not cancel the chooser")
	}
}

// TestUnarchivedFilingReturnsItsCount: a filing that never reached the archive
// has no deferred record to bring the count down, so its message does.
func TestUnarchivedFilingReturnsItsCount(t *testing.T) {
	a := App{}
	a.knowledge.pendingCommits = 1
	m, _ := a.Update(ticketFiledMsg{status: "file failed: tk: refused"})
	if pending := m.(App).knowledge.pendingCommits; pending != 0 {
		t.Errorf("pendingCommits = %d after a filing that archived nothing, want 0", pending)
	}
}

// TestInFlightFilingRefusesASecond: a confirmed candidate stays listed until
// its archive lands, so p on it — or a chooser opened before the first confirm
// — must not reach tk again until its ticketFiledMsg arrives.
func TestInFlightFilingRefusesASecond(t *testing.T) {
	art := Artifact{ID: "x", Type: ticketType, Status: "candidate", Path: "/store/_candidates/tickets/ticket/x.md", Title: "X breaks"}
	a := App{overlay: overlayKnowledge}
	a.knowledge.setArtifacts([]Artifact{art})
	a.knowledge.filing = &ticketFiling{art: art}
	stale := *a.knowledge.filing

	m, cmd := a.Update(keyPress("enter"))
	a = m.(App)
	if cmd == nil || !a.knowledge.inFlight[art.Path] {
		t.Fatal("confirming did not record the filing as in flight")
	}
	if got := msgOf[statusMsg](t, cmd); got != "filing X breaks…" {
		t.Errorf("confirm status = %q, want the filing line", got)
	}

	m, cmd = a.Update(keyPress("p"))
	a = m.(App)
	msgs := drainCmd(cmd)
	for _, msg := range msgs {
		if _, ok := msg.(ticketCheckedMsg); ok {
			t.Fatal("p on an in-flight candidate started another duplicate check")
		}
	}
	if len(msgs) != 1 || msgs[0] != statusMsg("already filing X breaks") {
		t.Errorf("p on an in-flight candidate = %v, want the refusal", msgs)
	}

	a.knowledge.filing = &stale
	m, cmd = a.Update(keyPress("enter"))
	a = m.(App)
	if a.knowledge.pendingCommits != 1 {
		t.Errorf("pendingCommits = %d after a refused second confirm, want 1", a.knowledge.pendingCommits)
	}
	if got := msgOf[statusMsg](t, cmd); got != "already filing X breaks" {
		t.Errorf("second confirm status = %q, want the refusal", got)
	}

	m, _ = a.Update(ticketFiledMsg{path: art.Path, status: "file failed: tk: refused"})
	if m.(App).knowledge.inFlight[art.Path] {
		t.Error("the filing's message did not clear it from in flight")
	}
}

// TestFilingStripsTerminalControls: the chooser renders the candidate, which is
// extractor-written, beside titles and diagnostics read from the shared tk
// store, and the filing reports back through the status line; a control in any
// of them reaches the terminal unless it is stripped, as the run screens do.
func TestFilingStripsTerminalControls(t *testing.T) {
	const osc, csi = "\x1b]52;c;AAAA\x07", "\x1b[2J"
	hasControl := func(s string) bool {
		plain := stripANSI(s) // lipgloss's own SGR is expected; anything else is injected
		return strings.ContainsAny(plain, "\x1b\x07\x9b") || strings.Contains(plain, "]52;c;AAAA") || strings.Contains(plain, "[2J")
	}
	t.Setenv("LOOM_KNOWLEDGE_ROOT", t.TempDir())
	art := Artifact{
		ID: "x", Type: ticketType, Status: "candidate", TicketType: "bug",
		Scope: "ticket" + osc, Title: "walkup" + csi + " breaks", Path: "/nosuch/x" + osc + ".md",
	}
	m := knowledgeModel{width: 160}
	m, _ = m.update(ticketCheckedMsg{art: art, check: ticketCheck{
		matches:    []ticketMatch{{ID: "ticket/t-1" + csi, Status: "open" + osc, Title: "dup" + osc + " title"}},
		incomplete: "skipped" + csi + " file",
	}})
	v := m.view()
	for _, want := range []string{"File bug in ticket: walkup breaks", "/nosuch/x.md", "possible duplicates in ticket", "skipped file", "add a note to ticket/t-1 [open] dup title"} {
		if !strings.Contains(stripANSI(v), want) {
			t.Errorf("chooser lost %q around a stripped control:\n%s", want, v)
		}
	}
	if hasControl(v) {
		t.Errorf("chooser carries a terminal control:\n%q", v)
	}

	// The candidate file does not exist, so the filing fails on its path
	// before tk is reached and reports that path back.
	_, cmd := m.update(keyPress("enter"))
	failed := false
	for _, msg := range drainCmd(cmd) {
		var status string
		switch msg := msg.(type) {
		case statusMsg:
			status = string(msg)
		case ticketFiledMsg:
			status = msg.status
			failed = strings.Contains(status, "file failed: stat /nosuch/x.md")
		default:
			continue
		}
		if hasControl(status) {
			t.Errorf("filing status carries a terminal control: %q", status)
		}
	}
	if !failed {
		t.Error("the filing did not report its failure on the candidate's path")
	}

	_, cmd = knowledgeModel{}.update(ticketCheckedMsg{err: errors.New("read " + osc)})
	if got := msgOf[statusMsg](t, cmd); hasControl(string(got)) || !strings.Contains(string(got), "duplicate check failed: read") {
		t.Errorf("check failure status = %q", got)
	}
}
