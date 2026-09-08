package extract

import (
	"reflect"
	"testing"
)

// The report is what makes onboarding measurable: a scope with no directory has
// to appear with the count of sessions it is costing, or non-use of the
// knowledge layer reads as a decline rather than as a directory nobody created.
func TestScopeStatusCountsPendingAndOnboardedScopes(t *testing.T) {
	e := newEnv(t, "loom", "idle")
	e.addSessionWithTurns("a", loomRemote, 5)
	e.addSessionWithTurns("b", loomRemote, 5)
	e.addSessionWithTurns("pending", warpRemote, 5)
	e.addSessionWithTurns("nowhere", "", 5)
	// Excluded by the threshold the sweep applies, so it is not a session any
	// scope directory would rescue.
	e.addSessionWithTurns("stub", warpRemote, 1)

	rep, err := ScopeStatus()
	if err != nil {
		t.Fatalf("scope status: %v", err)
	}
	want := []ScopeStat{
		{Name: "idle", Onboarded: true},
		{Name: "loom", Onboarded: true, Sessions: 2},
		{Name: "warp", Sessions: 1},
	}
	if !reflect.DeepEqual(rep.Scopes, want) {
		t.Fatalf("scopes = %+v, want %+v", rep.Scopes, want)
	}
	if rep.Unresolved != 1 {
		t.Fatalf("unresolved = %d, want 1 (the session with no remote and no marker)", rep.Unresolved)
	}
	if rep.Eligible != 4 {
		t.Fatalf("eligible = %d, want 4 (the stub is below min-turns=%d)", rep.Eligible, rep.MinTurns)
	}
	if !rep.TruthsDirExists {
		t.Fatal("truths dir reported absent on a store the scopes above came out of")
	}
}

// A host with no summaries.db and no store is an empty report rather than an
// error: `loom status` runs on machines that ship sessions and extract nothing.
func TestScopeStatusDegradesWithoutASummaryDB(t *testing.T) {
	newEnv(t)

	rep, err := ScopeStatus()
	if err != nil {
		t.Fatalf("scope status: %v", err)
	}
	if len(rep.Scopes) != 0 || rep.Eligible != 0 || rep.Unresolved != 0 {
		t.Fatalf("report = %+v, want an empty tally", rep)
	}
	// The fact `loom status` reports the absence from, established here rather
	// than re-stat'd there, where it could drift from the read above.
	if rep.TruthsDirExists {
		t.Fatal("truths dir reported present on a host with no store")
	}
}
