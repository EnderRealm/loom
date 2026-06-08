package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUnreleasedChangelogEntries(t *testing.T) {
	cases := []struct {
		name      string
		changelog string // empty means: write no CHANGELOG.md at all
		wantCount int
		wantOK    bool
	}{
		{
			name:   "no changelog file",
			wantOK: false,
		},
		{
			name: "unreleased with entries",
			changelog: `# Changelog

## [Unreleased]

### Added

- first thing
- second thing

## [1.0.0] — 2026-01-01

### Added

- old released thing
`,
			wantCount: 2,
			wantOK:    true,
		},
		{
			name: "empty scaffold counts zero",
			changelog: `# Changelog

## [Unreleased]

### Added

## [1.0.0] — 2026-01-01

### Added

- released thing
`,
			wantCount: 0,
			wantOK:    true,
		},
		{
			name: "no unreleased section",
			changelog: `# Changelog

## [1.0.0] — 2026-01-01

### Added

- released thing
`,
			wantCount: 0,
			wantOK:    true,
		},
		{
			name: "stops at next version heading",
			changelog: `# Changelog

## [Unreleased]

- pending one

## [2.0.0]

- not counted
- also not counted
`,
			wantCount: 1,
			wantOK:    true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.changelog != "" {
				if err := os.WriteFile(filepath.Join(dir, "CHANGELOG.md"), []byte(tc.changelog), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			count, ok := unreleasedChangelogEntries(dir)
			if count != tc.wantCount || ok != tc.wantOK {
				t.Fatalf("got (count=%d, ok=%v), want (count=%d, ok=%v)", count, ok, tc.wantCount, tc.wantOK)
			}
		})
	}
}
