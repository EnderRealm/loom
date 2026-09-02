package tui

import (
	"os"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"loom/internal/knowledge"
	"loom/internal/knowledge/store"
)

// Artifact is the corpus artifact shape, owned by the knowledge package.
// Aliased here so the TUI model code keeps referring to a local type.
type Artifact = knowledge.Artifact

// KnowledgeRoot and LoadKnowledge wrap the knowledge package so the TUI
// keeps its existing call sites.
func KnowledgeRoot() string { return knowledge.Root() }

func LoadKnowledge() ([]Artifact, error) { return knowledge.Load() }

// ----- TUI model -----

type knowledgeModel struct {
	artifacts []Artifact
	cursor    int
	offset    int
	width     int
	height    int

	showDetail   bool // sub-view: full body of selected artifact
	detailScroll int  // line offset into selected.Body when showDetail
}

func (m *knowledgeModel) setSize(w, h int) {
	m.width = w
	m.height = h
	m.clampOffset()
}

func (m *knowledgeModel) setArtifacts(a []Artifact) {
	var keepID string
	if s := m.selected(); s != nil {
		keepID = s.ID + "|" + s.Path
	}
	m.artifacts = a
	if keepID != "" {
		for i, x := range m.artifacts {
			if x.ID+"|"+x.Path == keepID {
				m.cursor = i
				break
			}
		}
	}
	if m.cursor >= len(m.artifacts) {
		m.cursor = max(0, len(m.artifacts)-1)
	}
	m.clampOffset()
}

func (m knowledgeModel) selected() *Artifact {
	if m.cursor >= 0 && m.cursor < len(m.artifacts) {
		return &m.artifacts[m.cursor]
	}
	return nil
}

func (m knowledgeModel) visibleRows() int {
	rows := m.height - 3 // header + footer counts
	if rows < 1 {
		rows = 1
	}
	return rows
}

func (m *knowledgeModel) clampOffset() {
	visible := m.visibleRows()
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+visible {
		m.offset = m.cursor - visible + 1
	}
	if m.offset < 0 {
		m.offset = 0
	}
}

func (m knowledgeModel) update(msg tea.Msg) (knowledgeModel, tea.Cmd) {
	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	if m.showDetail {
		switch km.String() {
		case "esc", "q", "backspace":
			m.showDetail = false
			m.detailScroll = 0
		case "p":
			return m.promote()
		case "x":
			return m.reject()
		case "e":
			return m.edit()
		case "up", "k":
			if m.detailScroll > 0 {
				m.detailScroll--
			}
		case "down", "j":
			m.detailScroll++
		case "pgup":
			m.detailScroll -= m.detailRows()
			if m.detailScroll < 0 {
				m.detailScroll = 0
			}
		case "pgdown":
			m.detailScroll += m.detailRows()
		case "g":
			m.detailScroll = 0
		}
		return m, nil
	}
	switch km.String() {
	case "enter", "v", "right", "l":
		if m.selected() != nil {
			m.showDetail = true
			m.detailScroll = 0
		}
	case "p":
		return m.promote()
	case "x":
		return m.reject()
	case "e":
		return m.edit()
	case "s":
		// Skip: leave the candidate in place, advance to the next row.
		if m.cursor < len(m.artifacts)-1 {
			m.cursor++
			m.clampOffset()
		}
		return m, statusCmd("skipped")
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
			m.clampOffset()
		}
	case "down", "j":
		if m.cursor < len(m.artifacts)-1 {
			m.cursor++
			m.clampOffset()
		}
	case "pgup":
		m.cursor -= m.visibleRows()
		if m.cursor < 0 {
			m.cursor = 0
		}
		m.clampOffset()
	case "pgdown":
		m.cursor += m.visibleRows()
		if m.cursor > len(m.artifacts)-1 {
			m.cursor = max(0, len(m.artifacts)-1)
		}
		m.clampOffset()
	case "g":
		m.cursor = 0
		m.clampOffset()
	case "G":
		m.cursor = max(0, len(m.artifacts)-1)
		m.clampOffset()
	}
	return m, nil
}

// promote and reject act on the selected candidate; both close the detail
// sub-view and trigger a reload so the moved file leaves the list. edit shells
// out to $EDITOR for any selected artifact, records what it wrote, and reloads
// on return.
func (m knowledgeModel) promote() (knowledgeModel, tea.Cmd) {
	a := m.selected()
	if a == nil {
		return m, nil
	}
	if a.Status != "candidate" {
		return m, statusCmd("only candidates can be promoted")
	}
	dest, warn, err := promoteCandidate(*a)
	if err != nil {
		return m, statusCmd("promote failed: " + err.Error())
	}
	m.showDetail = false
	status := "promoted → " + shortenPath(dest)
	if warn.NotCommitted != "" {
		status += " — not committed: " + warn.NotCommitted
	}
	if warn.NotPushed != "" {
		status += " — not pushed: " + warn.NotPushed
	}
	return m, tea.Batch(statusCmd(status), loadKnowledgeCmd())
}

func (m knowledgeModel) reject() (knowledgeModel, tea.Cmd) {
	a := m.selected()
	if a == nil {
		return m, nil
	}
	if a.Status != "candidate" {
		return m, statusCmd("only candidates can be rejected")
	}
	_, warn, err := rejectCandidate(*a)
	if err != nil {
		return m, statusCmd("reject failed: " + err.Error())
	}
	m.showDetail = false
	status := "rejected — archived to _rejected/"
	if warn.NotCommitted != "" {
		// Not "not committed": the reject's record can also fail to be written
		// at all, when the store has no log.md to append the decision to.
		status += " — record not saved: " + warn.NotCommitted
	}
	if warn.NotPushed != "" {
		// The record exists in the store's history and only the publication is
		// missing, which the next gesture's push carries unless the remote has
		// diverged.
		status += " — not pushed: " + warn.NotPushed
	}
	return m, tea.Batch(statusCmd(status), loadKnowledgeCmd())
}

func (m knowledgeModel) edit() (knowledgeModel, tea.Cmd) {
	a := m.selected()
	if a == nil {
		return m, nil
	}
	editor := os.Getenv("VISUAL")
	if editor == "" {
		editor = os.Getenv("EDITOR")
	}
	if editor == "" {
		return m, statusCmd("$VISUAL/$EDITOR not set")
	}
	cmd := exec.Command(editor, a.Path)
	edited := *a
	return m, tea.ExecProcess(cmd, func(err error) tea.Msg {
		if err != nil {
			return statusMsg("editor exited: " + err.Error())
		}
		warn := commitEdit(edited)
		arts, e := LoadKnowledge()
		if e != nil {
			return errMsg(e)
		}
		return knowledgeEditedMsg{artifacts: arts, warn: warn}
	})
}

// commitEdit records an artifact $EDITOR has just written. The file was written
// outside the store's Tx, so the path is declared rather than written and the
// commit is the store's either way — which is what keeps the edit from being a
// gesture with commit code of its own. Returns the zero Warn or the reason the
// commit did not land or was not published, which degrades like every other
// gesture: a status-line reason, the whole failure in knowledge-git.log, and
// never a block on the reload. An editor quit without saving leaves nothing to
// commit and is not a failure; the store decides that, since a declared path
// that did not change is not an edit-only case.
func commitEdit(a Artifact) store.Warn {
	warn, err := store.Apply(gestureMessage("edit", a), func(tx *store.Tx) error {
		return tx.Touch(a.Path)
	})
	if err != nil {
		// The store refused to record the path at all — it is not in the store,
		// or not content. Nothing was committed, so this degrades onto the status
		// line like a failed commit rather than reading as a clean edit.
		return store.Warn{NotCommitted: store.ShortReason(err)}
	}
	return warn
}

func (m knowledgeModel) detailRows() int {
	r := m.height - 4
	if r < 1 {
		r = 1
	}
	return r
}

const (
	colKnowStatus = 11
	colKnowType   = 9
	colKnowScope  = 10
	colKnowID     = 44
	colKnowAge    = 6
)

func (m knowledgeModel) view() string {
	if m.showDetail {
		return m.detailView()
	}
	return m.listView()
}

func (m knowledgeModel) listView() string {
	if m.width == 0 || m.height == 0 {
		return ""
	}
	if len(m.artifacts) == 0 {
		return StyleDim.Render("  no artifacts under " + KnowledgeRoot())
	}

	var b strings.Builder
	b.WriteString("  ")
	b.WriteString(padRight(StyleColHeader.Render("STATUS"), colKnowStatus))
	b.WriteString(padRight(StyleColHeader.Render("TYPE"), colKnowType))
	b.WriteString(padRight(StyleColHeader.Render("SCOPE"), colKnowScope))
	b.WriteString(padRight(StyleColHeader.Render("ID"), colKnowID))
	b.WriteString(padRight(StyleColHeader.Render("AGE"), colKnowAge))
	b.WriteString(StyleColHeader.Render("TITLE"))
	b.WriteString("\n")

	visible := m.visibleRows()
	end := m.offset + visible
	if end > len(m.artifacts) {
		end = len(m.artifacts)
	}
	for i := m.offset; i < end; i++ {
		b.WriteString(m.renderRow(m.artifacts[i], i == m.cursor))
		b.WriteString("\n")
	}
	for i := end - m.offset; i < visible; i++ {
		b.WriteString("\n")
	}

	cValid, cCand := 0, 0
	for _, a := range m.artifacts {
		if a.Status == "candidate" {
			cCand++
		} else {
			cValid++
		}
	}
	footer := StyleDim.Render("  ") + StyleSuccess.Render(itoa(cValid)) +
		StyleDim.Render(" validated · ") + StyleWarning.Render(itoa(cCand)) +
		StyleDim.Render(" candidate(s)")
	b.WriteString(footer)
	return b.String()
}

func (m knowledgeModel) renderRow(a Artifact, selected bool) string {
	selBg := lipgloss.NewStyle()
	if selected {
		selBg = lipgloss.NewStyle().Background(colorSurface)
	}
	var bg *lipgloss.Style
	if selected {
		bg = &selBg
	}

	var statusCell string
	if a.Status == "candidate" {
		statusCell = padRightBg(selBg.Foreground(colorWarning).Render("candidate"), colKnowStatus, bg)
	} else {
		statusCell = padRightBg(selBg.Foreground(colorSuccess).Render("validated"), colKnowStatus, bg)
	}

	typeCell := padRightBg(selBg.Foreground(colorAccent).Render(a.Type), colKnowType, bg)
	scopeCell := padRightBg(selBg.Foreground(colorInfo).Render(truncate(a.Scope, colKnowScope-1)), colKnowScope, bg)
	idCell := padRightBg(selBg.Foreground(colorWhite).Render(truncate(a.ID, colKnowID-1)), colKnowID, bg)

	// AGE is time since the fact was first noticed (earliest source date),
	// falling back to the file mtime when no source carried a usable date.
	var ageCell string
	basis := a.AgeBasis()
	if basis.IsZero() {
		ageCell = padRightBg(selBg.Foreground(colorMuted).Render("—"), colKnowAge, bg)
	} else {
		ageCell = padRightBg(selBg.Foreground(colorGray).Render(humanDuration(time.Since(basis))), colKnowAge, bg)
	}

	titleW := m.width - (2 + colKnowStatus + colKnowType + colKnowScope + colKnowID + colKnowAge)
	if titleW < 10 {
		titleW = 10
	}
	titleCell := selBg.Foreground(colorGray).Render(truncate(a.Title, titleW))

	sp := "  "
	if selected {
		sp = selBg.Render("  ")
	}
	line := sp + statusCell + typeCell + scopeCell + idCell + ageCell + titleCell
	if selected && m.width > 0 {
		rendered := lipgloss.Width(line)
		if rendered < m.width {
			line += selBg.Render(strings.Repeat(" ", m.width-rendered))
		}
	}
	return line
}

func (m knowledgeModel) detailView() string {
	a := m.selected()
	if a == nil {
		return ""
	}
	boxWidth := m.width - 4
	if boxWidth < 40 {
		boxWidth = 40
	}

	var b strings.Builder
	b.WriteString(StyleSection.Render(a.Title))
	b.WriteString("\n")
	b.WriteString(StyleDim.Render(a.Status + " · " + a.Type + " · " + a.Scope + " · " + a.ID))
	b.WriteString("\n")
	b.WriteString(StyleDim.Render(shortenPath(a.Path)))
	b.WriteString("\n\n")

	bodyLines := strings.Split(a.Body, "\n")
	rows := m.detailRows()
	if m.detailScroll >= len(bodyLines) {
		m.detailScroll = max(0, len(bodyLines)-1)
	}
	end := m.detailScroll + rows
	if end > len(bodyLines) {
		end = len(bodyLines)
	}
	for i := m.detailScroll; i < end; i++ {
		b.WriteString(bodyLines[i])
		b.WriteString("\n")
	}
	if end < len(bodyLines) {
		b.WriteString(StyleDim.Render("  … " + itoa(len(bodyLines)-end) + " more line(s)"))
	}
	return StyleOverlayBorder.Width(boxWidth).Render(b.String())
}
