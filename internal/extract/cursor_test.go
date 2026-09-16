package extract

import (
	"context"
	"os"
	"reflect"
	"testing"
	"time"

	"loom/internal/parse/summary"
)

func TestCursorSweepControlsAndLedger(t *testing.T) {
	e := newEnv(t, "loom")
	input := e.addSessionAs(summary.AgentCursor, "cursor", loomRemote, "", 3)
	e.addSessionAs(summary.AgentCursor, "stub", loomRemote, "", 2)
	e.addSessionAs(summary.AgentCursor, "unknown-scope", warpRemote, "", 3)
	if r := sweep(context.Background(), Options{Idle: time.Hour, MinTurns: 3}); r.deferred != 2 || len(e.runs) != 0 {
		t.Fatalf("active sweep = %+v, runs %v", r, e.runs)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(input, old, old); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		sweep(context.Background(), Options{Idle: time.Hour, MinTurns: 3})
	}
	if want := []string{"loom " + input}; !reflect.DeepEqual(e.runs, want) {
		t.Fatalf("runs = %v, want %v", e.runs, want)
	}
	st, err := loadState()
	if err != nil || !st.visited("cursor-cli", "cursor") || st.visited("cursor-cli", "stub") {
		t.Fatalf("ledger = %+v, %v", st, err)
	}
}

func TestCursorHistoricalBackfillAndRetrospect(t *testing.T) {
	e := newRetroEnv(t, "loom")
	e.historical()
	input := e.addSessionAs(summary.AgentCursor, "cursor", loomRemote, "", 3, "["+retroTicket+"] Cursor change")
	sweep(context.Background(), Options{MinTurns: 3})
	backfill(context.Background(), Options{Backfill: true, DryRun: true, MinTurns: 3})
	if len(e.runs) != 0 {
		t.Fatalf("historical sweep or dry run spent allowance: %v", e.runs)
	}
	for i := 0; i < 2; i++ {
		backfill(context.Background(), Options{Backfill: true, Scopes: []string{"loom"}, Limit: 1, MinTurns: 3})
	}
	if len(e.runs) != 1 {
		t.Fatalf("backfill runs = %v", e.runs)
	}
	if err := Retrospect(RetrospectOptions{TicketID: retroTicket}); err != nil {
		t.Fatal(err)
	}
	want := []string{"loom " + input, "loom " + input, "loom " + input}
	if !reflect.DeepEqual(e.runs, want) || !reflect.DeepEqual(e.kinds, []string{extractTypeTruth, extractTypeTruth, extractTypeDecision}) {
		t.Fatalf("retrospect runs = %v, kinds %v", e.runs, e.kinds)
	}
}
