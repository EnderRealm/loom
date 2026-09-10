package source

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"loom/internal/config"
)

// An agent run from a throwaway working directory reports that directory as
// its cwd, which is the one thing loom keys project identity on. The launcher
// knows the checkout the run is about even when the agent deliberately does
// not — warp's codex-lens.sh starts each review lens in an empty `mktemp -d`
// so the reviewed repo cannot instruct its own reviewer — so the launcher
// stamps the association here and the adapter reads it back.
//
// See docs/attribution-stamps.md for the producer contract.

// attributionFile is the registry's path under the loom state root.
const attributionFile = "attribution.jsonl"

// stamp is one producer record: the throwaway root an agent was pointed at,
// the checkout the run is about, and when the producer wrote it.
type stamp struct {
	WorkRoot   string `json:"work_root"`
	ProjectCwd string `json:"project_cwd"`
	StampedAt  string `json:"stamped_at"`

	// at is StampedAt parsed once at load. Zero when the record carried no
	// usable timestamp, which orders it before every dated record.
	at time.Time
}

// stampIndex holds the registry grouped by work root, each group ordered
// oldest-first. Loaded once per List() pass so a sweep sees one consistent
// registry and re-reads it on the next tick.
type stampIndex map[string][]stamp

// loadStamps reads the registry. Every failure mode — no state root, no
// file, an unreadable one, a malformed line — yields an index that resolves
// nothing, because a stamp is an additional source of identity and must
// never become a new way for capture to fail.
func loadStamps() stampIndex {
	f, err := os.Open(filepath.Join(config.Home(), attributionFile))
	if err != nil {
		return nil
	}
	defer f.Close()

	idx := stampIndex{}
	sc := bufio.NewScanner(f)
	// Records are short; the default 64KB token cap is generous, and a line
	// past it is skipped like any other unusable record.
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var s stamp
		if err := json.Unmarshal([]byte(line), &s); err != nil {
			continue
		}
		if s.WorkRoot == "" || s.ProjectCwd == "" {
			continue
		}
		if t, err := time.Parse(time.RFC3339, s.StampedAt); err == nil {
			s.at = t
		}
		root := filepath.Clean(s.WorkRoot)
		idx[root] = append(idx[root], s)
	}
	if err := sc.Err(); err != nil {
		return nil
	}
	for root, group := range idx {
		sortStampsByTime(group)
		idx[root] = group
	}
	return idx
}

// sortStampsByTime orders a work root's records oldest-first. Insertion sort:
// a work root carries one record in the ordinary case and a handful in the
// pathological one.
func sortStampsByTime(group []stamp) {
	for i := 1; i < len(group); i++ {
		for j := i; j > 0 && group[j].at.Before(group[j-1].at); j-- {
			group[j], group[j-1] = group[j-1], group[j]
		}
	}
}

// projectCwd returns the checkout a session running in cwd is about, or ""
// when the registry does not answer for it. start is the session's own start
// time, which decides between records when a work root recurs: the newest
// record stamped at or before the session began wins, so the answer is a
// function of what both sides recorded rather than of when the shipper ticked.
// A session with no usable start time takes the newest record, since without
// a session clock there is nothing better to compare against.
func (idx stampIndex) projectCwd(cwd string, start time.Time) string {
	if idx == nil || cwd == "" {
		return ""
	}
	group := idx[filepath.Clean(cwd)]
	if len(group) == 0 {
		return ""
	}
	if start.IsZero() {
		return group[len(group)-1].ProjectCwd
	}
	for i := len(group) - 1; i >= 0; i-- {
		if !group[i].at.After(start) {
			return group[i].ProjectCwd
		}
	}
	// Every record postdates the session: none of them describes this run.
	return ""
}

// ephemeralRoots are the prefixes under which a path is a throwaway working
// directory rather than a checkout. /var/folders is where macOS puts a
// per-user TMPDIR, and /private is its real location behind the symlink —
// a session reports one or the other depending on how the producer resolved
// the path, so both are listed.
var ephemeralRoots = []string{
	"/tmp/",
	"/private/tmp/",
	"/var/folders/",
	"/private/var/folders/",
	"/var/tmp/",
	"/private/var/tmp/",
}

// isEphemeralCwd reports whether cwd names a throwaway directory, which is
// the only case where a stamp is consulted. $TMPDIR is honored so a machine
// that puts scratch space elsewhere is covered without listing its layout.
func isEphemeralCwd(cwd string) bool {
	if cwd == "" {
		return false
	}
	clean := filepath.Clean(cwd)
	if tmp := os.Getenv("TMPDIR"); tmp != "" {
		if under(clean, filepath.Clean(tmp)) {
			return true
		}
	}
	for _, root := range ephemeralRoots {
		if under(clean, filepath.Clean(root)) {
			return true
		}
	}
	return false
}

// under reports whether path sits inside dir. Both are expected clean; the
// directory itself does not count as being under itself, since a session
// whose cwd IS $TMPDIR is not one of these runs.
func under(path, dir string) bool {
	if dir == "" || dir == "/" {
		return false
	}
	return strings.HasPrefix(path, dir+string(filepath.Separator))
}
