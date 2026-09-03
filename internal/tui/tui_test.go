package tui

import (
	"path/filepath"
	"reflect"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"loom/internal/knowledge/store"
)

// quitKey is the dashboard's quit gesture, the one a review session ends with.
var quitKey = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")}

// TestQuitWaitsForAPendingCommit: bubbletea does not wait on a Cmd's goroutine
// and cmd/loom returns as soon as Run does, so quitting while a gesture's
// deferred commit is still running would leave its already-moved file as
// working-tree state nothing records. The quit is held until the commit reports.
func TestQuitWaitsForAPendingCommit(t *testing.T) {
	a := App{}
	a.knowledge.pendingCommits = 1

	m, _ := a.Update(quitKey)
	app := m.(App)
	if !app.quitting {
		t.Fatal("q with a commit in flight left immediately")
	}
	if app.status == "" {
		t.Error("the wait is not reported on the status line")
	}

	m, cmd := app.Update(knowledgeCommittedMsg{status: "promoted → x", warn: store.Warn{}})
	if pending := m.(App).knowledge.pendingCommits; pending != 0 {
		t.Errorf("pendingCommits = %d after the commit reported", pending)
	}
	if cmd == nil {
		t.Fatal("the drained commit did not quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Error("the drained commit did not quit")
	}
}

// TestSecondQuitLeavesAnyway: the wait is bounded only by git, so a user who
// presses twice has decided.
func TestSecondQuitLeavesAnyway(t *testing.T) {
	a := App{quitting: true}
	a.knowledge.pendingCommits = 1

	_, cmd := a.Update(quitKey)
	if cmd == nil {
		t.Fatal("a second q did not quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Error("a second q did not quit")
	}
}

// TestQuitHoldsTheDrainLineForTheWholeWait: the wait is bounded by git, not by
// the status bar's timer. A line that cleared partway through would put the help
// footer back under a TUI that is silently refusing to exit — which is what
// pushes the user into the second q that kills the commit.
func TestQuitHoldsTheDrainLineForTheWholeWait(t *testing.T) {
	a := App{}
	a.knowledge.pendingCommits = 1

	m, cmd := a.Update(quitKey)
	app := m.(App)
	if app.status != quitDrainStatus {
		t.Fatalf("status = %q after a held q, want the drain line", app.status)
	}
	if cmd != nil {
		t.Errorf("the held q armed %T, so the drain line clears itself mid-wait", cmd())
	}
	// The clear armed for whatever line preceded the drain's is still pending.
	m, _ = app.Update(clearStatusMsg(app.statusGen - 1))
	if got := m.(App).status; got != quitDrainStatus {
		t.Errorf("an earlier line's clear blanked the drain line: status = %q", got)
	}
}

// TestAnotherKeyWithdrawsAHeldQuit: a user who goes back to work after pressing
// q has withdrawn it. Leaving the flag set would suppress every further
// gesture's warn and exit from under them when the count next reached zero.
func TestAnotherKeyWithdrawsAHeldQuit(t *testing.T) {
	a := App{quitting: true, status: quitDrainStatus}
	a.knowledge.pendingCommits = 1

	m, _ := a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
	app := m.(App)
	if app.quitting {
		t.Error("a key other than q left the quit held")
	}
	if app.status != "" {
		t.Errorf("status = %q after the quit was withdrawn, want the drain line down", app.status)
	}

	_, cmd := app.Update(knowledgeCommittedMsg{status: "promoted → x", warn: store.Warn{}})
	if cmd != nil {
		if _, ok := cmd().(tea.QuitMsg); ok {
			t.Error("the drain quit after the quit was withdrawn")
		}
	}
}

// TestPromoteCountsItsPendingCommit: the drain is only as good as the count the
// gesture keeps, and the quit tests above set it by hand. Drives the gesture
// itself against a git store, so a promote that stopped counting its deferred
// commit — leaving the quit free to kill it mid-git — fails here.
func TestPromoteCountsItsPendingCommit(t *testing.T) {
	_, art := seedGitCandidate(t)

	var m knowledgeModel
	m.setArtifacts([]Artifact{art})

	m, cmd := m.promote()
	if m.pendingCommits != 1 {
		t.Errorf("pendingCommits = %d after a promote, want 1", m.pendingCommits)
	}
	var committed bool
	for _, msg := range drainCmd(cmd) {
		if _, ok := msg.(knowledgeCommittedMsg); ok {
			committed = true
		}
	}
	if !committed {
		t.Error("the promote dispatched no deferred commit, so nothing will decrement the count")
	}
}

// TestRejectCountsItsPendingCommit mirrors the promote case: a reject that
// stopped counting its deferred commit would let q kill it mid-git with the
// archive move already on disk.
func TestRejectCountsItsPendingCommit(t *testing.T) {
	_, art := seedGitCandidate(t)

	var m knowledgeModel
	m.setArtifacts([]Artifact{art})

	m, cmd := m.reject()
	if m.pendingCommits != 1 {
		t.Errorf("pendingCommits = %d after a reject, want 1", m.pendingCommits)
	}
	var committed bool
	for _, msg := range drainCmd(cmd) {
		if _, ok := msg.(knowledgeCommittedMsg); ok {
			committed = true
		}
	}
	if !committed {
		t.Error("the reject dispatched no deferred commit, so nothing will decrement the count")
	}
}

// TestEditCountsItsPendingCommit: the edit commits inside ExecProcess's
// callback, which runs off the update loop like a gesture's commit, so the drain
// has to cover it too.
func TestEditCountsItsPendingCommit(t *testing.T) {
	t.Setenv("VISUAL", "true")

	var m knowledgeModel
	m.setArtifacts([]Artifact{{ID: "x", Path: filepath.Join(t.TempDir(), "x.md")}})

	m, cmd := m.edit()
	if m.pendingCommits != 1 {
		t.Errorf("pendingCommits = %d after an edit, want 1", m.pendingCommits)
	}
	if cmd == nil {
		t.Error("the edit dispatched no editor, so nothing will decrement the count")
	}
}

// drainCmd runs cmd and everything it fans out to, collecting the leaf messages.
// Reflection because bubbletea carries a Batch's and a Sequence's members alike
// as a []tea.Cmd message and the sequence's type is unexported.
func drainCmd(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	v := reflect.ValueOf(msg)
	if v.Kind() != reflect.Slice || v.Type().Elem() != reflect.TypeOf(tea.Cmd(nil)) {
		return []tea.Msg{msg}
	}
	var msgs []tea.Msg
	for i := 0; i < v.Len(); i++ {
		msgs = append(msgs, drainCmd(v.Index(i).Interface().(tea.Cmd))...)
	}
	return msgs
}
