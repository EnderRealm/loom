package tui

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

var universalKey = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("u")}

// seedUniversalStore extends the git-backed candidate store with what the
// universal gesture acts on and refuses: two validated loom truths, a universal
// truth sharing one of their filenames, and a validated decision. Everything is
// committed, so any write a refused gesture made shows up as dirt.
func seedUniversalStore(t *testing.T) (string, map[string]Artifact) {
	t.Helper()
	root, _ := seedGitCandidate(t)
	write := func(rel, id, typ string) {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		scope := filepath.Base(filepath.Dir(rel))
		body := "---\nid: " + id + "\ntitle: Title of " + id + "\nscope: " + scope + "\ntype: " + typ +
			"\nstatus: validated\nevidence:\n  - path: internal/x.go\n---\n\n## Claim\n\nloom retries webhooks.\n"
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("truths/loom/retry-backoff.md", "loom-retry-backoff", "truth")
	write("truths/loom/collide.md", "loom-collide", "truth")
	write("truths/universal/collide.md", "universal-collide", "truth")
	write("decisions/loom/pick.md", "loom-pick", "decision")
	testGit(t, root, "add", "-A")
	testGit(t, root, "commit", "-m", "seed validated artifacts")

	arts, err := LoadKnowledge()
	if err != nil {
		t.Fatalf("LoadKnowledge: %v", err)
	}
	byID := map[string]Artifact{}
	for _, a := range arts {
		byID[a.ID] = a
	}
	return root, byID
}

// selectArtifact is a knowledge view over the whole store with the cursor on id.
func selectArtifact(t *testing.T, id string) knowledgeModel {
	t.Helper()
	arts, err := LoadKnowledge()
	if err != nil {
		t.Fatalf("LoadKnowledge: %v", err)
	}
	var m knowledgeModel
	m.setArtifacts(arts)
	for i, a := range arts {
		if a.ID == id {
			m.cursor = i
			return m
		}
	}
	t.Fatalf("no artifact %s", id)
	return m
}

// universalHarness runs the App over a knowledge view under a real bubbletea
// program, so the gesture's Batch, Sequence and ExecProcess are dispatched, and
// the copy's commit handled, the way the TUI does it. It presses u at start and
// quits once no commit is outstanding.
type universalHarness struct {
	app       App
	committed []knowledgeCommittedMsg
	edited    *knowledgeEditedMsg
}

func (h universalHarness) Init() tea.Cmd {
	return func() tea.Msg { return universalKey }
}

func (h universalHarness) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case knowledgeCommittedMsg:
		h.committed = append(h.committed, msg)
	case knowledgeEditedMsg:
		h.edited = &msg
	}
	m, cmd := h.app.Update(msg)
	h.app = m.(App)
	switch msg.(type) {
	case knowledgeCommittedMsg, knowledgeEditedMsg:
		if h.app.knowledge.pendingCommits == 0 {
			return h, tea.Quit
		}
	}
	return h, cmd
}

func (h universalHarness) View() string { return "" }

// runUniversal presses u on id in a knowledge overlay and runs the program
// until the gesture's commits have all reported.
func runUniversal(t *testing.T, id string) universalHarness {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	app := App{overlay: overlayKnowledge, knowledge: selectArtifact(t, id)}
	// An empty reader rather than nil input: RestoreTerminal re-arms the input
	// reader after ExecProcess without checking for a disabled one.
	p := tea.NewProgram(universalHarness{app: app},
		tea.WithContext(ctx), tea.WithInput(strings.NewReader("")), tea.WithOutput(io.Discard),
		tea.WithoutRenderer(), tea.WithoutSignalHandler())
	final, err := p.Run()
	if err != nil {
		t.Fatalf("program: %v", err)
	}
	return final.(universalHarness)
}

// TestUniversalPromoteFromTheTUI drives u on a validated loom truth: the truth
// is copied to truths/universal/ with its frontmatter rewritten to a distinct id
// that cites the project truth, the project truth stays byte-identical, that
// copy is its own store commit, and $EDITOR then opens on the universal file —
// after the copy's commit, which the editor observes — and what it wrote is the
// next commit. Both truths then load, and u on the project truth again is
// refused for the occupied destination.
func TestUniversalPromoteFromTheTUI(t *testing.T) {
	root, arts := seedUniversalStore(t)
	art := arts["loom-retry-backoff"]
	original, err := os.ReadFile(art.Path)
	if err != nil {
		t.Fatal(err)
	}

	marker := filepath.Join(t.TempDir(), "head-at-edit")
	editor := filepath.Join(t.TempDir(), "editor.sh")
	script := "#!/bin/sh\n" +
		"git -C \"$(dirname \"$1\")\" log -1 --format=%s > \"$MARKER\"\n" +
		"printf '\\nRewritten for every project.\\n' >> \"$1\"\n"
	if err := os.WriteFile(editor, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MARKER", marker)
	t.Setenv("VISUAL", editor)

	h := runUniversal(t, art.ID)

	if h.app.knowledge.pendingCommits != 0 {
		t.Errorf("pendingCommits = %d after the gesture, want 0", h.app.knowledge.pendingCommits)
	}
	if len(h.committed) != 1 || h.committed[0].warn.NotCommitted != "" {
		t.Fatalf("the copy's commit did not report cleanly: %+v", h.committed)
	}
	if h.edited == nil || h.edited.status != "" || h.edited.warn.NotCommitted != "" {
		t.Fatalf("the edit did not commit: %+v", h.edited)
	}

	dest := filepath.Join(root, "truths", "universal", "retry-backoff.md")
	if kept, err := os.ReadFile(art.Path); err != nil || string(kept) != string(original) {
		t.Errorf("project truth changed by the promotion (%v):\n%s", err, kept)
	}
	body, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("universal file: %v", err)
	}
	fm, _, _ := strings.Cut(strings.TrimPrefix(string(body), "---\n"), "\n---\n")
	for _, want := range []string{"id: universal-retry-backoff", "scope: universal", "promoted_from: loom",
		"related:\n  - loom/retry-backoff.md"} {
		if !strings.Contains(fm, want+"\n") && !strings.HasSuffix(fm, want) {
			t.Errorf("frontmatter missing %q:\n%s", want, fm)
		}
	}
	if n := strings.Count(fm, "\nscope:"); n != 1 {
		t.Errorf("frontmatter carries %d scope: lines, want 1:\n%s", n, fm)
	}
	if !strings.Contains(string(body), "evidence:\n  - path: internal/x.go") {
		t.Errorf("sub-fields did not survive the copy:\n%s", body)
	}

	copySubject := universalGesture + " truth loom/" + art.ID
	if got, _ := os.ReadFile(marker); strings.TrimSpace(string(got)) != copySubject {
		t.Errorf("editor opened with HEAD at %q, want the copy's commit %q", strings.TrimSpace(string(got)), copySubject)
	}
	if got := testGit(t, root, "log", "-1", "--format=%s", "HEAD~1"); got != copySubject {
		t.Errorf("copy commit subject = %q, want %q", got, copySubject)
	}
	names := testGit(t, root, "show", "--name-status", "--no-renames", "--format=", "HEAD~1")
	if names != "A\ttruths/universal/retry-backoff.md" {
		t.Errorf("copy commit records more than the universal file:\n%s", names)
	}
	if got := testGit(t, root, "log", "-1", "--format=%s"); got != "edit truth universal/universal-retry-backoff" {
		t.Errorf("edit commit subject = %q, want the universal artifact's", got)
	}
	if committed := testGit(t, root, "show", "HEAD:truths/universal/retry-backoff.md"); !strings.Contains(committed, "Rewritten for every project.") {
		t.Errorf("edit commit does not carry what the editor wrote:\n%s", committed)
	}
	if st := testGit(t, root, "status", "--porcelain", "-uall"); st != "" {
		t.Errorf("store dirty after the gesture:\n%s", st)
	}

	loaded, err := LoadKnowledge()
	if err != nil {
		t.Fatalf("LoadKnowledge: %v", err)
	}
	found := map[string]string{}
	for _, a := range loaded {
		found[a.ID] = a.Path
	}
	if found[art.ID] != art.Path || found["universal-retry-backoff"] != dest {
		t.Errorf("after the promotion want both truths loaded, got %s=%q universal-retry-backoff=%q",
			art.ID, found[art.ID], found["universal-retry-backoff"])
	}
	_, cmd := selectArtifact(t, art.ID).update(universalKey)
	if msgs := drainCmd(cmd); len(msgs) != 1 || !strings.Contains(string(msgs[0].(statusMsg)), "already exists") {
		t.Errorf("u again on the promoted project truth = %+v, want the already-exists refusal", msgs)
	}
}

// TestUniversalPromoteRefusals: u on anything but a project-scoped validated
// truth, onto an occupied destination, or with no editor to rewrite in, says why
// on the status line and writes nothing.
func TestUniversalPromoteRefusals(t *testing.T) {
	root, arts := seedUniversalStore(t)
	var candidate string
	for id, a := range arts {
		if a.Status == "candidate" {
			candidate = id
		}
	}
	cases := []struct {
		name, id, visual, want string
	}{
		{"candidate", candidate, "true", "not a validated truth"},
		{"decision", "loom-pick", "true", "only truths"},
		{"already universal", "universal-collide", "true", "already universal"},
		{"destination exists", "loom-collide", "true", "already exists"},
		{"no editor", "loom-retry-backoff", "", "$VISUAL/$EDITOR not set"},
		// The artifact's refusal is the one that applies, editor or not.
		{"decision with no editor", "loom-pick", "", "only truths"},
		{"already universal with no editor", "universal-collide", "", "already universal"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("VISUAL", c.visual)
			t.Setenv("EDITOR", "")
			head := testGit(t, root, "rev-parse", "HEAD")

			m, cmd := selectArtifact(t, c.id).update(universalKey)
			msgs := drainCmd(cmd)
			if len(msgs) != 1 {
				t.Fatalf("refusal dispatched %d messages, want only the status line: %+v", len(msgs), msgs)
			}
			status, ok := msgs[0].(statusMsg)
			if !ok || !strings.Contains(string(status), c.want) {
				t.Errorf("status = %v, want it to contain %q", msgs[0], c.want)
			}
			if m.pendingCommits != 0 {
				t.Errorf("pendingCommits = %d after a refusal", m.pendingCommits)
			}
			if now := testGit(t, root, "rev-parse", "HEAD"); now != head {
				t.Errorf("HEAD moved on a refusal: %s -> %s", head, now)
			}
			if st := testGit(t, root, "status", "--porcelain", "-uall"); st != "" {
				t.Errorf("refusal wrote to the store:\n%s", st)
			}
		})
	}
}

// TestUniversalPromoteRefusesSymlinkSource: a truth file committed as a symlink
// out of the store loads with the contents it points at, so promoting it from
// that load would publish a file outside the store into truths/universal/. The
// source is read through the store, which refuses the link before anything is
// written or committed.
func TestUniversalPromoteRefusesSymlinkSource(t *testing.T) {
	root, _ := seedUniversalStore(t)
	private := filepath.Join(t.TempDir(), "private.md")
	secret := "---\nid: loom-leak\ntitle: Private notes\n---\n\nprivate contents\n"
	if err := os.WriteFile(private, []byte(secret), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(private, filepath.Join(root, "truths", "loom", "leak.md")); err != nil {
		t.Fatal(err)
	}
	testGit(t, root, "add", "-A")
	testGit(t, root, "commit", "-m", "commit a symlinked truth")
	t.Setenv("VISUAL", "true")
	head := testGit(t, root, "rev-parse", "HEAD")

	m, cmd := selectArtifact(t, "loom-leak").update(universalKey)
	msgs := drainCmd(cmd)
	if len(msgs) != 1 {
		t.Fatalf("refusal dispatched %d messages, want only the status line: %+v", len(msgs), msgs)
	}
	if status, ok := msgs[0].(statusMsg); !ok || !strings.Contains(string(status), "symlink") {
		t.Errorf("status = %v, want the symlink refusal", msgs[0])
	}
	if m.pendingCommits != 0 {
		t.Errorf("pendingCommits = %d after a refusal", m.pendingCommits)
	}
	if _, err := os.Lstat(filepath.Join(root, "truths", "universal", "leak.md")); !os.IsNotExist(err) {
		t.Errorf("destination written from a symlinked source: %v", err)
	}
	if now := testGit(t, root, "rev-parse", "HEAD"); now != head {
		t.Errorf("HEAD moved on a refusal: %s -> %s", head, now)
	}
	if st := testGit(t, root, "status", "--porcelain", "-uall"); st != "" {
		t.Errorf("refusal wrote to the store:\n%s", st)
	}
}

// TestUniversalPromoteUncommittedCopyOpensNoEditor: when the copy lands but its
// commit does not — here truths/universal/ is ignored, which no promoted file
// may be — the reason reaches the status line and $EDITOR never opens, since
// the edit's commit would fold the promotion into the edit.
func TestUniversalPromoteUncommittedCopyOpensNoEditor(t *testing.T) {
	root, arts := seedUniversalStore(t)
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("truths/universal/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testGit(t, root, "add", "--", filepath.Join(root, ".gitignore"))
	testGit(t, root, "commit", "-m", "ignore truths/universal/")
	opened := filepath.Join(t.TempDir(), "opened")
	editor := filepath.Join(t.TempDir(), "editor.sh")
	if err := os.WriteFile(editor, []byte("#!/bin/sh\ntouch \"$OPENED\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENED", opened)
	t.Setenv("VISUAL", editor)

	h := runUniversal(t, arts["loom-retry-backoff"].ID)

	if len(h.committed) != 1 || h.committed[0].warn.NotCommitted == "" {
		t.Fatalf("the copy's commit was expected to fail: %+v", h.committed)
	}
	if h.edited != nil {
		t.Errorf("the edit ran over an uncommitted copy: %+v", h.edited)
	}
	if _, err := os.Stat(opened); !os.IsNotExist(err) {
		t.Errorf("$EDITOR opened over an uncommitted copy")
	}
	if h.app.knowledge.pendingCommits != 0 {
		t.Errorf("pendingCommits = %d, want 0", h.app.knowledge.pendingCommits)
	}
	if !strings.Contains(h.app.status, "not committed") || !strings.Contains(h.app.status, h.committed[0].warn.NotCommitted) {
		t.Errorf("status = %q, want the copy's not-committed reason", h.app.status)
	}
}

// TestUniversalPromoteRefusesMissingFrontmatter: the loader lists only files
// with frontmatter, but the gesture re-reads the source through the store, so a
// truth rewritten after the list loaded — its frontmatter removed, or left
// unclosed — has nowhere to carry scope: universal, promoted_from: and related:. The
// gesture refuses it on the status line rather than filing a universal truth
// with no provenance.
func TestUniversalPromoteRefusesMissingFrontmatter(t *testing.T) {
	cases := map[string]string{
		"none":         "## Claim\n\nloom retries webhooks.\n",
		"unterminated": "---\nid: loom-retry-backoff\nscope: loom\n\n## Claim\n\nloom retries webhooks.\n",
	}
	for name, rewritten := range cases {
		t.Run(name, func(t *testing.T) {
			root, arts := seedUniversalStore(t)
			art := arts["loom-retry-backoff"]
			t.Setenv("VISUAL", "true")
			m := selectArtifact(t, art.ID)
			if err := os.WriteFile(art.Path, []byte(rewritten), 0o644); err != nil {
				t.Fatal(err)
			}
			testGit(t, root, "add", "-A")
			testGit(t, root, "commit", "-m", "rewrite the truth after the list loaded")
			head := testGit(t, root, "rev-parse", "HEAD")

			m, cmd := m.update(universalKey)
			msgs := drainCmd(cmd)
			if len(msgs) != 1 {
				t.Fatalf("refusal dispatched %d messages, want only the status line: %+v", len(msgs), msgs)
			}
			if status, ok := msgs[0].(statusMsg); !ok || !strings.Contains(string(status), "no frontmatter block") {
				t.Errorf("status = %v, want the missing-frontmatter refusal", msgs[0])
			}
			if m.pendingCommits != 0 {
				t.Errorf("pendingCommits = %d after a refusal", m.pendingCommits)
			}
			if _, err := os.Stat(filepath.Join(root, "truths", "universal", "retry-backoff.md")); !os.IsNotExist(err) {
				t.Errorf("destination written without frontmatter: %v", err)
			}
			if now := testGit(t, root, "rev-parse", "HEAD"); now != head {
				t.Errorf("HEAD moved on a refusal: %s -> %s", head, now)
			}
			if st := testGit(t, root, "status", "--porcelain", "-uall"); st != "" {
				t.Errorf("refusal wrote to the store:\n%s", st)
			}
		})
	}
}

// TestUniversalFrontmatterKeepsOneOfEach: a promoted file carries exactly one
// id:, one scope: and one promoted_from:, whatever duplicates the source had,
// and a source with no id: or scope: still gets them. A source with no complete
// frontmatter block is an error, not a pass-through.
func TestUniversalFrontmatterKeepsOneOfEach(t *testing.T) {
	in := "---\nid: loom-x\nscope: loom\npromoted_from: old\nscope: loom\nid: loom-x\n---\n\nscope: loom stays in prose\n"
	want := "---\nid: universal-x\nscope: universal\npromoted_from: loom\nrelated:\n  - loom/x.md\n---\n\nscope: loom stays in prose\n"
	if got, err := universalFrontmatter(in, "loom", "x.md"); err != nil || got != want {
		t.Errorf("universalFrontmatter =\n%q, %v\nwant\n%q", got, err, want)
	}
	bare := "---\ntitle: t\n---\nbody\n"
	if got, err := universalFrontmatter(bare, "loom", "x.md"); err != nil ||
		got != "---\ntitle: t\nid: universal-x\nscope: universal\npromoted_from: loom\nrelated:\n  - loom/x.md\n---\nbody\n" {
		t.Errorf("universalFrontmatter without id: or scope: =\n%q, %v", got, err)
	}
	for _, body := range []string{"body only\n", "---\nid: x\nscope: loom\nbody\n"} {
		if got, err := universalFrontmatter(body, "loom", "x.md"); err == nil {
			t.Errorf("universalFrontmatter(%q) = %q, want an error", body, got)
		}
	}
}

// TestUniversalFrontmatterLinksSource: the project truth is appended to
// whatever related: the source already carries, in the form it carries it, and
// sub-fields of other keys pass through.
func TestUniversalFrontmatterLinksSource(t *testing.T) {
	const head = "---\nid: loom-x\nscope: loom\n"
	const out = "---\nid: universal-x\nscope: universal\npromoted_from: loom\n"
	const tail = "verified_at: 2026-01-01\n---\nbody\n"
	cases := map[string]struct{ in, want string }{
		"block list": {
			head + "related:\n  - a.md\n  - b.md\n" + tail,
			out + "related:\n  - a.md\n  - b.md\n  - loom/x.md\n" + tail,
		},
		"block list at column zero": {
			head + "related:\n- a.md\n" + tail,
			out + "related:\n- a.md\n- loom/x.md\n" + tail,
		},
		"block list closing the frontmatter": {
			head + "related:\n  - a.md\n---\nbody\n",
			out + "related:\n  - a.md\n  - loom/x.md\n---\nbody\n",
		},
		"empty key": {
			head + "related:\n" + tail,
			out + "related:\n  - loom/x.md\n" + tail,
		},
		"null": {
			head + "related: ~\n" + tail,
			out + "related:\n  - loom/x.md\n" + tail,
		},
		"empty flow list": {
			head + "related: []\n" + tail,
			out + "related: [loom/x.md]\n" + tail,
		},
		"flow list": {
			head + "related: [a.md, b.md]\n" + tail,
			out + "related: [a.md, b.md, loom/x.md]\n" + tail,
		},
		"scalar": {
			head + "related: a.md\n" + tail,
			out + "related:\n  - a.md\n  - loom/x.md\n" + tail,
		},
		"none": {
			head + "evidence:\n  - path: internal/x.go\nverified_at: 2026-01-01\n---\nbody\n",
			out + "evidence:\n  - path: internal/x.go\nverified_at: 2026-01-01\nrelated:\n  - loom/x.md\n---\nbody\n",
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got, err := universalFrontmatter(c.in, "loom", "x.md"); err != nil || got != c.want {
				t.Errorf("universalFrontmatter =\n%q, %v\nwant\n%q", got, err, c.want)
			}
		})
	}
}
