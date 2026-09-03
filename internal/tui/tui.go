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
	"loom/internal/knowledge/store"
	"loom/internal/summaries"
)

type overlayID int

const (
	overlayNone overlayID = iota
	overlayDetail
	overlayKnowledge
	overlayActivity
)

type App struct {
	dashboard dashboardModel
	detail    detailModel
	knowledge knowledgeModel
	activity  activityModel
	overlay   overlayID
	width     int
	height    int
	status    string
	statusGen int
	err       error
	loading   bool
	quitting  bool
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

// knowledgeEditedMsg is an $EDITOR return: the reload the edit needs, carrying
// the reason its commit did not land or was not published. One message rather
// than two so a degraded commit reaches the status line without the reload
// waiting on it. status is set instead when the edit never reached its commit —
// the editor or the reload failed — since every path out of the edit's
// ExecProcess has to land here or the count the quit drain waits on never
// returns to zero.
type knowledgeEditedMsg struct {
	artifacts []Artifact
	warn      store.Warn
	status    string
}

// knowledgeCommittedMsg is a promote or reject's deferred commit returning: the
// git work ran off the update loop, so its outcome arrives here rather than at
// the gesture. status is the line the gesture already set and notCommitted the
// wording it uses for a record that did not land — a reject's can fail before
// any commit — so the reason composes onto that line instead of replacing it.
type knowledgeCommittedMsg struct {
	status       string
	notCommitted string
	warn         store.Warn
}
type activityLoadedMsg struct {
	view    *summaries.ActivityView
	tickets TicketActivity
}
type errMsg error
type statusMsg string

// clearStatusMsg carries the status generation it was armed for: a line set
// later — a deferred commit's warn composed after the gesture's own line — must
// outlive the earlier line's timer, which is still pending and would otherwise
// wipe it partway through.
type clearStatusMsg int
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

// loadActivityCmd gathers the rolling 24h summary view and ticket created/
// closed activity in one shot for the activity overlay.
func loadActivityCmd() tea.Cmd {
	return func() tea.Msg {
		av, err := summaries.LoadActivity(24 * time.Hour)
		if err != nil {
			return errMsg(err)
		}
		return activityLoadedMsg{view: av, tickets: LoadTicketActivity(24 * time.Hour)}
	}
}

func tickCmd() tea.Cmd {
	return tea.Tick(15*time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

// setStatus puts one line on the status bar and returns the cmd that clears it
// after d, or no cmd at d == 0 — a line held until something replaces it, for a
// wait bounded by work rather than by the clock. Every site that writes a.status
// goes through here, so no line is shown without a clear armed for that line and
// no other. Call sites bind the returned cmd to a variable before returning it:
// Go orders the calls in a return statement but not the plain `a` operand
// against them, so `return a, a.setStatus(…)` may return the pre-mutation copy
// and drop the line.
func (a *App) setStatus(status string, d time.Duration) tea.Cmd {
	a.status = status
	a.statusGen++
	gen := a.statusGen
	if d == 0 {
		return nil
	}
	return tea.Tick(d, func(time.Time) tea.Msg { return clearStatusMsg(gen) })
}

// clearStatus takes the current line down now. The generation bump leaves any
// clear still armed for it pointing at a generation that no longer matches, so
// it cannot blank whatever line comes next.
func (a *App) clearStatus() {
	a.status = ""
	a.statusGen++
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
		a.activity.setSize(a.width, a.contentHeight())
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

	case knowledgeEditedMsg:
		// The edit's commit runs off the update loop too, inside ExecProcess's
		// callback, so it is drained on the way out like a gesture's.
		a.knowledge.pendingCommits--
		if a.quitting {
			if a.knowledge.pendingCommits == 0 {
				return a, tea.Quit
			}
			return a, nil
		}
		if msg.status != "" {
			return a, statusCmd(msg.status)
		}
		a.knowledge.setArtifacts(msg.artifacts)
		if msg.warn.NotCommitted != "" {
			return a, statusCmd("edited — not committed: " + msg.warn.NotCommitted)
		}
		if msg.warn.NotPushed != "" {
			return a, statusCmd("edited — not pushed: " + msg.warn.NotPushed)
		}
		return a, nil

	case knowledgeCommittedMsg:
		a.knowledge.pendingCommits--
		if a.quitting {
			// A warn is not shown on the way out: the whole failure is already in
			// knowledge-git.log, which is the record that outlives the session.
			if a.knowledge.pendingCommits == 0 {
				return a, tea.Quit
			}
			// The drain's line was set with no clear armed, so it stands for the
			// rest of the wait without being re-armed here.
			return a, nil
		}
		// A clean record leaves the gesture's own status alone: re-arming it would
		// hold a line the user has already read past for another three seconds.
		if msg.warn == (store.Warn{}) {
			return a, nil
		}
		status := msg.status
		if msg.warn.NotCommitted != "" {
			status += msg.notCommitted + msg.warn.NotCommitted
		}
		if msg.warn.NotPushed != "" {
			// The record exists in the store's history and only the publication is
			// missing, which the next gesture's push carries unless the remote has
			// diverged.
			status += " — not pushed: " + msg.warn.NotPushed
		}
		cmd := a.setStatus(status, 3*time.Second)
		return a, cmd

	case activityLoadedMsg:
		a.activity.setData(msg.view, msg.tickets)
		return a, nil

	case tickMsg:
		return a, tea.Batch(loadCmd(), loadKnowledgeCmd(), tickCmd())

	case errMsg:
		a.err = msg
		return a, nil

	case statusMsg:
		cmd := a.setStatus(string(msg), 3*time.Second)
		return a, cmd

	case clearStatusMsg:
		if int(msg) == a.statusGen {
			a.status = ""
		}
		return a, nil

	case tea.KeyMsg:
		// Any key but a repeat quit withdraws a held quit: the user went back to
		// work, and a flag left set would exit from under them — mid-review, with
		// the last gesture's outcome reported nowhere — the moment the drain
		// reaches zero. A second q or ctrl+c still arrives with it set, so the
		// escape hatch out of a slow commit survives.
		if a.quitting && msg.String() != "q" && msg.String() != "ctrl+c" {
			a.quitting = false
			a.clearStatus()
		}
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
		if a.overlay == overlayActivity {
			switch msg.String() {
			case "esc", "q":
				a.overlay = overlayNone
				return a, nil
			case "r":
				cmd := a.setStatus("refreshing…", 2*time.Second)
				return a, tea.Batch(loadActivityCmd(), cmd)
			}
			var cmd tea.Cmd
			a.activity, cmd = a.activity.update(msg)
			return a, cmd
		}
		switch msg.String() {
		case "q", "ctrl+c":
			return a.quit()
		case "r":
			cmd := a.setStatus("refreshing…", 2*time.Second)
			return a, tea.Batch(loadCmd(), loadKnowledgeCmd(), cmd)
		case "a":
			a.activity = activityModel{}
			a.activity.setSize(a.width, a.contentHeight())
			a.overlay = overlayActivity
			return a, loadActivityCmd()
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
	if a.overlay == overlayActivity {
		var cmd tea.Cmd
		a.activity, cmd = a.activity.update(msg)
		return a, cmd
	}
	var cmd tea.Cmd
	a.dashboard, cmd = a.dashboard.update(msg)
	return a, cmd
}

// quit leaves, unless a gesture's deferred commit is still in flight. bubbletea
// does not wait on a Cmd's goroutine and cmd/loom returns as soon as Run does,
// so quitting mid-commit abandons that goroutine partway through the sequence:
// the git child already running is orphaned rather than killed — nothing signals
// it, and runGit's deferred cancel never runs — but every step after it does not
// happen. The push, commitKnowledge's unstaging recovery and the Warn that would
// have reached the status line and knowledge-git.log are all lost, leaving the
// gesture's already-moved file as working-tree state whose record is at best
// partial and unreported — the state the store package exists to prevent. The
// wait is bounded only by git, so a second q or ctrl+c leaves anyway: a user who
// presses twice has decided. The
// drain's line is set with no clear armed for the same reason — a line that
// timed out mid-wait would leave the help footer up under a TUI silently
// refusing to exit, which is what pushes the user into that second q.
func (a App) quit() (tea.Model, tea.Cmd) {
	if a.quitting || a.knowledge.pendingCommits == 0 {
		return a, tea.Quit
	}
	a.quitting = true
	cmd := a.setStatus(quitDrainStatus, 0)
	return a, cmd
}

// quitDrainStatus reports the held quit. It carries the way out rather than the
// footer, which has no room for it and is not on screen while a status line is.
const quitDrainStatus = "finishing knowledge commit… (q again leaves anyway)"

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
	if a.overlay == overlayActivity {
		body = a.activity.view()
	}
	b.WriteString(body)

	// Footer: status or help.
	b.WriteString(sep)
	b.WriteString("\n")
	if a.status != "" {
		// Producers compose a status out of a path and a reason and none of them
		// know the window; clamp here — pad takes a column on each side — so a
		// long one cannot wrap the fullscreen layout.
		b.WriteString(pad.Render(StyleWarning.Render(truncate(a.status, a.width-2))))
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
	if a.overlay == overlayActivity {
		return "↑↓ scroll  │  r refresh  │  esc/q close"
	}
	return "↑↓ select  │  enter open  │  s sort  │  a activity  │  c knowledge  │  r refresh  │  q quit"
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
