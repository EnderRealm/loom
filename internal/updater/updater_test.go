package updater

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// newSourceRepo builds a bare origin with one commit on main and a
// working clone of it, both configured with a local identity so the
// tests don't depend on global git config. It returns the source
// checkout (what the updater operates on) and the bare origin path.
func newSourceRepo(t *testing.T) (src, origin string) {
	t.Helper()
	root := t.TempDir()
	origin = filepath.Join(root, "origin.git")
	git(t, root, "init", "--bare", "--initial-branch=main", origin)

	seed := filepath.Join(root, "seed")
	git(t, root, "clone", origin, seed)
	configIdentity(t, seed)
	writeFile(t, seed, "file.txt", "v1\n")
	git(t, seed, "add", "file.txt")
	git(t, seed, "commit", "-m", "seed")
	git(t, seed, "push", "origin", "main")

	src = filepath.Join(root, "src")
	git(t, root, "clone", origin, src)
	configIdentity(t, src)
	return src, origin
}

// advanceOrigin pushes a new commit to origin/main from a throwaway
// clone so the source checkout sees a remote that moved forward.
func advanceOrigin(t *testing.T, origin, content string) {
	t.Helper()
	work := filepath.Join(t.TempDir(), "advance")
	git(t, "", "clone", origin, work)
	configIdentity(t, work)
	writeFile(t, work, "file.txt", content)
	git(t, work, "add", "file.txt")
	git(t, work, "commit", "-m", "advance")
	git(t, work, "push", "origin", "main")
}

func configIdentity(t *testing.T, dir string) {
	t.Helper()
	git(t, dir, "config", "user.name", "Test")
	git(t, dir, "config", "user.email", "test@example.com")
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func head(t *testing.T, dir string) string {
	t.Helper()
	out, err := gitOutput(context.Background(), dir, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestUpdateSourceUpToDate(t *testing.T) {
	src, _ := newSourceRepo(t)
	before := head(t, src)

	updated, err := updateSource(context.Background(), src)
	if err != nil {
		t.Fatalf("updateSource: %v", err)
	}
	if updated {
		t.Fatal("updated = true, want false for up-to-date checkout")
	}
	if head(t, src) != before {
		t.Fatal("HEAD moved on an up-to-date checkout")
	}
}

func TestUpdateSourceHappyPath(t *testing.T) {
	src, origin := newSourceRepo(t)
	advanceOrigin(t, origin, "v2\n")

	updated, err := updateSource(context.Background(), src)
	if err != nil {
		t.Fatalf("updateSource: %v", err)
	}
	if !updated {
		t.Fatal("updated = false, want true when origin/main moved ahead of a clean main")
	}
	remote, err := gitOutput(context.Background(), src, "rev-parse", "origin/main")
	if err != nil {
		t.Fatal(err)
	}
	if got := head(t, src); got != remote {
		t.Fatalf("HEAD = %s, want origin/main %s", got, remote)
	}
	if got := readFile(t, src, "file.txt"); got != "v2\n" {
		t.Fatalf("file.txt = %q, want updated content", got)
	}
}

func TestUpdateSourceDirtyTreeSkips(t *testing.T) {
	src, origin := newSourceRepo(t)
	advanceOrigin(t, origin, "v2\n")
	before := head(t, src)
	writeFile(t, src, "file.txt", "local edit\n")

	updated, err := updateSource(context.Background(), src)
	if err != nil {
		t.Fatalf("updateSource: %v", err)
	}
	if updated {
		t.Fatal("updated = true, want false with a dirty working tree")
	}
	if got := readFile(t, src, "file.txt"); got != "local edit\n" {
		t.Fatalf("dirty edit clobbered: file.txt = %q", got)
	}
	if head(t, src) != before {
		t.Fatal("HEAD moved despite dirty tree")
	}
}

func TestUpdateSourceFeatureBranchSkips(t *testing.T) {
	src, origin := newSourceRepo(t)
	git(t, src, "checkout", "-b", "feature")
	writeFile(t, src, "feature.txt", "work\n")
	git(t, src, "add", "feature.txt")
	git(t, src, "commit", "-m", "feature work")
	featureSHA := head(t, src)
	advanceOrigin(t, origin, "v2\n")

	updated, err := updateSource(context.Background(), src)
	if err != nil {
		t.Fatalf("updateSource: %v", err)
	}
	if updated {
		t.Fatal("updated = true, want false on a feature branch")
	}
	if got := head(t, src); got != featureSHA {
		t.Fatalf("feature branch ref reset: HEAD = %s, want %s", got, featureSHA)
	}
	branch, _ := gitOutput(context.Background(), src, "rev-parse", "--abbrev-ref", "HEAD")
	if branch != "feature" {
		t.Fatalf("current branch = %q, want feature", branch)
	}
}

func TestUpdateSourceUnpushedCommitSkips(t *testing.T) {
	src, _ := newSourceRepo(t)
	writeFile(t, src, "local.txt", "unpushed\n")
	git(t, src, "add", "local.txt")
	git(t, src, "commit", "-m", "local unpushed work")
	localSHA := head(t, src)

	updated, err := updateSource(context.Background(), src)
	if err != nil {
		t.Fatalf("updateSource: %v", err)
	}
	if updated {
		t.Fatal("updated = true, want false with an unpushed local commit")
	}
	if got := head(t, src); got != localSHA {
		t.Fatalf("local commit lost: HEAD = %s, want %s", got, localSHA)
	}
}

func TestUpdateSourceDetachedHeadSkips(t *testing.T) {
	src, origin := newSourceRepo(t)
	sha := head(t, src)
	git(t, src, "checkout", sha)
	advanceOrigin(t, origin, "v2\n")

	updated, err := updateSource(context.Background(), src)
	if err != nil {
		t.Fatalf("updateSource: %v", err)
	}
	if updated {
		t.Fatal("updated = true, want false on a detached HEAD")
	}
	if head(t, src) != sha {
		t.Fatal("HEAD moved on a detached checkout")
	}
}
