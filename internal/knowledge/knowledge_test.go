package knowledge

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParseArtifactEvidenceAndClaim(t *testing.T) {
	body := `---
id: loom-example-truth
title: Example truth title
scope: loom
type: truth
status: validated
evidence:
  - path: cmd/loom/cmd/relevant.go
    line: 42
    note: the ranker entry point
  - commit: abc123
    note: not a path, must be ignored
  - path: internal/knowledge/relevant.go
sources:
  - session: deadbeef
    project: loom
    path: should-not-leak-into-evidence.go
---

## Claim

The claim text spans this line and continues
onto a second line.

## Why it matters

This must not be captured as claim.
`
	a := parseArtifact(body, "/tmp/x.md", "loom", "truths", "validated")
	if a.ID != "loom-example-truth" {
		t.Errorf("ID = %q", a.ID)
	}
	if a.Title != "Example truth title" {
		t.Errorf("Title = %q", a.Title)
	}
	wantPaths := []string{"cmd/loom/cmd/relevant.go", "internal/knowledge/relevant.go"}
	if !reflect.DeepEqual(a.EvidencePaths, wantPaths) {
		t.Errorf("EvidencePaths = %v, want %v", a.EvidencePaths, wantPaths)
	}
	wantClaim := "The claim text spans this line and continues\nonto a second line."
	if a.Claim != wantClaim {
		t.Errorf("Claim = %q, want %q", a.Claim, wantClaim)
	}
}

func TestParseArtifactFirstNoticed(t *testing.T) {
	cases := []struct {
		name    string
		sources string
		want    string // "" means zero
	}{
		{
			name: "earliest of several wins",
			sources: `sources:
  - session: aaa
    project: loom
    date: 2026-04-08
    role: discovery
  - session: bbb
    date: 2026-01-17
  - date: 2026-06-30
    session: ccc
`,
			want: "2026-01-17",
		},
		{
			name:    "no sources block",
			sources: "",
		},
		{
			name: "undated source entry",
			sources: `sources:
  - session: aaa
    project: loom
    role: discovery
`,
		},
		{
			name: "unparseable date values",
			sources: `sources:
  - session: aaa
    date: <YYYY-MM-DD>
  - session: bbb
    date: 2026-13-45 to x
  - session: ccc
    date: 2026-
`,
		},
		{
			name: "range with a trailing end date",
			sources: `sources:
  - session: aaa
    date: 2026-03-19 to 2026-03-22
`,
			want: "2026-03-19",
		},
		{
			name: "slash-separated range",
			sources: `sources:
  - session: aaa
    date: 2026-03-09/2026-03-10
`,
			want: "2026-03-09",
		},
		{
			name: "earliest wins across ranges and single dates",
			sources: `sources:
  - session: aaa
    date: 2026-03-19 to 2026-03-22
  - session: bbb
    date: 2026-02-04
  - session: ccc
    date: 2026-01-30/2026-02-02
  - session: ddd
    date: <YYYY-MM-DD>
`,
			want: "2026-01-30",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := "---\nid: loom-example\ntitle: Example\n" + tc.sources + "verified_at: 2020-01-01\n---\n\n## Claim\n\nyes\n"
			a := parseArtifact(body, "/tmp/x.md", "loom", "truths", "validated")
			if tc.want == "" {
				if !a.FirstNoticed.IsZero() {
					t.Fatalf("FirstNoticed = %v, want zero", a.FirstNoticed)
				}
				mtime := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
				a.Modified = mtime
				if !a.AgeBasis().Equal(mtime) {
					t.Errorf("AgeBasis = %v, want mtime %v", a.AgeBasis(), mtime)
				}
				return
			}
			if got := a.FirstNoticed.Format("2006-01-02"); got != tc.want {
				t.Fatalf("FirstNoticed = %q, want %q", got, tc.want)
			}
			a.Modified = time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
			if !a.AgeBasis().Equal(a.FirstNoticed) {
				t.Errorf("AgeBasis = %v, want FirstNoticed %v", a.AgeBasis(), a.FirstNoticed)
			}
		})
	}
}

func TestParseArtifactNoFrontmatter(t *testing.T) {
	a := parseArtifact("just a body, no frontmatter\n", "/tmp/x.md", "loom", "truths", "validated")
	if a.ID != "" {
		t.Errorf("expected empty ID for frontmatter-less file, got %q", a.ID)
	}
}

// TestLoadOrdersByFirstNoticed confirms candidates still lead, and that within
// a status group the ordering follows AgeBasis (first-noticed date, mtime
// fallback) rather than mtime alone.
func TestLoadOrdersByFirstNoticed(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LOOM_KNOWLEDGE_ROOT", root)

	write := func(rel, sources string, mtime time.Time) {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		id := strings.TrimSuffix(filepath.Base(rel), ".md")
		body := "---\nid: " + id + "\ntitle: " + id + "\n" + sources + "---\n\n## Claim\n\nyes\n"
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}

	src := func(date string) string {
		return "sources:\n  - session: aaa\n    date: " + date + "\n"
	}

	// mtime order would be no-date, old-date, new-date; first-noticed order is
	// no-date (mtime fallback), new-date, old-date.
	write("truths/loom/no-date.md", "", time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC))
	write("truths/loom/old-date.md", src("2026-01-01"), time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC))
	write("truths/loom/new-date.md", src("2026-06-01"), time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC))
	write("_candidates/truths/loom/cand.md", src("2020-01-01"), time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

	arts, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	var got []string
	for _, a := range arts {
		got = append(got, a.ID)
	}
	want := []string{"cand", "no-date", "new-date", "old-date"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
}

// TestLoadSkipsArtifactsWithoutFrontmatter confirms Load() drops files that
// parse to an empty ID (existing walkArtifacts behavior).
func TestLoadSkipsArtifactsWithoutFrontmatter(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LOOM_KNOWLEDGE_ROOT", root)

	mustWrite := func(rel, content string) {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	mustWrite("truths/loom/good.md", "---\nid: loom-good\ntitle: Good\nstatus: validated\n---\n\n## Claim\n\nyes\n")
	mustWrite("truths/loom/bad.md", "no frontmatter at all\n")

	arts, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(arts) != 1 {
		t.Fatalf("got %d artifacts, want 1 (frontmatter-less skipped)", len(arts))
	}
	if arts[0].ID != "loom-good" {
		t.Errorf("ID = %q, want loom-good", arts[0].ID)
	}
}
