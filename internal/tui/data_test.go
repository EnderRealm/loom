package tui

import (
	"testing"
	"time"

	"github.com/EnderRealm/ticket/v7/pkg/ticket"
)

// TestBucketTicketActivity covers the 24h ticket window logic: created/closed
// bucketing, the Completed zero-value guard (non-terminal tickets must not be
// counted as closed), window boundary exclusion, and newest-first ordering.
func TestBucketTicketActivity(t *testing.T) {
	now := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)
	since := now.Add(-24 * time.Hour)

	tickets := []*ticket.Ticket{
		// Created in-window, still open (Completed zero): created only.
		{ID: "loom/recent-open", Title: "recent open", Status: ticket.StatusOpen,
			Created: now.Add(-2 * time.Hour)},
		// Created before window, closed in-window: closed only.
		{ID: "ticket/old-but-closed", Title: "old but closed", Status: ticket.StatusDone,
			Created: now.Add(-72 * time.Hour), Completed: now.Add(-1 * time.Hour)},
		// Created and closed in-window: both lists.
		{ID: "loom/quick", Title: "quick turnaround", Status: ticket.StatusDone,
			Created: now.Add(-5 * time.Hour), Completed: now.Add(-30 * time.Minute)},
		// Fully outside the window: neither list.
		{ID: "loom/ancient", Title: "ancient", Status: ticket.StatusDone,
			Created: now.Add(-200 * time.Hour), Completed: now.Add(-150 * time.Hour)},
		// Created just before the cutoff (boundary exclusive): not created.
		{ID: "loom/boundary", Title: "boundary", Status: ticket.StatusOpen,
			Created: since.Add(-time.Second)},
	}

	ta := bucketTicketActivity(tickets, since)

	if !ta.Available {
		t.Error("Available: got false, want true")
	}

	createdIDs := changeIDs(ta.Created)
	wantCreated := []string{"loom/recent-open", "loom/quick"} // newest-first
	if !equalStrings(createdIDs, wantCreated) {
		t.Errorf("created: got %v, want %v", createdIDs, wantCreated)
	}

	closedIDs := changeIDs(ta.Closed)
	wantClosed := []string{"loom/quick", "ticket/old-but-closed"} // newest-first
	if !equalStrings(closedIDs, wantClosed) {
		t.Errorf("closed: got %v, want %v", closedIDs, wantClosed)
	}

	// Project is parsed from the namespaced ID.
	if len(ta.Created) > 0 && ta.Created[0].Project != "loom" {
		t.Errorf("created[0].Project: got %q, want loom", ta.Created[0].Project)
	}
}

func changeIDs(cs []TicketChange) []string {
	ids := make([]string, len(cs))
	for i, c := range cs {
		ids[i] = c.ID
	}
	return ids
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
