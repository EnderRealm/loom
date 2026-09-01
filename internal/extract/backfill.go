package extract

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"time"

	"loom/internal/parse/summary"
	"loom/internal/summaries"
)

// Exclusion buckets for the backfill's report. The full reason is logged per
// session; these labels are what the summary counts, so a run over hundreds of
// sessions reads at a glance.
const (
	reasonVisited      = "already visited"
	reasonAgent        = "unsupported agent"
	reasonBelowTurns   = "below turn threshold"
	reasonUnreadable   = "artifact unreadable"
	reasonActive       = "still being written"
	reasonNoRemote     = "no git remote"
	reasonUnknownScope = "unknown scope"
	reasonOtherScope   = "outside --scope"
)

// progressEvery paces the running-count line. A full backfill is hundreds of
// sessions at minutes each, so an operator tailing extractor.log needs to see
// how far along it is without counting per-session lines.
const progressEvery = 10

type backfillResult struct {
	extracted, failed, skipped int
}

// backfillItem is one selected session and the scope it resolved to, so the
// run doesn't re-derive what the selection pass already decided.
type backfillItem struct {
	src summaries.SessionSource
	res resolution
}

// backfillPlan is what the run will do, decided before anything is spent so a
// dry run and a real run report the same thing. Scanned is short of the whole
// DB when --limit ended the pass early, which is what makes the excluded
// counts readable as a partial tally rather than the full backlog's shape.
type backfillPlan struct {
	items    []backfillItem
	scanned  int
	byScope  map[string]int
	bySource map[string]int
	excluded map[string]int
}

// backfill extracts the historical backlog: sessions summarized before the
// trigger stamped its watermark, which sweep deliberately leaves alone. It is
// operator-initiated — one pass, no watch loop, and bounded by Scopes/Limit
// rather than by maxPerSweep, so clearing hundreds of sessions isn't paced at
// four per tick. It shares the trigger's ledger, so neither can double-spend
// on a session the other already visited — consulted per session rather than
// only when the plan is made, since the two run over the same hours.
func backfill(ctx context.Context, opts Options) backfillResult {
	r := backfillResult{}

	script, err := ScriptPath()
	if err != nil {
		log.Printf("extractor unavailable: %v", err)
		return r
	}
	load := loadState
	if opts.DryRun {
		load = peekState
	}
	st, err := load()
	if err != nil {
		log.Printf("load state: %v", err)
		return r
	}
	// No watermark bound: the backlog the watermark excludes is precisely what
	// this run is for.
	sessions, err := summaries.LoadSessionSources(time.Time{})
	if err != nil {
		log.Printf("load sessions: %v", err)
		return r
	}

	plan := planBackfill(st, sessions, opts)

	label := "backfill"
	if opts.DryRun {
		label = "backfill dry run"
	}
	line := fmt.Sprintf("%s: scanned %d/%d sessions, %d to extract", label, plan.scanned, len(sessions), len(plan.items))
	if len(plan.byScope) > 0 {
		line += " (" + formatCounts(plan.byScope) + ")"
	}
	// A dry run never reaches extractOne, so this is the only place its
	// derivations are accounted for.
	if len(plan.bySource) > 0 {
		line += " via " + formatCounts(plan.bySource)
	}
	// Stated only when the threshold is in force: two dry runs at different
	// thresholds otherwise leave indistinguishable lines in extractor.log.
	if opts.MinTurns > 0 {
		line += fmt.Sprintf(" min-turns=%d", opts.MinTurns)
	}
	log.Print(line)
	if len(plan.excluded) > 0 {
		log.Printf("%s: %d excluded (%s)", label, total(plan.excluded), formatCounts(plan.excluded))
	}
	if opts.DryRun {
		return r
	}

	start := time.Now()
	for i, item := range plan.items {
		if ctx.Err() != nil {
			break
		}
		// The plan is a snapshot taken before the first extraction, and a
		// backfill is unbounded by the watermark, so its selection overlaps the
		// sessions the installed trigger sweeps during the same hours. Re-read
		// the ledger per session: one the trigger claimed since the plan was
		// computed is dropped rather than extracted — and paid for — twice.
		claimed, err := visitedOnDisk(item.src.Agent, item.src.SessionID)
		if err != nil {
			// The ledger is what makes at-most-once true; without a reading of
			// it, extracting risks a second charge for the same session.
			r.skipped++
			logSkip(item.src, fmt.Sprintf("ledger unreadable: %v", err))
			continue
		}
		if claimed {
			r.skipped++
			logSkip(item.src, "claimed by another run since the plan was computed")
			continue
		}

		outcome := extractOne(ctx, st, script, item.src, item.res)
		if outcome == "" {
			// Interrupted; the session stays unvisited so a restart resumes here.
			break
		}
		if outcome == outcomeFailed {
			r.failed++
		} else {
			r.extracted++
		}
		if n := i + 1; n%progressEvery == 0 && n < len(plan.items) {
			log.Printf("backfill progress: %d/%d (extracted=%d failed=%d skipped=%d) elapsed=%s",
				n, len(plan.items), r.extracted, r.failed, r.skipped, time.Since(start).Round(time.Second))
		}
	}

	log.Printf("backfill done: extracted=%d failed=%d skipped=%d of %d selected in %s",
		r.extracted, r.failed, r.skipped, len(plan.items), time.Since(start).Round(time.Second))
	return r
}

// planBackfill classifies every summarized session into what the run will
// extract — in order, capped at opts.Limit so the run stops at the bound
// rather than mid-session — and counts what it excluded and why.
//
// Skips are reported here, not recorded. sweep persists outcomeSkipped so it
// doesn't re-log every tick; a backfill is operator-initiated and re-runnable,
// and most of the backlog's skips are "unknown scope" — recording those would
// mean creating truths/<scope>/ later could never rescue the very sessions it
// was created for.
func planBackfill(st *state, sessions []summaries.SessionSource, opts Options) backfillPlan {
	want := map[string]bool{}
	for _, scope := range wantedScopes(opts.Scopes) {
		want[scope] = true
	}

	var items []backfillItem
	byScope := map[string]int{}
	bySource := map[string]int{}
	excluded := map[string]int{}

	// A backfill resolves the whole DB, so a repo's marker facts would otherwise
	// be restated once per session ahead of the report below.
	seen := logOnce{}

	scanned := 0
	for _, s := range sessions {
		if opts.Limit > 0 && len(items) >= opts.Limit {
			break
		}
		scanned++
		if st.visited(s.Agent, s.SessionID) {
			// Counted but not logged: on a resumed run this is most of the DB.
			excluded[reasonVisited]++
			continue
		}
		if s.Agent != string(summary.AgentClaude) {
			excluded[reasonAgent]++
			logSkip(s, fmt.Sprintf("unsupported agent %q (preprocess.py reads claude-code jsonl only)", s.Agent))
			continue
		}
		if belowMinTurns(s, opts.MinTurns) {
			// Counted but not logged, as with reasonVisited: at the default
			// threshold this is most of the DB.
			excluded[reasonBelowTurns]++
			continue
		}
		info, err := os.Stat(s.SourcePath)
		if err != nil {
			excluded[reasonUnreadable]++
			logSkip(s, fmt.Sprintf("artifact unreadable: %v", err))
			continue
		}
		if time.Since(info.ModTime()) < opts.Idle {
			// A live session's jsonl is still growing; extracting the partial
			// copy would record it as visited and lose the rest.
			excluded[reasonActive]++
			continue
		}
		res, err := resolveScope(s.CwdRaw, s.GitRemote, seen)
		if err != nil {
			// errNoRemote still means "nothing named this session's project":
			// resolveScope only reaches it once the marker derivation has
			// failed too, so the bucket keeps counting the same sessions.
			if errors.Is(err, errNoRemote) {
				excluded[reasonNoRemote]++
			} else {
				excluded[reasonUnknownScope]++
			}
			logSkip(s, err.Error())
			continue
		}
		if len(want) > 0 && !want[res.scope] {
			excluded[reasonOtherScope]++
			continue
		}
		items = append(items, backfillItem{src: s, res: res})
		byScope[res.scope]++
		bySource[res.source]++
	}
	return backfillPlan{items: items, scanned: scanned, byScope: byScope, bySource: bySource, excluded: excluded}
}

// wantedScopes normalizes the --scope values into the shape resolveScope
// produces. Lowercased because a resolved scope is lowercase by construction —
// summaries.NormalizeRemote lowercases and scopePattern requires a leading
// [a-z0-9] — so "Loom" would otherwise match nothing and push the whole
// backlog into the "outside --scope" bucket. Empty entries are dropped for the
// same reason: one would make the filter active and exclude everything.
func wantedScopes(scopes []string) []string {
	var want []string
	for _, s := range scopes {
		if s = strings.ToLower(strings.TrimSpace(s)); s != "" {
			want = append(want, s)
		}
	}
	return want
}

// validateScopes rejects a --scope value no session could ever resolve to,
// against the same truths/<scope>/ check resolveScope applies. Rejecting rather
// than logging an unmatched scope after the fact: a run that selects nothing
// reports "0 to extract" and exits 0, which is indistinguishable from an
// already-cleared backlog, and the operator's next move on that reading is to
// stop rather than to look. A scope that exists but holds no unvisited sessions
// still reports zero, which is the honest answer.
func validateScopes(scopes []string) error {
	for _, scope := range wantedScopes(scopes) {
		// The same gate, not a copy of it: a requested value that couldn't clear
		// what a derived scope clears can never match one.
		if err := validScope(scope); err != nil {
			return fmt.Errorf("--scope: %w", err)
		}
	}
	return nil
}

func total(counts map[string]int) int {
	n := 0
	for _, c := range counts {
		n += c
	}
	return n
}

// formatCounts renders a breakdown in a fixed order, since map iteration would
// otherwise reshuffle the report on every run.
func formatCounts(counts map[string]int) string {
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, counts[k]))
	}
	return strings.Join(parts, ", ")
}
