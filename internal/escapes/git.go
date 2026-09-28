package escapes

import (
	"bufio"
	"bytes"
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/EnderRealm/ticket/v8/pkg/ticket"

	"loom/internal/summaries"
)

// generatedPaths are repo-relative path prefixes whose lines are output, not
// authored code: a fix that regenerates them says nothing about who wrote the
// defect, so their lines are never blamed.
var generatedPaths = []string{
	"tests/golden/",
}

func generated(path string) bool {
	for _, p := range generatedPaths {
		if strings.HasPrefix(path, p) {
			return true
		}
	}
	return false
}

// Repo is one registered project checkout as attribution reads it: the
// non-merge history reachable from HEAD, with each commit's parents and the
// commits whose subject marker names a ticket.
type Repo struct {
	Path   string
	Remote string // origin URL as summaries.NormalizeRemote forms it, "" when the repo has none

	parents map[string][]string // full hash → parent hashes
	fixes   map[string][]string // marker id → full hashes, oldest first
	byShort map[string][]string // 7-char prefix → full hashes
}

// OpenRepo reads the history reachable from HEAD in one `git log` and the
// origin URL. Merge commits are left out: a merge carrying a ticket marker
// only brings a fix in, and its diff against the first parent is not the fix.
func OpenRepo(path string) (*Repo, error) {
	out, err := git(path, "log", "--no-merges", "--reverse", "--format=%H%x00%P%x00%s", "HEAD")
	if err != nil {
		return nil, err
	}
	r := &Repo{
		Path:    path,
		parents: map[string][]string{},
		fixes:   map[string][]string{},
		byShort: map[string][]string{},
	}
	for _, line := range strings.Split(strings.TrimSuffix(string(out), "\n"), "\n") {
		f := strings.SplitN(line, "\x00", 3)
		if len(f) != 3 || len(f[0]) < 7 {
			continue
		}
		hash := f[0]
		r.parents[hash] = strings.Fields(f[1])
		r.byShort[hash[:7]] = append(r.byShort[hash[:7]], hash)
		if id, ok := summaries.MarkerTicketID(f[2]); ok {
			r.fixes[id] = append(r.fixes[id], hash)
		}
	}
	// A repo with no origin is matched to loom commits by cwd alone.
	if remote, err := git(path, "remote", "get-url", "origin"); err == nil {
		r.Remote = summaries.NormalizeRemote(string(remote))
	}
	return r, nil
}

// FixCommits returns the commits whose marker names ticketID, oldest first.
// Older commits carry the bare id, newer ones the qualified form, so both are
// read; the repo belongs to the ticket's project, so a bare id is unambiguous.
func (r *Repo) FixCommits(ticketID string) []string {
	_, bare := ticket.ParseNamespacedID(ticketID)
	seen := map[string]bool{}
	var out []string
	for _, id := range []string{ticketID, bare} {
		for _, h := range r.fixes[id] {
			if !seen[h] {
				seen[h] = true
				out = append(out, h)
			}
		}
	}
	return out
}

// Resolve returns the one HEAD-reachable commit an abbreviated hash names,
// false when none does or the abbreviation is ambiguous.
func (r *Repo) Resolve(short string) (string, bool) {
	if len(short) < 7 {
		return "", false
	}
	var match string
	for _, h := range r.byShort[short[:7]] {
		if strings.HasPrefix(h, short) {
			if match != "" {
				return "", false
			}
			match = h
		}
	}
	return match, match != ""
}

// lineRange is an inclusive 1-based line range in a file's old side.
type lineRange struct{ start, end int }

// hunkRe reads a unified hunk header's old side: start and optional count.
var hunkRe = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+\d+(?:,(\d+))? @@`)

// oldSideRanges diffs fix against parent ignoring whitespace and returns, per
// old-side path, the line ranges the fix deleted or modified. Pure additions
// have no old side and contribute nothing; generated paths are dropped.
func (r *Repo) oldSideRanges(parent, fix string) (map[string][]lineRange, error) {
	out, err := git(r.Path, "diff", "-w", "-U0", "-M", "--no-color", "--no-ext-diff", "--no-textconv",
		"--src-prefix=a/", "--dst-prefix=b/", parent, fix)
	if err != nil {
		return nil, err
	}
	ranges := map[string][]lineRange{}
	var path string
	oldLeft, newLeft := 0, 0
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 1024*1024), 64*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		// Inside a hunk every line is content, even one reading like a header.
		if oldLeft > 0 || newLeft > 0 {
			switch {
			case strings.HasPrefix(line, "-"):
				oldLeft--
			case strings.HasPrefix(line, "+"):
				newLeft--
			}
			continue
		}
		switch {
		case strings.HasPrefix(line, "diff --git "):
			path = ""
		case strings.HasPrefix(line, "--- "):
			path = diffPath(strings.TrimPrefix(line, "--- "), "a/")
		case strings.HasPrefix(line, "@@ "):
			m := hunkRe.FindStringSubmatch(line)
			if m == nil {
				return nil, fmt.Errorf("git diff %s %s: unreadable hunk header %q", parent, fix, line)
			}
			start, _ := strconv.Atoi(m[1])
			oldLeft, newLeft = 1, 1
			if m[2] != "" {
				oldLeft, _ = strconv.Atoi(m[2])
			}
			if m[3] != "" {
				newLeft, _ = strconv.Atoi(m[3])
			}
			if oldLeft > 0 && path != "" && !generated(path) {
				ranges[path] = append(ranges[path], lineRange{start, start + oldLeft - 1})
			}
		}
	}
	return ranges, sc.Err()
}

// diffPath strips a diff header path's prefix, "" for /dev/null. Git ends a
// path holding a space with a tab, for GNU patch, and C-quotes a path holding
// unusual bytes; Go's unquoting reads that form.
func diffPath(p, prefix string) string {
	p = strings.TrimSuffix(p, "\t")
	if p == "/dev/null" {
		return ""
	}
	if strings.HasPrefix(p, `"`) {
		if u, err := strconv.Unquote(p); err == nil {
			p = u
		}
	}
	return strings.TrimPrefix(p, prefix)
}

// porcelainHeaderRe matches the header git blame --porcelain prints before
// every blamed line: the commit, then original and final line numbers.
var porcelainHeaderRe = regexp.MustCompile(`^([0-9a-f]{40}) \d+ \d+`)

// blame counts, per commit, the lines of path at rev within ranges, blamed
// with whitespace ignored so a reindent is looked through to the line's
// author.
func (r *Repo) blame(rev, path string, ranges []lineRange) (map[string]int, error) {
	args := []string{"blame", "-w", "--porcelain"}
	for _, lr := range ranges {
		args = append(args, "-L", fmt.Sprintf("%d,%d", lr.start, lr.end))
	}
	args = append(args, rev, "--", path)
	out, err := git(r.Path, args...)
	if err != nil {
		return nil, err
	}
	counts := map[string]int{}
	var cur string
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "\t") {
			counts[cur]++
			continue
		}
		if m := porcelainHeaderRe.FindStringSubmatch(line); m != nil {
			cur = m[1]
		}
	}
	return counts, nil
}

// Blamed is one commit that last touched lines a fix deleted or modified.
type Blamed struct {
	Commit string `json:"commit"`
	Lines  int    `json:"lines"`
}

// BlameFixes blames the lines each fix deleted or modified, at the fix's own
// parent, and returns the commits that wrote them, most lines first. Lines
// written by another of the same bug's fixes are dropped: a follow-up fix
// correcting the first is not an introduction. oldSide reports whether any
// fix deleted or modified a line at all, which separates an add-only fix from
// one whose replaced lines were all the bug's own. A root-commit fix has no
// parent and so no old side.
func (r *Repo) BlameFixes(fixes []string) (blamed []Blamed, oldSide bool, err error) {
	isFix := map[string]bool{}
	for _, f := range fixes {
		isFix[f] = true
	}
	total := map[string]int{}
	for _, fix := range fixes {
		parents := r.parents[fix]
		if len(parents) == 0 {
			continue
		}
		ranges, err := r.oldSideRanges(parents[0], fix)
		if err != nil {
			return nil, false, err
		}
		if len(ranges) > 0 {
			oldSide = true
		}
		paths := make([]string, 0, len(ranges))
		for p := range ranges {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		for _, p := range paths {
			counts, err := r.blame(parents[0], p, ranges[p])
			if err != nil {
				return nil, false, err
			}
			for h, n := range counts {
				if !isFix[h] {
					total[h] += n
				}
			}
		}
	}
	blamed = make([]Blamed, 0, len(total))
	for h, n := range total {
		blamed = append(blamed, Blamed{Commit: h, Lines: n})
	}
	sort.Slice(blamed, func(i, j int) bool {
		if blamed[i].Lines != blamed[j].Lines {
			return blamed[i].Lines > blamed[j].Lines
		}
		return blamed[i].Commit < blamed[j].Commit
	})
	return blamed, oldSide, nil
}

// git runs git against repo with args as argv, never through a shell: the
// hashes and paths are repo and config data.
func git(repo string, args ...string) ([]byte, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git -C %s %s: %v: %s", repo, strings.Join(args, " "), err, bytes.TrimSpace(stderr.Bytes()))
	}
	return stdout.Bytes(), nil
}
