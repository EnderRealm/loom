// Package tui is the terminal UI for managing Loom. The dashboard lists every
// project Loom has seen on the local shipper/receiver and drills into session
// and ticket state for one project at a time.
package tui

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"loom/internal/config"
)

type overlayID int

const (
	overlayNone overlayID = iota
	overlayDetail
	overlayKnowledge
)

type App struct {
	dashboard dashboardModel
	detail    detailModel
	knowledge knowledgeModel
	overlay   overlayID
	width     int
	height    int
	status    string
	err       error
	loading   bool
}

func New() App {
	return App{loading: true, dashboard: dashboardModel{sortCol: defaultSortCol()}}
}

// defaultSortCol returns the index of the SESSIONS column so the dashboard's
// default order matches the historical load-time sort.
func defaultSortCol() int {
	for i, c := range sortColumns {
		if c.header == "SESSIONS" {
			return i
		}
	}
	return 0
}

type projectsLoadedMsg []Project
type knowledgeLoadedMsg []Artifact
type errMsg error
type statusMsg string
type clearStatusMsg struct{}
type tickMsg time.Time

func loadCmd() tea.Cmd {
	return func() tea.Msg {
		projects, err := LoadProjects()
		if err != nil {
			return errMsg(err)
		}
		return projectsLoadedMsg(projects)
	}
}

func loadKnowledgeCmd() tea.Cmd {
	return func() tea.Msg {
		arts, err := LoadKnowledge()
		if err != nil {
			return errMsg(err)
		}
		return knowledgeLoadedMsg(arts)
	}
}

func tickCmd() tea.Cmd {
	return tea.Tick(15*time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func clearStatusAfter(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return clearStatusMsg{} })
}

func (a App) Init() tea.Cmd {
	return tea.Batch(loadCmd(), loadKnowledgeCmd(), tickCmd())
}

func (a App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width = msg.Width
		a.height = msg.Height
		a.dashboard.setSize(a.width, a.contentHeight())
		a.detail.setSize(a.width, a.contentHeight())
		a.knowledge.setSize(a.width, a.contentHeight())
		return a, nil

	case projectsLoadedMsg:
		a.loading = false
		a.dashboard.setProjects([]Project(msg))
		if a.overlay == overlayDetail {
			if sel := a.findProject(a.detail.project.Slug); sel != nil {
				a.detail.project = sel
			} else {
				a.overlay = overlayNone
			}
		}
		return a, nil

	case knowledgeLoadedMsg:
		a.knowledge.setArtifacts([]Artifact(msg))
		return a, nil

	case tickMsg:
		return a, tea.Batch(loadCmd(), loadKnowledgeCmd(), tickCmd())

	case errMsg:
		a.err = msg
		return a, nil

	case statusMsg:
		a.status = string(msg)
		return a, clearStatusAfter(3 * time.Second)

	case clearStatusMsg:
		a.status = ""
		return a, nil

	case tea.KeyMsg:
		if a.overlay == overlayDetail {
			switch msg.String() {
			case "esc", "q":
				a.overlay = overlayNone
				return a, nil
			case "t":
				return a.launchTk()
			}
			var cmd tea.Cmd
			a.detail, cmd = a.detail.update(msg)
			return a, cmd
		}
		if a.overlay == overlayKnowledge {
			// Detail sub-view consumes its own keys; only close the overlay
			// from the list view, so 'q' inside detail doesn't quit unexpectedly.
			if !a.knowledge.showDetail {
				switch msg.String() {
				case "esc", "q":
					a.overlay = overlayNone
					return a, nil
				}
			}
			var cmd tea.Cmd
			a.knowledge, cmd = a.knowledge.update(msg)
			return a, cmd
		}
		switch msg.String() {
		case "q", "ctrl+c":
			return a, tea.Quit
		case "r":
			a.status = "refreshing…"
			return a, tea.Batch(loadCmd(), loadKnowledgeCmd(), clearStatusAfter(2*time.Second))
		case "c":
			a.overlay = overlayKnowledge
			return a, loadKnowledgeCmd()
		case "enter", "o", "l", "right":
			if sel := a.dashboard.selected(); sel != nil {
				a.detail = newDetailModel(sel, a.width, a.contentHeight())
				a.overlay = overlayDetail
				return a, nil
			}
		}
		var cmd tea.Cmd
		a.dashboard, cmd = a.dashboard.update(msg)
		return a, cmd
	}

	if a.overlay == overlayDetail {
		var cmd tea.Cmd
		a.detail, cmd = a.detail.update(msg)
		return a, cmd
	}
	if a.overlay == overlayKnowledge {
		var cmd tea.Cmd
		a.knowledge, cmd = a.knowledge.update(msg)
		return a, cmd
	}
	var cmd tea.Cmd
	a.dashboard, cmd = a.dashboard.update(msg)
	return a, cmd
}

func (a App) findProject(slug string) *Project {
	for i := range a.dashboard.projects {
		if a.dashboard.projects[i].Slug == slug {
			return &a.dashboard.projects[i]
		}
	}
	return nil
}

func (a App) View() string {
	if a.err != nil {
		return fmt.Sprintf("error: %v\n", a.err)
	}
	if a.width == 0 || a.height == 0 {
		return ""
	}

	pad := lipgloss.NewStyle().PaddingLeft(1).PaddingRight(1)
	sep := lipgloss.NewStyle().Foreground(colorSubtle).Render(strings.Repeat("─", a.width))

	var b strings.Builder

	// Header.
	b.WriteString(" ")
	b.WriteString(StyleBold.Foreground(colorWhite).Render("loom"))
	b.WriteString("  ")
	b.WriteString(StyleDim.Render("—"))
	b.WriteString("  ")
	b.WriteString(StyleDim.Render(config.Home()))
	b.WriteString("  ")
	b.WriteString(StyleDim.Render(fmt.Sprintf("projects: %d", len(a.dashboard.projects))))
	b.WriteString("\n")
	b.WriteString(sep)
	b.WriteString("\n")

	// Body.
	var body string
	if a.loading && len(a.dashboard.projects) == 0 {
		body = StyleDim.Render("  loading…")
	} else if len(a.dashboard.projects) == 0 {
		body = StyleDim.Render("  no projects found under " + config.Home())
	} else {
		body = a.dashboard.view()
	}
	if a.overlay == overlayDetail {
		body = StyleDim.Render(body) + "\n" + a.detail.view()
	}
	if a.overlay == overlayKnowledge {
		body = a.knowledge.view()
	}
	b.WriteString(body)

	// Footer: status or help.
	b.WriteString(sep)
	b.WriteString("\n")
	if a.status != "" {
		b.WriteString(pad.Render(StyleWarning.Render(a.status)))
	} else {
		b.WriteString(pad.Render(StyleHelp.Render(a.helpLine())))
	}
	return b.String()
}

func (a App) helpLine() string {
	if a.overlay == overlayDetail {
		return "↑↓ scroll  │  t open in tk  │  esc/q close"
	}
	if a.overlay == overlayKnowledge {
		if a.knowledge.showDetail {
			return "↑↓ scroll  │  p promote  │  x reject  │  e edit  │  esc/q back"
		}
		return "↑↓ select  │  enter view  │  p promote  │  x reject  │  e edit  │  s skip  │  esc/q close"
	}
	return "↑↓ select  │  enter open  │  s sort  │  c knowledge  │  r refresh  │  q quit"
}

// launchTk shells out to `tk ui --repo <path>` via tea.ExecProcess so the
// child takes over the terminal. On exit we return to the dashboard; the
// next refresh tick (or an immediate one we queue here) picks up any ticket
// edits the user made inside tk.
func (a App) launchTk() (tea.Model, tea.Cmd) {
	if a.detail.project == nil || a.detail.project.Path == "" {
		return a, statusCmd("path unresolved — cannot open tk")
	}
	if _, err := exec.LookPath("tk"); err != nil {
		return a, statusCmd("tk not on $PATH")
	}
	cmd := exec.Command("tk", "ui", "--repo", a.detail.project.Path)
	return a, tea.ExecProcess(cmd, func(err error) tea.Msg {
		if err != nil {
			return statusMsg("tk exited: " + err.Error())
		}
		return nil
	})
}

func statusCmd(msg string) tea.Cmd {
	return func() tea.Msg { return statusMsg(msg) }
}

func (a App) contentHeight() int {
	h := a.height - 4
	if h < 1 {
		h = 1
	}
	return h
}
