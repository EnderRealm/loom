package cmd

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"loom/internal/config"
	"loom/internal/extract"
	"loom/internal/updater"
	"loom/transport/receiver"
	"loom/transport/shipper"
)

func TestExpectedDaemons(t *testing.T) {
	cases := []struct {
		name             string
		role             string
		updaterInstalled bool
		want             []string
	}{
		{
			name: "server without updater",
			role: config.RoleServer,
			want: []string{receiver.AgentLabel, summarizerLabel, extract.AgentLabel},
		},
		{
			name:             "server with updater",
			role:             config.RoleServer,
			updaterInstalled: true,
			want:             []string{receiver.AgentLabel, summarizerLabel, extract.AgentLabel, updater.AgentLabel},
		},
		{
			name: "remote without updater",
			role: config.RoleRemote,
			want: []string{shipper.AgentLabel},
		},
		{
			name:             "remote with updater",
			role:             config.RoleRemote,
			updaterInstalled: true,
			want:             []string{shipper.AgentLabel, updater.AgentLabel},
		},
		{
			name: "empty role falls back to all four (updater not installed)",
			role: "",
			want: []string{receiver.AgentLabel, summarizerLabel, shipper.AgentLabel, updater.AgentLabel},
		},
		{
			name:             "empty role falls back to all four (updater installed)",
			role:             "",
			updaterInstalled: true,
			want:             []string{receiver.AgentLabel, summarizerLabel, shipper.AgentLabel, updater.AgentLabel},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := expectedDaemons(tc.role, tc.updaterInstalled)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("expectedDaemons(%q, %v) = %v, want %v", tc.role, tc.updaterInstalled, got, tc.want)
			}
		})
	}
}

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
