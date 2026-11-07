package config

import "testing"

func TestDashboardSortRoundTrip(t *testing.T) {
	t.Setenv("LOOM_HOME", t.TempDir())
	if got := ReadDashboardSort(); got != "" {
		t.Fatalf("ReadDashboardSort on missing file = %q, want \"\"", got)
	}
	if err := WriteDashboardSort("SIZE"); err != nil {
		t.Fatalf("WriteDashboardSort: %v", err)
	}
	if got := ReadDashboardSort(); got != "SIZE" {
		t.Fatalf("ReadDashboardSort after write = %q, want SIZE", got)
	}
}
