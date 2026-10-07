package tui

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/EnderRealm/ticket/v8/pkg/ticket"

	"loom/internal/knowledge/store"
)

// ticketType is the artifact type of a candidate whose destination is tk rather
// than a validated tree in the knowledge store.
const ticketType = "ticket"

// ticketMatchMax bounds the duplicates the chooser offers. tk's search scores
// any shared term, so every ticket in a busy namespace matches a little; the
// head of the ranking is what a reviewer can actually judge.
const ticketMatchMax = 5

// ticketMatch is one existing ticket a candidate may duplicate.
type ticketMatch struct {
	ID     string // qualified project/id
	Status string
	Title  string
}

// ticketCheck is the duplicate check's answer for one candidate. incomplete
// names what the tk read could not see, so a check that may have missed a
// duplicate is not presented as one that found none.
type ticketCheck struct {
	matches    []ticketMatch
	incomplete string
}

// checkTicketDuplicates is the duplicate check that precedes every filing: the
// candidate's title ranked against every ticket in its scope's namespace, done
// and closed included, so a defect already fixed or already refused is seen
// before it is filed again. It is the ranking `tk search` prints, read through
// the library behind it rather than parsed back out of that table.
func checkTicketDuplicates(a Artifact) (ticketCheck, error) {
	snap, err := LoadTicketSnapshot()
	if err != nil {
		return ticketCheck{}, err
	}
	var tickets []*ticket.Ticket
	for _, t := range snap.Tickets {
		if ns, _ := ticket.ParseNamespacedID(t.ID); ns == a.Scope {
			tickets = append(tickets, t)
		}
	}
	var check ticketCheck
	for _, r := range ticket.Search(tickets, a.Title) {
		if len(check.matches) == ticketMatchMax {
			break
		}
		check.matches = append(check.matches, ticketMatch{
			ID: r.Ticket.ID, Status: string(r.Ticket.Status), Title: r.Ticket.Title,
		})
	}
	if !snap.Complete {
		check.incomplete = strings.Join(snap.Diagnostics(), "; ")
	}
	return check, nil
}

// fileTicketCandidate files a ticket candidate into tk — a new ticket in the
// candidate's scope when target is empty, otherwise a note on target, the
// duplicate the reviewer chose — then archives the candidate under
// _candidates/_filed/ and records the filing in log.md, deferring the commit
// as reject does. Returns the ticket the candidate landed on and the deferred
// Commit.
//
// tk is written first and the store second, and the two cannot be one unit.
// A failed tk write leaves the candidate in place and returns no id. A tk
// write followed by an archive that never landed returns the id with the
// error and a nil Commit: the ticket exists and the candidate is still listed,
// so the next attempt's duplicate check surfaces that ticket rather than
// filing a second one.
func fileTicketCandidate(a Artifact, target string) (string, store.Commit, error) {
	if a.Type != ticketType || a.Status != "candidate" {
		return "", nil, fmt.Errorf("not a ticket candidate")
	}
	if a.TicketType != string(ticket.TypeBug) && a.TicketType != string(ticket.TypeFeature) {
		return "", nil, fmt.Errorf("ticket_type %q is not bug or feature", a.TicketType)
	}
	if strings.TrimSpace(a.Title) == "" {
		return "", nil, fmt.Errorf("candidate has no title")
	}
	root := KnowledgeRoot()
	dest := filepath.Join(root, "_candidates", "_filed", "tickets", a.Scope, filepath.Base(a.Path))
	// Both checked before tk is touched: either would otherwise surface only
	// after a ticket exists for a candidate that cannot be archived.
	if _, err := os.Stat(a.Path); err != nil {
		return "", nil, err
	}
	if _, err := os.Stat(dest); err == nil {
		return "", nil, fmt.Errorf("%s already exists", shortenPath(dest))
	}

	gesture, done, id := "file", "filed", target
	if target == "" {
		created, err := createTicket(a)
		if err != nil {
			return "", nil, err
		}
		id = created
	} else {
		gesture, done = "note", "noted on"
		if err := noteTicket(a, target); err != nil {
			return "", nil, err
		}
	}

	logPath := filepath.Join(root, "log.md")
	archived := false
	commit, err := store.ApplyDeferred(root, gestureMessage(gesture, a), func(tx *store.Tx) error {
		if err := tx.Rename(a.Path, dest); err != nil {
			return err
		}
		archived = true
		tx.Droppable(dest)
		return tx.Append(logPath, fileLogEntry(gesture, a, id))
	})
	if err != nil {
		if !archived {
			return id, nil, fmt.Errorf("%s %s, but the candidate was not archived: %s", done, id, store.ShortReason(err))
		}
		return id, withReason(commit, store.ShortReason(err)), nil
	}
	return id, commit, nil
}

// createTicket runs `tk create` in the candidate's namespace and returns the
// new ticket's qualified id. tk prints the created ticket as its document, and
// the id is read from that document's frontmatter.
func createTicket(a Artifact) (string, error) {
	// --project= keeps the scope from ever being read as a flag, and `--` does
	// the same for a title that begins with a dash.
	out, err := runTK("--project="+a.Scope, "create", "-t", a.TicketType,
		"-d", ticketText(a, ""), "--", a.Title)
	if err != nil {
		return "", err
	}
	lines := strings.Split(string(out), "\n")
	if len(lines) < 2 || lines[0] != "---" || !strings.HasPrefix(lines[1], "id: ") {
		return "", fmt.Errorf("tk create printed no ticket id — check %s for the ticket before filing again", a.Scope)
	}
	return a.Scope + "/" + strings.TrimSpace(strings.TrimPrefix(lines[1], "id: ")), nil
}

// noteTicket appends the candidate to an existing ticket as a note, which is
// the project's standing rule for a duplicate: add context to the ticket that
// exists rather than file another.
func noteTicket(a Artifact, target string) error {
	ns, bare := ticket.ParseNamespacedID(target)
	if ns == "" {
		ns = a.Scope
	}
	lead := fmt.Sprintf("Also reported by loom ticket candidate `%s`: %s", a.ID, a.Title)
	_, err := runTK("--project="+ns, "add-note", "--", bare, ticketText(a, lead))
	return err
}

// ticketText is the body a candidate files as — the ticket's description, or
// with a lead line a note — closing with the sessions that surfaced it, which
// is the filed ticket's only link back to the evidence.
func ticketText(a Artifact, lead string) string {
	var b strings.Builder
	if lead != "" {
		b.WriteString(lead + "\n\n")
	}
	if body := candidateBody(a.Body); body != "" {
		b.WriteString(body + "\n\n")
	}
	if len(a.EvidencePaths) > 0 {
		b.WriteString("Evidence:\n")
		for _, p := range a.EvidencePaths {
			b.WriteString("- " + p + "\n")
		}
		b.WriteString("\n")
	}
	b.WriteString("Sources: loom ticket candidate `" + a.ID + "`, from session(s):\n")
	for _, s := range a.Sessions {
		b.WriteString("- " + s + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// candidateBody is the markdown after a candidate's frontmatter.
func candidateBody(body string) string {
	lines := strings.Split(body, "\n")
	if len(lines) < 2 || strings.TrimSpace(lines[0]) != "---" {
		return strings.TrimSpace(body)
	}
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			return strings.TrimSpace(strings.Join(lines[i+1:], "\n"))
		}
	}
	return ""
}

// runTK runs the tk CLI with args as argv — never through a shell — and
// returns its stdout. stderr is folded into the error rather than forwarded:
// the TUI owns the terminal, and the status line is where a refusal is read.
func runTK(args ...string) ([]byte, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.Command("tk", args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("tk: %s", firstLine(msg))
		}
		return nil, fmt.Errorf("tk: %w", err)
	}
	return stdout.Bytes(), nil
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

// fileLogFormat is the store's log.md convention for a filing: the gesture
// (file or note), the candidate, and the ticket it landed on.
const fileLogFormat = "## [%s] %s %s | %s | ticket candidate %s → %s"

// Bounds for the two fields a filing adds to a reject's. The gesture is one of
// two constants. A ticket id is bounded per half, since the allow-list would
// rewrite the separator between them: the namespace at the scope's bound, the
// id at what is left of logFieldTicketMax once the "/" is counted.
const (
	logFieldGestureMax = 4
	logFieldTicketMax  = 60
)

var fileLogFixedRunes = len([]rune(fmt.Sprintf(fileLogFormat, "2006-01-02", "", "", "", "", "")))

// fileLogBaseMax is derived like logFieldBaseMax, so the worst-case entry
// still fits store.MessageMax.
var fileLogBaseMax = store.MessageMax - fileLogFixedRunes - logFieldGestureMax - logFieldIDMax -
	logFieldScopeMax - logFieldTicketMax

// fileLogEntry renders a filing in the store's log.md convention. It is the
// record that the candidate became a ticket, as the reject entry is the record
// that one was refused; the basename says which sibling, as it does there.
func fileLogEntry(gesture string, a Artifact, ticketID string) string {
	entry := fmt.Sprintf(fileLogFormat,
		time.Now().Format("2006-01-02"),
		logField(gesture, logFieldGestureMax),
		logField(a.ID, logFieldIDMax),
		logField(a.Scope, logFieldScopeMax),
		logFieldTail(filepath.Base(a.Path), fileLogBaseMax),
		logTicketField(ticketID))
	return "\n" + store.SanitizeRecord(entry) + "\n"
}

// logTicketField bounds a qualified ticket id to logFieldTicketMax runes,
// each half through the allow-list on its own.
func logTicketField(id string) string {
	ns, bare := ticket.ParseNamespacedID(id)
	return logField(ns, logFieldScopeMax) + "/" + logField(bare, logFieldTicketMax-logFieldScopeMax-1)
}

// ----- TUI: the duplicate-check chooser -----

// ticketFiling is the chooser open over a ticket candidate: row 0 files a new
// ticket, row i adds a note to matches[i-1].
type ticketFiling struct {
	art    Artifact
	check  ticketCheck
	cursor int
}

// ticketCheckedMsg is the duplicate check returning for art.
type ticketCheckedMsg struct {
	art   Artifact
	check ticketCheck
	err   error
}

// ticketFiledMsg is a filing returning. commit is the deferred record of the
// archive, nil when the archive never landed — the tk write alone, or nothing.
type ticketFiledMsg struct {
	path   string
	status string
	commit store.Commit
}

func checkTicketCmd(a Artifact) tea.Cmd {
	return func() tea.Msg {
		check, err := checkTicketDuplicates(a)
		return ticketCheckedMsg{art: a, check: check, err: err}
	}
}

// fileTicketCmd runs the filing off the update loop: tk and the archive move
// are each a process or a filesystem walk, and the frame would freeze for both.
func fileTicketCmd(a Artifact, target string) tea.Cmd {
	return func() tea.Msg {
		id, commit, err := fileTicketCandidate(a, target)
		switch {
		case err != nil && id == "" && target == "":
			return ticketFiledMsg{path: a.Path, status: sanitize("file failed: " + err.Error())}
		case err != nil && id == "":
			return ticketFiledMsg{path: a.Path, status: sanitize("note failed: " + err.Error())}
		case err != nil:
			return ticketFiledMsg{path: a.Path, status: sanitize(err.Error())}
		case target == "":
			return ticketFiledMsg{path: a.Path, status: sanitize("filed " + id + " — archived to _filed/"), commit: commit}
		}
		return ticketFiledMsg{path: a.Path, status: sanitize("noted on " + id + " — archived to _filed/"), commit: commit}
	}
}

// openFiling opens the chooser once the duplicate check has answered. A check
// that failed opens nothing: filing without one is what the standing rule
// forbids.
func (m knowledgeModel) openFiling(msg ticketCheckedMsg) (knowledgeModel, tea.Cmd) {
	if msg.err != nil {
		return m, statusCmd(sanitize("duplicate check failed: " + msg.err.Error()))
	}
	m.filing = &ticketFiling{art: msg.art, check: msg.check}
	return m, nil
}

func (m knowledgeModel) updateFiling(km tea.KeyMsg) (knowledgeModel, tea.Cmd) {
	f := *m.filing
	switch km.String() {
	case "esc", "q":
		m.filing = nil
		return m, statusCmd("filing cancelled")
	case "up", "k":
		if f.cursor > 0 {
			f.cursor--
		}
	case "down", "j":
		if f.cursor < len(f.check.matches) {
			f.cursor++
		}
	case "enter":
		target := ""
		if f.cursor > 0 {
			target = f.check.matches[f.cursor-1].ID
		}
		m.filing = nil
		m.showDetail = false
		// A chooser opened before an earlier confirm on the same candidate would
		// otherwise file it twice.
		if m.inFlight[f.art.Path] {
			return m, statusCmd(sanitize("already filing " + f.art.Title))
		}
		if m.inFlight == nil {
			m.inFlight = map[string]bool{}
		}
		m.inFlight[f.art.Path] = true
		// Counted from here, not from the commit: a quit while tk is writing
		// would otherwise abandon the archive the ticket is waiting on.
		m.pendingCommits++
		// Sequenced so the filing's own outcome always replaces this line.
		return m, tea.Sequence(statusCmd(sanitize("filing "+f.art.Title+"…")), fileTicketCmd(f.art, target))
	}
	m.filing = &f
	return m, nil
}

func (m knowledgeModel) filingView() string {
	f := m.filing
	boxWidth := m.width - 4
	if boxWidth < 40 {
		boxWidth = 40
	}
	var b strings.Builder
	// Every field here is data: the candidate is extractor-written and the
	// matches and diagnostics come from the shared tk store, so each line is
	// stripped of terminal controls before it is truncated or styled.
	b.WriteString(StyleSection.Render(sanitize("File " + f.art.TicketType + " in " + f.art.Scope + ": " + f.art.Title)))
	b.WriteString("\n")
	b.WriteString(StyleDim.Render(sanitize(shortenPath(f.art.Path))))
	b.WriteString("\n\n")
	if len(f.check.matches) == 0 {
		b.WriteString(StyleDim.Render(sanitize("no existing " + f.art.Scope + " ticket matches the title")))
	} else {
		b.WriteString(StyleWarning.Render(sanitize("possible duplicates in " + f.art.Scope + ", best first")))
	}
	b.WriteString("\n")
	if f.check.incomplete != "" {
		b.WriteString(StyleWarning.Render("! tk store read incomplete — a duplicate may be missing: " + sanitize(f.check.incomplete)))
		b.WriteString("\n")
	}
	b.WriteString("\n")
	rows := []string{"file a new ticket"}
	for _, t := range f.check.matches {
		rows = append(rows, sanitize("add a note to "+t.ID+" ["+t.Status+"] "+t.Title))
	}
	for i, row := range rows {
		prefix := "  "
		if i == f.cursor {
			prefix = "> "
		}
		b.WriteString(truncate(prefix+row, boxWidth-4))
		b.WriteString("\n")
	}
	return StyleOverlayBorder.Width(boxWidth).Render(b.String())
}
