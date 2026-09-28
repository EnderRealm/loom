package escapes

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/EnderRealm/ticket/v8/pkg/ticket"

	"loom/internal/config"
)

// The attribution classes. Every done bug gets exactly one.
const (
	ClassSingle         = "single"
	ClassMulti          = "multi"
	ClassUnattributable = "unattributable"
)

// The reasons a bug is unattributable.
const (
	ReasonNoRepo      = "project has no usable registered repo"
	ReasonNoFix       = "no fix commit found"
	ReasonAddOnly     = "add-only fix"
	ReasonOnlyFixes   = "blamed lines were written by the bug's own fix commits"
	ReasonNotInLoom   = "commit not in loom (human commit or uncaptured session)"
	ReasonBlameFailed = "blame failed"
)

// Attribution is the blame stage's verdict on one bug, independent of loom:
// its fix commits, the commits that wrote the lines those fixes deleted or
// modified, and the class that follows from them.
type Attribution struct {
	Fix    []string
	Blamed []Blamed
	Class  string
	Reason string
}

// Attribute runs the blame stage for one bug in its project's repo. An
// add-only fix is unattributable rather than guessed at: nothing it replaced
// names a culprit.
func Attribute(repo *Repo, ticketID string) (Attribution, error) {
	a := Attribution{Fix: repo.FixCommits(ticketID)}
	if len(a.Fix) == 0 {
		a.Class, a.Reason = ClassUnattributable, ReasonNoFix
		return a, nil
	}
	blamed, oldSide, err := repo.BlameFixes(a.Fix)
	if err != nil {
		return a, fmt.Errorf("%s: %w", ticketID, err)
	}
	a.Blamed = blamed
	switch {
	case len(blamed) == 1:
		a.Class = ClassSingle
	case len(blamed) > 1:
		a.Class = ClassMulti
	case oldSide:
		a.Class, a.Reason = ClassUnattributable, ReasonOnlyFixes
	default:
		a.Class, a.Reason = ClassUnattributable, ReasonAddOnly
	}
	return a, nil
}

// Introducing is one blamed commit in the per-bug record: its share of the
// bug's credit and the loom session that made it, nil when loom has none.
type Introducing struct {
	Commit  string   `json:"commit"`
	Lines   int      `json:"lines"`
	Credit  float64  `json:"credit"`
	Session *Session `json:"session"`
}

// Record is one line of the per-bug JSONL: enough to debug an attribution
// from the file alone.
type Record struct {
	Ticket      string        `json:"ticket"`
	Repo        string        `json:"repo,omitempty"`
	Class       string        `json:"class"`
	Reason      string        `json:"reason,omitempty"`
	Fix         []string      `json:"fix"`
	Introducing []Introducing `json:"introducing"`
}

// Slice is one (agent, model, cli_version) row of the report.
type Slice struct {
	Agent      string
	Model      string
	CLIVersion string
	// Credit is the fractional bug-introducing credit: 1 per single bug,
	// 1/n per introducing commit of a multi bug. One commit can introduce
	// several bugs, so it can exceed Introducing.
	Credit float64
	// Introducing is how many distinct commits in the slice carry any credit.
	Introducing int
	// Commits is every distinct loom commit in the slice that is reachable
	// from HEAD in a registered repo.
	Commits int
}

// Rate is the share of the slice's commits that introduced a bug. Both
// counts are distinct commits and every introducing commit is one of the
// slice's commits, so it never exceeds 1.
func (s Slice) Rate() float64 {
	if s.Commits == 0 {
		return 0
	}
	return float64(s.Introducing) / float64(s.Commits)
}

// Report is the result of one attribution pass. FixtureCorrect and
// FixtureSingles are Precision over the embedded fixture, scored against this
// pass's attributions.
type Report struct {
	Records        []Record
	Slices         []Slice
	FixtureCorrect int
	FixtureSingles int
}

// Run attributes every done bug in the tk store to the loom sessions that
// introduced it, reading loom from dbPath. Problems that cost a project its
// repo, or one bug its blame, are written to diag and leave those bugs
// unattributable.
func Run(dbPath string, diag io.Writer) (*Report, error) {
	store, err := LoadStore()
	if err != nil {
		return nil, err
	}
	commits, err := loadLoomCommits(dbPath)
	if err != nil {
		return nil, err
	}
	labels, err := Fixture()
	if err != nil {
		return nil, err
	}

	repos := map[string]*Repo{}
	owners := map[string]map[string]*Session{} // project → full hash → session
	type sliceKey struct{ agent, model, cli string }
	slices := map[sliceKey]*Slice{}
	slice := func(s *Session) *Slice {
		k := sliceKey{s.Agent, s.Model, s.CLIVersion}
		if slices[k] == nil {
			slices[k] = &Slice{Agent: s.Agent, Model: s.Model, CLIVersion: s.CLIVersion}
		}
		return slices[k]
	}
	for _, p := range store.Projects {
		path, err := config.ProjectRepoPath(p)
		if err != nil {
			fmt.Fprintf(diag, "escapes: %s: %v\n", p, err)
			continue
		}
		repo, err := OpenRepo(path)
		if err != nil {
			fmt.Fprintf(diag, "escapes: %s: %v\n", p, err)
			continue
		}
		repos[p] = repo
		owners[p] = ownersIn(repo, commits)
		for _, s := range owners[p] {
			slice(s).Commits++
		}
	}

	var rep Report
	credited := map[string]bool{}
	attrs := map[string]Attribution{}
	for _, id := range store.DoneBugs {
		project, _ := ticket.ParseNamespacedID(id)
		rec := Record{Ticket: id, Fix: []string{}, Introducing: []Introducing{}}
		repo := repos[project]
		if repo == nil {
			rec.Class, rec.Reason = ClassUnattributable, ReasonNoRepo
			rep.Records = append(rep.Records, rec)
			continue
		}
		rec.Repo = repo.Path
		a, err := Attribute(repo, id)
		rec.Fix = append(rec.Fix, a.Fix...)
		if err != nil {
			fmt.Fprintf(diag, "escapes: %v\n", err)
			rec.Class, rec.Reason = ClassUnattributable, ReasonBlameFailed
			rep.Records = append(rep.Records, rec)
			continue
		}
		attrs[id] = a
		rec.Class, rec.Reason = a.Class, a.Reason
		inLoom := false
		for _, b := range a.Blamed {
			in := Introducing{Commit: b.Commit, Lines: b.Lines, Credit: 1 / float64(len(a.Blamed)), Session: owners[project][b.Commit]}
			if in.Session != nil {
				inLoom = true
			}
			rec.Introducing = append(rec.Introducing, in)
		}
		if len(a.Blamed) > 0 && !inLoom {
			rec.Class, rec.Reason = ClassUnattributable, ReasonNotInLoom
		}
		if rec.Class != ClassUnattributable {
			for _, in := range rec.Introducing {
				if in.Session == nil {
					continue
				}
				sl := slice(in.Session)
				sl.Credit += in.Credit
				if !credited[in.Commit] {
					credited[in.Commit] = true
					sl.Introducing++
				}
			}
		}
		rep.Records = append(rep.Records, rec)
	}

	rep.FixtureCorrect, rep.FixtureSingles = Precision(labels, attrs)
	for _, s := range slices {
		rep.Slices = append(rep.Slices, *s)
	}
	sort.Slice(rep.Slices, func(i, j int) bool {
		a, b := rep.Slices[i], rep.Slices[j]
		if a.Agent != b.Agent {
			return a.Agent < b.Agent
		}
		if a.Model != b.Model {
			return a.Model < b.Model
		}
		return a.CLIVersion < b.CLIVersion
	})
	return &rep, nil
}

// WriteJSONL writes one record per line to path.
func WriteJSONL(path string, recs []Record) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	for _, r := range recs {
		if err := enc.Encode(r); err != nil {
			f.Close()
			return err
		}
	}
	return f.Close()
}

// Render prints the per-slice table — distinct introducing commits, all
// commits, their ratio and the fractional credit — a total row, the class
// counts with the unattributable bugs broken down by reason, and precision
// on the fixture's singles.
func Render(w io.Writer, rep *Report) {
	const format = "%-12s %-22s %-12s %11s %8s %7s %7s\n"
	fmt.Fprintf(w, format, "agent", "model", "cli_version", "introducing", "commits", "rate", "credit")
	var total Slice
	for _, s := range rep.Slices {
		fmt.Fprintf(w, format, dash(s.Agent), dash(s.Model), dash(s.CLIVersion),
			fmt.Sprint(s.Introducing), fmt.Sprint(s.Commits), pct(s.Rate()), fmt.Sprintf("%.2f", s.Credit))
		total.Credit += s.Credit
		total.Introducing += s.Introducing
		total.Commits += s.Commits
	}
	fmt.Fprintf(w, format, "total", "", "", fmt.Sprint(total.Introducing), fmt.Sprint(total.Commits),
		pct(total.Rate()), fmt.Sprintf("%.2f", total.Credit))

	classes := map[string]int{}
	reasons := map[string]int{}
	for _, r := range rep.Records {
		classes[r.Class]++
		if r.Class == ClassUnattributable {
			reasons[r.Reason]++
		}
	}
	fmt.Fprintf(w, "\ndone bugs %d: single %d, multi %d, unattributable %d\n",
		len(rep.Records), classes[ClassSingle], classes[ClassMulti], classes[ClassUnattributable])
	keys := make([]string, 0, len(reasons))
	for k := range reasons {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if reasons[keys[i]] != reasons[keys[j]] {
			return reasons[keys[i]] > reasons[keys[j]]
		}
		return keys[i] < keys[j]
	})
	for _, k := range keys {
		fmt.Fprintf(w, "  unattributable %4d  %s\n", reasons[k], k)
	}
	if rep.FixtureSingles == 0 {
		fmt.Fprintln(w, "precision on fixture blame-stage singles: no fixture bug classed single")
	} else {
		fmt.Fprintf(w, "precision on fixture blame-stage singles: %d/%d = %s\n", rep.FixtureCorrect, rep.FixtureSingles,
			pct(float64(rep.FixtureCorrect)/float64(rep.FixtureSingles)))
	}
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func pct(f float64) string {
	return fmt.Sprintf("%.1f%%", 100*f)
}

// Precision scores the blame stage against the hand-labelled fixture: of the
// labelled bugs attrs classes single, how many name a commit the label lists
// as introducing.
func Precision(labels []Label, attrs map[string]Attribution) (correct, singles int) {
	for _, l := range labels {
		a, ok := attrs[l.Ticket]
		if !ok || a.Class != ClassSingle {
			continue
		}
		singles++
		for _, h := range l.Introducing {
			if h == a.Blamed[0].Commit {
				correct++
				break
			}
		}
	}
	return correct, singles
}
