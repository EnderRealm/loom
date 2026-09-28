package escapes

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/EnderRealm/ticket/v8/pkg/ticket"

	"loom/internal/config"
	"loom/internal/summaries"
)

// testRepo is a throwaway git repository whose commits a test writes.
type testRepo struct {
	t   *testing.T
	dir string
}

func newTestRepo(t *testing.T) *testRepo {
	t.Helper()
	r := &testRepo{t: t, dir: t.TempDir()}
	r.git("init", "-q", "-b", "main")
	return r
}

func (r *testRepo) git(args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", append([]string{"-C", r.dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// commit writes files (path → content) and commits them, returning the hash.
func (r *testRepo) commit(subject string, files map[string]string) string {
	r.t.Helper()
	for path, content := range files {
		full := filepath.Join(r.dir, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			r.t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			r.t.Fatal(err)
		}
	}
	r.git("add", "-A")
	r.git("commit", "-q", "-m", subject)
	return r.git("rev-parse", "HEAD")
}

// TestBlameIgnoresWhitespaceAndGenerated builds a history in which a fix
// changes one line's content, one line's whitespace only, and a golden file,
// and in which a whitespace-only commit sits between the defect and the fix.
// Only the commit that wrote the content-changed line may be blamed.
func TestBlameIgnoresWhitespaceAndGenerated(t *testing.T) {
	r := newTestRepo(t)
	intro := r.commit("Add a and b", map[string]string{
		"main.go":              "package main\n\nfunc a() int { return 1 }\nfunc b() int { return 2 }\n",
		"tests/golden/out.txt": "x\n",
	})
	addC := r.commit("Add c", map[string]string{
		"main.go": "package main\n\nfunc a() int { return 1 }\nfunc b() int { return 2 }\nfunc c() int { return 3 }\n",
	})
	reindent := r.commit("Reindent a", map[string]string{
		"main.go": "package main\n\nfunc a() int {  return 1  }\nfunc b() int { return 2 }\nfunc c() int { return 3 }\n",
	})
	golden := r.commit("Regenerate golden", map[string]string{
		"tests/golden/out.txt": "y\n",
	})
	fix := r.commit("[proj/bug-1a2b] Return 10 from a", map[string]string{
		"main.go":              "package main\n\nfunc a() int {  return 10  }\nfunc b() int { return 2 }\n\tfunc c() int {   return 3 }\n",
		"tests/golden/out.txt": "z\n",
	})

	repo, err := OpenRepo(r.dir)
	if err != nil {
		t.Fatal(err)
	}
	a, err := Attribute(repo, "proj/bug-1a2b")
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Fix) != 1 || a.Fix[0] != fix {
		t.Fatalf("Fix = %v, want [%s]", a.Fix, fix)
	}
	names := map[string]string{intro: "intro", addC: "add c", reindent: "reindent", golden: "golden"}
	for _, b := range a.Blamed {
		if b.Commit != intro {
			t.Errorf("blamed %s (%s) for %d lines", names[b.Commit], b.Commit, b.Lines)
		}
	}
	if a.Class != ClassSingle || len(a.Blamed) != 1 || a.Blamed[0].Lines != 1 {
		t.Errorf("attribution = %+v, want single on intro for 1 line", a)
	}
}

// TestBlameUnusualPaths blames fixes to files whose names git's diff header
// does not print bare: one holding a space, which git ends with a tab, and one
// holding a non-ASCII byte, which git C-quotes unless core.quotePath is off.
func TestBlameUnusualPaths(t *testing.T) {
	for _, name := range []string{"foo bar.go", "foo bar\u00e9.go"} {
		t.Run(name, func(t *testing.T) {
			r := newTestRepo(t)
			intro := r.commit("Add a", map[string]string{name: "a\nb\n"})
			r.commit("[proj/bug-1a2b] Fix a", map[string]string{name: "A\nb\n"})
			repo, err := OpenRepo(r.dir)
			if err != nil {
				t.Fatal(err)
			}
			a, err := Attribute(repo, "proj/bug-1a2b")
			if err != nil {
				t.Fatal(err)
			}
			if a.Class != ClassSingle || a.Blamed[0].Commit != intro {
				t.Errorf("attribution = %+v, want single on %s", a, intro)
			}
		})
	}
}

// TestOwnersMatchNormalizedRemote checks a commit captured from a clone whose
// origin is spelled differently from the checkout's still joins to its session.
func TestOwnersMatchNormalizedRemote(t *testing.T) {
	r := newTestRepo(t)
	r.git("remote", "add", "origin", "git@github.com:EnderRealm/loom.git")
	hash := r.commit("Add a", map[string]string{"a.go": "a\n"})
	repo, err := OpenRepo(r.dir)
	if err != nil {
		t.Fatal(err)
	}
	s := &Session{Agent: "claude", SessionID: "s1"}
	owners := ownersIn(repo, []loomCommit{{
		hash:      hash[:7],
		gitRemote: summaries.NormalizeRemote("https://github.com/EnderRealm/loom.git"),
		cwd:       "/elsewhere",
		session:   s,
	}})
	if owners[hash] != s {
		t.Errorf("owners = %v, want %s owned by %+v", owners, hash, s)
	}
}

// TestPrecisionAgainstFixture runs the blame stage over every hand-labelled
// bug, discovering fix commits the way the command does, and scores the bugs
// it classes single against the labels' introducing commits.
func TestPrecisionAgainstFixture(t *testing.T) {
	repos := map[string]*Repo{}
	attrs := map[string]Attribution{}
	for _, l := range loadFixture(t) {
		project, _ := ticket.ParseNamespacedID(l.Ticket)
		repo := repos[project]
		if repo == nil {
			path, err := config.ProjectRepoPath(project)
			if err != nil {
				t.Fatalf("%s: %v", l.Ticket, err)
			}
			if repo, err = OpenRepo(path); err != nil {
				t.Fatalf("%s: %v", l.Ticket, err)
			}
			repos[project] = repo
		}
		a, err := Attribute(repo, l.Ticket)
		if err != nil {
			t.Fatal(err)
		}
		got, want := append([]string{}, a.Fix...), append([]string{}, l.Fix...)
		sort.Strings(got)
		sort.Strings(want)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s: discovered fix commits %v, fixture labels %v", l.Ticket, got, want)
		}
		attrs[l.Ticket] = a
		t.Logf("%s: %s %s %v", l.Ticket, a.Class, a.Reason, a.Blamed)
	}

	correct, singles := Precision(loadFixture(t), attrs)
	if singles == 0 {
		t.Fatal("no fixture bug classed single; precision is undefined")
	}
	precision := float64(correct) / float64(singles)
	t.Logf("precision on fixture blame-stage singles: %d/%d = %.1f%%", correct, singles, 100*precision)
	if precision < 0.60 {
		t.Errorf("precision on fixture blame-stage singles = %.1f%%, want at least 60%%", 100*precision)
	}
}

// TestEveryBugClassifiedOnce runs the whole pipeline against the real tk
// store, the registered repos and this machine's summary DB, and checks the
// JSONL holds every done bug exactly once with one valid class.
func TestEveryBugClassifiedOnce(t *testing.T) {
	dbPath := filepath.Join(config.Home(), "summaries.db")
	if _, err := os.Stat(dbPath); err != nil {
		t.Skipf("no summary DB at %s: the pipeline needs loom's commits table", dbPath)
	}
	rep, err := Run(dbPath, testWriter{t})
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "escapes.jsonl")
	if err := WriteJSONL(out, rep.Records); err != nil {
		t.Fatal(err)
	}

	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	seen := map[string]int{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for sc.Scan() {
		var r Record
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatalf("decode %q: %v", sc.Text(), err)
		}
		seen[r.Ticket]++
		switch r.Class {
		case ClassSingle, ClassMulti:
			if r.Reason != "" {
				t.Errorf("%s: %s with reason %q", r.Ticket, r.Class, r.Reason)
			}
			if (r.Class == ClassSingle) != (len(r.Introducing) == 1) || len(r.Introducing) == 0 {
				t.Errorf("%s: %s with %d introducing commits", r.Ticket, r.Class, len(r.Introducing))
			}
		case ClassUnattributable:
			if r.Reason == "" {
				t.Errorf("%s: unattributable with no reason", r.Ticket)
			}
		default:
			t.Errorf("%s: class %q", r.Ticket, r.Class)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}

	store, err := LoadStore()
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range store.DoneBugs {
		if seen[id] != 1 {
			t.Errorf("%s appears %d times in the JSONL, want 1", id, seen[id])
		}
		delete(seen, id)
	}
	for id := range seen {
		t.Errorf("%s is in the JSONL but is not a done bug", id)
	}
}

// testWriter sends the pipeline's diagnostics to the test log.
type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimSuffix(string(p), "\n"))
	return len(p), nil
}
