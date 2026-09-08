package extract

import (
	"errors"
	"os"
	"sort"
	"time"

	"loom/internal/parse/summary"
	"loom/internal/summaries"
)

// ScopeStat is one knowledge scope's onboarding state: whether the store holds
// truths/<Name>/, and how many of this host's eligible sessions resolve to it.
// A stat with Onboarded false is a project accumulating nothing — its sessions
// are the ones the sweep declines for want of a directory.
type ScopeStat struct {
	Name      string
	Onboarded bool
	Sessions  int
}

// ScopeReport is what `loom status` prints about scope onboarding. It counts
// the whole summary DB rather than one sweep's window: the backlog a scope
// would rescue is the number that decides whether onboarding it is worth
// anything.
type ScopeReport struct {
	TruthsDir string
	// TruthsDirExists reports whether that tree was there to read. Carried
	// rather than left to the caller to stat again, so the answer is the one the
	// scopes below were derived from. False means no scope has been onboarded on
	// this machine, which is not the same as no store.
	TruthsDirExists bool
	// Scopes holds every scope the store has, at zero sessions if that is the
	// honest number, and every scope sessions resolve to that the store lacks.
	Scopes []ScopeStat
	// Unresolved counts sessions with no derivable scope at all — no marker
	// naming a scope this store has, and no usable git remote — which no
	// directory fixes.
	Unresolved int
	Eligible   int
	MinTurns   int
}

// ScopeStatus tallies scope onboarding across every summarized session.
//
// The sweep's filters up to scope resolution and no further: the idle stat and
// the ledger describe one sweep's decisions, where this describes what a scope
// is worth. Nor is the ledger consulted — after the sweep stopped recording
// scope skips a pending scope has no ledger entries to read, and the honest
// number for an onboarded scope is its total.
//
// A host with no summaries.db reports an empty tally rather than an error, per
// LoadSessionSources' degrade-silently contract, and so does one with no
// knowledge store.
func ScopeStatus() (ScopeReport, error) {
	rep := ScopeReport{TruthsDir: TruthsDir(), MinTurns: DefaultMinTurns()}

	counts := map[string]int{}
	onboarded := map[string]bool{}
	// A tree that isn't there yet has no scopes, which is the same answer as an
	// empty one; the error is kept only to tell those two apart for the report.
	entries, err := os.ReadDir(rep.TruthsDir)
	rep.TruthsDirExists = err == nil
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		onboarded[e.Name()] = true
		// Seeded so a scope that exists and has drawn nothing reads as zero
		// rather than as absent.
		counts[e.Name()] = 0
	}

	// Unbounded by the watermark: the backlog the watermark excludes is most of
	// what a newly created scope would rescue.
	sessions, err := summaries.LoadSessionSources(time.Time{})
	if err != nil {
		return ScopeReport{}, err
	}
	for _, s := range sessions {
		if s.Agent != string(summary.AgentClaude) || belowMinTurns(s, rep.MinTurns) {
			continue
		}
		rep.Eligible++
		// The real derivation, so the report says what the sweep would do, with
		// the marker warnings dropped: this is a read of state, not a run.
		res, err := resolveScope(s.CwdRaw, s.GitRemote, discardLog{})
		if err != nil {
			var unknown errUnknownScope
			if errors.As(err, &unknown) {
				counts[unknown.scope]++
				continue
			}
			rep.Unresolved++
			continue
		}
		counts[res.scope]++
	}

	for name, n := range counts {
		rep.Scopes = append(rep.Scopes, ScopeStat{Name: name, Onboarded: onboarded[name], Sessions: n})
	}
	sort.Slice(rep.Scopes, func(i, j int) bool { return rep.Scopes[i].Name < rep.Scopes[j].Name })
	return rep, nil
}
