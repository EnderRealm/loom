package config

import (
	"os"
	"path/filepath"
	"strings"
)

// dashboardSortPath returns the path to the file holding the `loom ui`
// dashboard's last sort column under Home().
func dashboardSortPath() string {
	return filepath.Join(Home(), "ui-dashboard-sort")
}

// ReadDashboardSort returns the header of the dashboard's last sort column, or
// "" when none is saved or the file is unreadable. The value is not validated
// here: the tui owns the column set and falls back on a header it does not know.
func ReadDashboardSort() string {
	data, err := os.ReadFile(dashboardSortPath())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// WriteDashboardSort persists header as the dashboard's sort column, creating
// Home() if needed.
func WriteDashboardSort(header string) error {
	if err := os.MkdirAll(Home(), 0o700); err != nil {
		return err
	}
	return os.WriteFile(dashboardSortPath(), []byte(header+"\n"), 0o644)
}
