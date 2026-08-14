package extract

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"unicode"
)

// newCheckout builds a git checkout on this host, optionally holding a
// .loom-project marker — the shape a session's recorded cwd points at. The path
// is symlink-resolved because the walk resolves the cwd before reading, and the
// marker path it logs is the resolved one.
func newCheckout(t *testing.T, marker string) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if marker != "" {
		writeMarker(t, dir, marker)
	}
	return dir
}

// writeMarker plants a marker in dir and returns dir, so a fixture that needs
// one below a checkout's root still reads as a single line.
func writeMarker(t *testing.T, dir, value string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, markerName), []byte(value), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// subdir creates name under parent and returns it — the cwd of a session
// captured below the checkout's root.
func subdir(t *testing.T, parent, name string) string {
	t.Helper()
	dir := filepath.Join(parent, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestResolveScope(t *testing.T) {
	newEnv(t, "loom", "forge")

	// A cwd below the repo root walks up to the root's marker, but a nearer one
	// wins — while the walk still stops at the root, so a stray marker in the
	// checkout's parent is out of reach.
	nested := subdir(t, newCheckout(t, "loom\n"), "sub")
	nearest := writeMarker(t, subdir(t, newCheckout(t, "loom\n"), "sub"), "forge\n")
	// An unusable marker doesn't end the chain: the walk continues up, so the
	// repo root's declaration still wins.
	unusable := writeMarker(t, subdir(t, newCheckout(t, "loom\n"), "vendored"), "ghostwheel\n")
	stray := writeMarker(t, t.TempDir(), "forge\n")
	captured := subdir(t, stray, "repo")
	subdir(t, captured, ".git")
	symlinked := newCheckout(t, "")
	if err := os.Symlink(filepath.Join(newCheckout(t, "forge"), markerName),
		filepath.Join(symlinked, markerName)); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name       string
		cwd        string
		remote     string
		want       string
		wantSource string
		wantErr    bool
	}{
		{name: "https remote", remote: "https://github.com/EnderRealm/loom.git", want: "loom", wantSource: sourceRemote},
		{name: "ssh remote", remote: "git@github.com:EnderRealm/loom.git", want: "loom", wantSource: sourceRemote},
		{name: "absent remote", remote: "  ", wantErr: true},
		{name: "scope without a store directory", remote: "https://github.com/EnderRealm/ticket.git", wantErr: true},
		// git_remote is client-supplied: a traversing basename must not
		// resolve to the knowledge root or its parent, both real directories.
		{name: "remote traversing out of the store", remote: "https://github.com/EnderRealm/loom.git/..", wantErr: true},
		{name: "remote naming the store itself", remote: "https://github.com/EnderRealm/loom.git/.", wantErr: true},

		{name: "marker agreeing with the remote", cwd: newCheckout(t, "loom\n"), remote: loomRemote,
			want: "loom", wantSource: sourceMarker},
		{name: "marker disagreeing with the remote", cwd: newCheckout(t, "forge\n"), remote: loomRemote,
			want: "forge", wantSource: sourceMarker},
		{name: "marker with no remote at all", cwd: newCheckout(t, "loom\n"), remote: "",
			want: "loom", wantSource: sourceMarker},
		// Everything on this side is lowercase by construction, so a marker is
		// folded rather than rejected — unlike resolve_project.py.
		{name: "mixed-case marker", cwd: newCheckout(t, "# the project\n\nLoom\n"), remote: forgeRemote,
			want: "loom", wantSource: sourceMarker},
		{name: "cwd in a subdirectory of the checkout", cwd: nested, remote: forgeRemote,
			want: "loom", wantSource: sourceMarker},
		{name: "nearest marker wins", cwd: nearest, remote: loomRemote,
			want: "forge", wantSource: sourceMarker},
		{name: "unusable marker below a usable root marker", cwd: unusable, remote: forgeRemote,
			want: "loom", wantSource: sourceMarker},

		// Every marker failure falls back rather than failing: a session that
		// resolves via its remote today must keep resolving.
		{name: "cwd absent from this host", cwd: filepath.Join(t.TempDir(), "moved"), remote: loomRemote,
			want: "loom", wantSource: sourceRemote},
		{name: "no cwd recorded", cwd: "", remote: loomRemote, want: "loom", wantSource: sourceRemote},
		{name: "relative cwd", cwd: "code/loom", remote: loomRemote, want: "loom", wantSource: sourceRemote},
		{name: "cwd outside any checkout", cwd: t.TempDir(), remote: loomRemote, want: "loom", wantSource: sourceRemote},
		{name: "checkout without a marker", cwd: newCheckout(t, ""), remote: loomRemote, want: "loom", wantSource: sourceRemote},
		{name: "marker naming a scope the store lacks", cwd: newCheckout(t, "ghostwheel\n"), remote: loomRemote,
			want: "loom", wantSource: sourceRemote},
		{name: "marker holding only comments", cwd: newCheckout(t, "# forge\n\n"), remote: loomRemote,
			want: "loom", wantSource: sourceRemote},
		{name: "symlinked marker", cwd: symlinked, remote: loomRemote, want: "loom", wantSource: sourceRemote},
		{name: "marker above the repo root", cwd: captured, remote: loomRemote, want: "loom", wantSource: sourceRemote},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveScope(tc.cwd, tc.remote, logOnce{})
			if tc.wantErr {
				if err == nil {
					t.Fatalf("resolveScope(%q, %q) = %+v, want error", tc.cwd, tc.remote, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveScope(%q, %q): %v", tc.cwd, tc.remote, err)
			}
			if got.scope != tc.want || got.source != tc.wantSource {
				t.Fatalf("resolveScope(%q, %q) = %+v, want scope %q from %q",
					tc.cwd, tc.remote, got, tc.want, tc.wantSource)
			}
		})
	}
}

// errNoRemote is the backfill's sentinel for a session captured outside a git
// checkout, and it still has to mean that once a marker read comes first.
func TestResolveScopeKeepsTheNoRemoteSentinel(t *testing.T) {
	newEnv(t, "loom")

	if _, err := resolveScope(newCheckout(t, ""), "", logOnce{}); !errors.Is(err, errNoRemote) {
		t.Fatalf("resolveScope of a markerless checkout with no remote = %v, want %v", err, errNoRemote)
	}
}

// The marker is the canonical name, so it wins — but a disagreement means one
// of the two is stale, and only an operator can say which.
func TestSweepPrefersTheMarkerAndLogsTheDisagreement(t *testing.T) {
	e := newEnv(t, "loom", "forge")
	cwd := newCheckout(t, "forge\n")
	input := e.addSessionIn("s1", loomRemote, cwd)

	sweep(context.Background(), Options{})

	want := "forge " + input
	if len(e.runs) != 1 || e.runs[0] != want {
		t.Fatalf("runs = %v, want exactly [%q] — the marker names the scope", e.runs, want)
	}
	logs := e.logs.String()
	for _, w := range []string{
		"extract claude-code/s1 scope=forge source=marker",
		// The marker that won, not the cwd it was reached from: in a nested
		// checkout those are different directories.
		filepath.Join(cwd, markerName) + " names scope=forge, git remote " + loomRemote + " names scope=loom",
	} {
		if !strings.Contains(logs, w) {
			t.Fatalf("log missing %q; got:\n%s", w, logs)
		}
	}
}

// git_remote reaches the log through the disagreement line as well, on a path
// that never runs it past validScope — so it is the same forging channel a
// hostile session id is, and needs the same escaping.
func TestSweepEscapesHostileRemoteInTheDisagreementLog(t *testing.T) {
	e := newEnv(t, "loom")
	const injected = "\nbackfill done: extracted=99 failed=0 skipped=0 of 99 selected"
	e.addSessionIn("s1", "https://github.com/EnderRealm/forge"+injected, newCheckout(t, "loom\n"))

	sweep(context.Background(), Options{})

	// The disagreement, extract + ok, and the sweep summary.
	logs := e.logs.String()
	if got := strings.Count(logs, "\n"); got != 4 {
		t.Fatalf("log has %d lines, want 4 — one per statement:\n%q", got, logs)
	}
	if strings.ContainsFunc(strings.ReplaceAll(logs, "\n", ""), unicode.IsControl) {
		t.Fatalf("log carries a raw control character:\n%q", logs)
	}
	// Escaped and bounded: neither the remote nor the scope derived from it has
	// been through validScope on this path, so both carry the same bound a
	// rejected marker's value carries.
	hostile := "https://github.com/EnderRealm/forge" + injected
	derived := "forge" + injected
	for _, w := range []string{
		strconv.Quote(hostile[:scopeEchoLimit]) + "…",
		strconv.Quote(derived[:scopeEchoLimit]) + "…",
	} {
		if !strings.Contains(logs, w) {
			t.Fatalf("log missing %q; got:\n%q", w, logs)
		}
	}
	for _, w := range []string{hostile, derived} {
		if strings.Contains(logs, strconv.Quote(w)) {
			t.Fatalf("log carries the whole client-supplied remote %q:\n%q", w, logs)
		}
	}
}

// resolveScope takes the dedupe map as an ordinary parameter, so a caller can
// reasonably pass nil for "don't dedupe" — which must log, not panic.
func TestResolveScopeLogsWithoutADedupeMap(t *testing.T) {
	newEnv(t, "loom", "forge")

	if _, err := resolveScope(newCheckout(t, "forge\n"), loomRemote, nil); err != nil {
		t.Fatalf("resolveScope with a nil logOnce: %v", err)
	}
}

// A marker below the repo root is as likely a vendored subtree's declaration as
// the project's, which is the one signal resolve_project.py emits that this path
// would otherwise drop.
func TestSweepWarnsForAMarkerBelowTheRepoRoot(t *testing.T) {
	e := newEnv(t, "loom")
	root := newCheckout(t, "")
	sub := writeMarker(t, subdir(t, root, "vendored"), "loom\n")
	e.addSessionIn("s1", "", sub)

	sweep(context.Background(), Options{})

	want := filepath.Join(sub, markerName) + " is below the repo root " + root +
		" — using it, but the marker belongs at " + filepath.Join(root, markerName)
	if !strings.Contains(e.logs.String(), want) {
		t.Fatalf("log missing %q; got:\n%s", want, e.logs.String())
	}
}

// The load-bearing constraint: the marker is an additional source of truth,
// never a new way to fail.
func TestSweepStillResolvesCheckoutsWithoutAMarker(t *testing.T) {
	e := newEnv(t, "loom")
	bare := newCheckout(t, "")
	input := e.addSessionIn("s1", loomRemote, bare)
	// And a marker naming a scope the store has no directory for.
	other := e.addSessionIn("s2", loomRemote, newCheckout(t, "ghostwheel\n"))

	sweep(context.Background(), Options{})

	runs := append([]string{}, e.runs...)
	sort.Strings(runs)
	want := []string{"loom " + input, "loom " + other}
	sort.Strings(want)
	if !reflect.DeepEqual(runs, want) {
		t.Fatalf("runs = %v, want %v — both sessions resolve via their remote", e.runs, want)
	}
	logs := e.logs.String()
	if !strings.Contains(logs, `unusable (unknown scope "ghostwheel"`) {
		t.Fatalf("log missing the unusable-marker fallback:\n%s", logs)
	}
	// "No marker here" is most of every walk, and saying so per session would
	// bury the cases that matter.
	if strings.Contains(logs, filepath.Join(bare, markerName)) {
		t.Fatalf("log names a marker that was never there:\n%s", logs)
	}
}

// A marker that exists but can't be used is the answer to why a repo that has
// one still resolves through its remote, so each refusal says which it was.
func TestSweepLogsWhyAMarkerWasDeclined(t *testing.T) {
	cases := []struct {
		name  string
		plant func(t *testing.T, dir string)
		want  string
	}{
		{name: "symlink", want: "is a symlink", plant: func(t *testing.T, dir string) {
			if err := os.Symlink(filepath.Join(newCheckout(t, "forge"), markerName),
				filepath.Join(dir, markerName)); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "directory", want: "is not a regular file (d---------)", plant: func(t *testing.T, dir string) {
			subdir(t, dir, markerName)
		}},
		{name: "not utf-8", want: "is not UTF-8", plant: func(t *testing.T, dir string) {
			if err := os.WriteFile(filepath.Join(dir, markerName), []byte{'l', 0xff, 'm'}, 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "no value", want: "holds no project name", plant: func(t *testing.T, dir string) {
			writeMarker(t, dir, "# the project name goes here\n\n")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, "loom")
			cwd := newCheckout(t, "")
			tc.plant(t, cwd)
			input := e.addSessionIn("s1", loomRemote, cwd)

			sweep(context.Background(), Options{})

			if want := "loom " + input; len(e.runs) != 1 || e.runs[0] != want {
				t.Fatalf("runs = %v, want exactly [%q] — a declined marker falls back", e.runs, want)
			}
			want := filepath.Join(cwd, markerName) + " " + tc.want + " — ignoring it"
			if !strings.Contains(e.logs.String(), want) {
				t.Fatalf("log missing %q; got:\n%s", want, e.logs.String())
			}
		})
	}
}

// The marker path is client-steerable — cwd_raw is shipper-supplied, so a
// session can point the walk at any directory on this host — and what it finds
// there is read into memory and echoed into the audit log.
func TestSweepIgnoresAnOversizedMarker(t *testing.T) {
	e := newEnv(t, "loom")
	cwd := newCheckout(t, strings.Repeat("a", maxMarkerBytes+1))
	input := e.addSessionIn("s1", loomRemote, cwd)

	sweep(context.Background(), Options{})

	if want := "loom " + input; len(e.runs) != 1 || e.runs[0] != want {
		t.Fatalf("runs = %v, want exactly [%q] — an oversized marker falls back", e.runs, want)
	}
	logs := e.logs.String()
	want := fmt.Sprintf("%s is larger than %d bytes", filepath.Join(cwd, markerName), maxMarkerBytes)
	if !strings.Contains(logs, want) {
		t.Fatalf("log missing %q; got:\n%s", want, logs)
	}
	if strings.Contains(logs, strings.Repeat("a", scopeEchoLimit+1)) {
		t.Fatalf("log carries the marker's contents:\n%s", logs)
	}
}

// A rejected value is arbitrary file content and the line it lands in is the
// audit record, so the echo is bounded the way resolve_project.py bounds its own.
func TestSweepBoundsWhatARejectedMarkerEchoes(t *testing.T) {
	cases := []struct {
		name  string
		value string
	}{
		{name: "ascii", value: "/" + strings.Repeat("x", 500)},
		// Cut by character, as resolve_project.py cuts its own echo: a byte cut
		// would split a rune and end the line in \xNN escapes.
		{name: "multi-byte", value: "/" + strings.Repeat("é", 500)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, "loom")
			cwd := newCheckout(t, tc.value)
			input := e.addSessionIn("s1", loomRemote, cwd)

			sweep(context.Background(), Options{})

			if want := "loom " + input; len(e.runs) != 1 || e.runs[0] != want {
				t.Fatalf("runs = %v, want exactly [%q] — an unusable marker falls back", e.runs, want)
			}
			logs := e.logs.String()
			want := "unsafe scope " + strconv.Quote(string([]rune(tc.value)[:scopeEchoLimit])) + "…"
			if !strings.Contains(logs, want) {
				t.Fatalf("log missing %q; got:\n%s", want, logs)
			}
			if strings.Contains(logs, tc.value) {
				t.Fatalf("log carries the whole rejected value:\n%s", logs)
			}
		})
	}
}
