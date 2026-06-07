package tui

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"loom/internal/config"
)

// Artifact is one truth or decision file under ~/.loom/knowledge/. Both
// validated artifacts (truths/<scope>/, decisions/<scope>/) and candidates
// (_candidates/<type>/<scope>/) are loaded into this same shape; Status
// distinguishes them.
type Artifact struct {
	ID       string
	Title    string
	Scope    string
	Type     string // "truth" | "decision"
	Status   string // "validated" | "candidate"
	Path     string
	Body     string    // full file contents (eager-loaded; corpus is small)
	Modified time.Time
}

// KnowledgeRoot returns the durable knowledge store path, honoring
// LOOM_KNOWLEDGE_ROOT for parity with the extractors. Defaults to
// $LOOM_HOME/knowledge.
func KnowledgeRoot() string {
	if v := os.Getenv("LOOM_KNOWLEDGE_ROOT"); v != "" {
		return v
	}
	return filepath.Join(config.Home(), "knowledge")
}

// LoadKnowledge walks the store and returns every artifact: validated
// first (status=validated), then candidates (status=candidate). Within each
// status group, results are sorted newest-first by file mtime so recent
// extractions surface at the top of the review list.
func LoadKnowledge() ([]Artifact, error) {
	root := KnowledgeRoot()
	var out []Artifact

	// Validated: <type>s/<scope>/*.md
	for _, t := range []string{"truths", "decisions"} {
		base := filepath.Join(root, t)
		more, err := walkArtifacts(base, t, "validated")
		if err != nil {
			return nil, err
		}
		out = append(out, more...)
	}

	// Candidates: _candidates/<type>s/<scope>/*.md (skip _rejected/)
	for _, t := range []string{"truths", "decisions"} {
		base := filepath.Join(root, "_candidates", t)
		more, err := walkArtifacts(base, t, "candidate")
		if err != nil {
			return nil, err
		}
		out = append(out, more...)
	}

	sort.SliceStable(out, func(i, j int) bool {
		// candidates first (most actionable), then validated
		if out[i].Status != out[j].Status {
			return out[i].Status == "candidate"
		}
		return out[i].Modified.After(out[j].Modified)
	})
	return out, nil
}

func walkArtifacts(base, plural, status string) ([]Artifact, error) {
	if _, err := os.Stat(base); os.IsNotExist(err) {
		return nil, nil
	}
	var out []Artifact
	entries, err := os.ReadDir(base)
	if err != nil {
		return nil, err
	}
	for _, scopeEntry := range entries {
		if !scopeEntry.IsDir() {
			continue
		}
		// Skip _rejected/ archive (sibling of <scope> dirs under _candidates/)
		if strings.HasPrefix(scopeEntry.Name(), "_") {
			continue
		}
		scope := scopeEntry.Name()
		scopeDir := filepath.Join(base, scope)
		files, err := os.ReadDir(scopeDir)
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".md") {
				continue
			}
			if strings.HasPrefix(f.Name(), "_") || f.Name() == "README.md" {
				continue
			}
			path := filepath.Join(scopeDir, f.Name())
			body, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			info, _ := f.Info()
			a := parseArtifact(string(body), path, scope, plural, status)
			if a.ID == "" {
				continue
			}
			if info != nil {
				a.Modified = info.ModTime()
			}
			out = append(out, a)
		}
	}
	return out, nil
}

// frontmatterKey matches a top-level "key: value" line inside a `---` block.
// Indented lines (sub-fields under sources:, evidence:) are ignored.
var frontmatterKey = regexp.MustCompile(`^([a-z_]+):\s*(.*)$`)

func parseArtifact(body, path, scope, plural, status string) Artifact {
	a := Artifact{Path: path, Scope: scope, Body: body, Status: status}
	switch plural {
	case "truths":
		a.Type = "truth"
	case "decisions":
		a.Type = "decision"
	}

	// Carve out frontmatter (between the first two `---` lines).
	lines := strings.Split(body, "\n")
	if len(lines) < 2 || strings.TrimSpace(lines[0]) != "---" {
		return a
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return a
	}
	for _, line := range lines[1:end] {
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") || strings.HasPrefix(line, "-") {
			continue
		}
		m := frontmatterKey.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		// Last-write-wins matches the extractor's inject_frontmatter behavior.
		switch m[1] {
		case "id":
			a.ID = strings.TrimSpace(m[2])
		case "title":
			a.Title = strings.TrimSpace(m[2])
		case "status":
			if v := strings.TrimSpace(m[2]); v != "" {
				a.Status = v
			}
		}
	}
	return a
}

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
// out to $EDITOR for any selected artifact and reloads on return.
func (m knowledgeModel) promote() (knowledgeModel, tea.Cmd) {
	a := m.selected()
	if a == nil {
		return m, nil
	}
	if a.Status != "candidate" {
		return m, statusCmd("only candidates can be promoted")
	}
	dest, err := promoteCandidate(*a)
	if err != nil {
		return m, statusCmd("promote failed: " + err.Error())
	}
	m.showDetail = false
	return m, tea.Batch(statusCmd("promoted → "+shortenPath(dest)), loadKnowledgeCmd())
}

func (m knowledgeModel) reject() (knowledgeModel, tea.Cmd) {
	a := m.selected()
	if a == nil {
		return m, nil
	}
	if a.Status != "candidate" {
		return m, statusCmd("only candidates can be rejected")
	}
	if _, err := rejectCandidate(*a); err != nil {
		return m, statusCmd("reject failed: " + err.Error())
	}
	m.showDetail = false
	return m, tea.Batch(statusCmd("rejected — archived to _rejected/"), loadKnowledgeCmd())
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
	return m, tea.ExecProcess(cmd, func(err error) tea.Msg {
		if err != nil {
			return statusMsg("editor exited: " + err.Error())
		}
		arts, e := LoadKnowledge()
		if e != nil {
			return errMsg(e)
		}
		return knowledgeLoadedMsg(arts)
	})
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

	var ageCell string
	if a.Modified.IsZero() {
		ageCell = padRightBg(selBg.Foreground(colorMuted).Render("—"), colKnowAge, bg)
	} else {
		ageCell = padRightBg(selBg.Foreground(colorGray).Render(humanDuration(time.Since(a.Modified))), colKnowAge, bg)
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
