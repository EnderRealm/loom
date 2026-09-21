package extract

import (
	"context"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// Two repos sharing a basename derive one scope, and the merged directory
// says nothing about it afterwards. The sweep still files both where they
// resolve — detection changes no filing decision — but the ledger keeps the
// remote each was filed under, and the log names the scope with both.
func TestSweepReportsAScopeFiledUnderTwoRemotes(t *testing.T) {
	e := newEnv(t, "tools")
	a := e.addSession("a", "https://github.com/a/tools.git")
	b := e.addSession("b", "git@github.com:b/tools.git")

	sweep(context.Background(), Options{})

	want := []string{"tools " + a, "tools " + b}
	sort.Strings(e.runs)
	if !reflect.DeepEqual(e.runs, want) {
		t.Fatalf("runs = %v, want %v (both still extract into the scope they resolve to)", e.runs, want)
	}
	st, err := loadState()
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	for id, remote := range map[string]string{"a": "github.com/a/tools", "b": "github.com/b/tools"} {
		rec := st.Sessions[sessionKey("claude-code", id)]
		if rec.Scope != "tools" || rec.Remote != remote {
			t.Fatalf("record %s = %+v, want scope tools filed under %s", id, rec, remote)
		}
	}
	logs := e.logs.String()
	line := "scope tools is filed under 2 remotes: github.com/a/tools, github.com/b/tools"
	if strings.Count(logs, line) != 1 {
		t.Fatalf("log has %d copies of %q, want exactly one:\n%s", strings.Count(logs, line), line, logs)
	}
}

// The failed mark carries the remote too: a session the extractor choked on
// was still filed under that scope, and the check reads the ledger.
func TestSweepRecordsTheRemoteOnAFailedRun(t *testing.T) {
	e := newEnv(t, "loom")
	e.addSession("s1", loomRemote)
	orig := runExtractor
	runExtractor = func(context.Context, string, string, string, string, string) (extractRun, error) {
		return extractRun{}, errExtractorFailed
	}
	t.Cleanup(func() { runExtractor = orig })

	sweep(context.Background(), Options{})

	st, err := loadState()
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	rec := st.Sessions[sessionKey("claude-code", "s1")]
	if rec.Outcome != outcomeFailed || rec.Remote != "github.com/enderrealm/loom" {
		t.Fatalf("record = %+v, want the failed outcome filed under github.com/enderrealm/loom", rec)
	}
}

// One repo cloned over ssh on one machine and https on another is one remote
// once normalized, so the scope holds a single remote and nothing is reported.
func TestSweepDoesNotReportOneRepoReachedTwoWays(t *testing.T) {
	e := newEnv(t, "loom")
	e.addSession("ssh", "git@github.com:enderrealm/loom.git")
	e.addSession("https", "https://github.com/enderrealm/loom")

	sweep(context.Background(), Options{})

	if len(e.runs) != 2 {
		t.Fatalf("runs = %v, want both sessions extracted", e.runs)
	}
	st, err := loadState()
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if got := st.remotes()["loom"]; len(got) != 1 || !got["github.com/enderrealm/loom"] {
		t.Fatalf("remotes for loom = %v, want the one normalized key", got)
	}
	if logs := e.logs.String(); strings.Contains(logs, "is filed under") {
		t.Fatalf("log reports a collision for one repo:\n%s", logs)
	}
}

// A marker resolution records no remote: the marker is the project's own
// declaration, so it cannot have merged two repos, and a session resolved
// through it neither joins a scope's set nor triggers the line.
func TestSweepRecordsNoRemoteForAMarkerResolution(t *testing.T) {
	e := newEnv(t, "loom")
	e.addSessionIn("marked", forgeRemote, newCheckout(t, "loom\n"))
	e.addSession("remote", loomRemote)

	sweep(context.Background(), Options{})

	if len(e.runs) != 2 {
		t.Fatalf("runs = %v, want both sessions extracted into loom", e.runs)
	}
	st, err := loadState()
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if rec := st.Sessions[sessionKey("claude-code", "marked")]; rec.Scope != "loom" || rec.Remote != "" {
		t.Fatalf("record = %+v, want scope loom with no remote recorded", rec)
	}
	if got := st.remotes()["loom"]; len(got) != 1 {
		t.Fatalf("remotes for loom = %v, want only the remote-derived session's", got)
	}
	if logs := e.logs.String(); strings.Contains(logs, "is filed under") {
		t.Fatalf("log reports a collision where a marker named the scope:\n%s", logs)
	}
}

// The remotes reach the line through the same bound and quoting the
// resolver's disagreement line applies: neither has been through validScope.
func TestSweepBoundsTheRemotesACollisionEchoes(t *testing.T) {
	e := newEnv(t, "tools")
	long := "https://github.com/" + strings.Repeat("x", scopeEchoLimit) + "/tools"
	e.addSession("a", "https://github.com/a/tools")
	e.addSession("b", long)

	sweep(context.Background(), Options{})

	logs := e.logs.String()
	bounded := string([]rune("github.com/" + strings.Repeat("x", scopeEchoLimit) + "/tools")[:scopeEchoLimit]) + "…"
	if !strings.Contains(logs, "scope tools is filed under 2 remotes: github.com/a/tools, "+bounded) {
		t.Fatalf("log missing the bounded remote %q:\n%s", bounded, logs)
	}
}

// A remote can carry a token (https://u:secret@host/o/r) and NormalizeRemote
// keeps it, so the key the ledger stores strips it and the line never shows it.
func TestSweepNeverEchoesACredentialBearingRemote(t *testing.T) {
	e := newEnv(t, "tools")
	e.addSession("a", "https://u:secret@github.com/a/tools.git")
	e.addSession("b", "https://github.com/b/tools")
	// An unescaped "@" in the password: the key strips through the last "@"
	// in the authority, not the first, so no fragment of it is stored.
	e.addSession("c", "https://u:pass@still@secret@github.com/c/tools.git")

	sweep(context.Background(), Options{})

	st, err := loadState()
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if rec := st.Sessions[sessionKey("claude-code", "a")]; rec.Remote != "github.com/a/tools" {
		t.Fatalf("record = %+v, want the remote stored without its userinfo", rec)
	}
	if rec := st.Sessions[sessionKey("claude-code", "c")]; rec.Remote != "github.com/c/tools" {
		t.Fatalf("record = %+v, want the remote stored without its userinfo", rec)
	}
	logs := e.logs.String()
	for _, fragment := range []string{"secret", "pass", "still"} {
		if strings.Contains(logs, fragment) {
			t.Fatalf("log echoes the credential fragment %q:\n%s", fragment, logs)
		}
	}
	if !strings.Contains(logs, "scope tools is filed under 3 remotes: github.com/a/tools, github.com/b/tools, github.com/c/tools") {
		t.Fatalf("log missing the redacted collision line:\n%s", logs)
	}
}

// NormalizeRemote strips only the http, https and ssh schemes, so a remote
// under any other scheme reaches the key with its scheme — and its userinfo —
// intact. The key drops the userinfo behind the scheme and the line never
// shows it.
func TestSweepNeverEchoesACredentialBearingFTPRemote(t *testing.T) {
	e := newEnv(t, "tools")
	e.addSession("a", "ftp://u:secret@host/a/tools.git")
	e.addSession("b", "https://github.com/b/tools")

	sweep(context.Background(), Options{})

	st, err := loadState()
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if rec := st.Sessions[sessionKey("claude-code", "a")]; rec.Remote != "ftp://host/a/tools" {
		t.Fatalf("record = %+v, want the remote stored without its userinfo", rec)
	}
	logs := e.logs.String()
	if strings.Contains(logs, "secret") {
		t.Fatalf("log echoes the credential:\n%s", logs)
	}
	if !strings.Contains(logs, "scope tools is filed under 2 remotes: ftp://host/a/tools, github.com/b/tools") {
		t.Fatalf("log missing the redacted collision line:\n%s", logs)
	}
}

// A ledger written before the key stripped userinfo is rendered redacted too:
// Remotes stays the stored value, and EchoRemotes is what reaches a line.
func TestEchoRemotesRedactsAndBoundsStoredValues(t *testing.T) {
	long := strings.Repeat("x", scopeEchoLimit) + "/tools"
	c := ScopeCollision{Name: "tools", Remotes: []string{
		"u:secret@github.com/a/tools",
		"ftp://u:secret@host/a/tools",
		"u:pass@still@secret@github.com/c/tools",
		"ftp://u:pass@secret@host/c@d/tools",
		"github.com/" + long,
		"github.com/b/tools\n",
	}}
	want := "github.com/a/tools, ftp://host/a/tools, github.com/c/tools, ftp://host/c@d/tools, " + string([]rune("github.com/" + long)[:scopeEchoLimit]) + "…, \"github.com/b/tools\\n\""
	got := c.EchoRemotes()
	if got != want {
		t.Fatalf("EchoRemotes = %q, want %q", got, want)
	}
	for _, fragment := range []string{"secret", "pass", "still"} {
		if strings.Contains(got, fragment) {
			t.Fatalf("EchoRemotes = %q echoes the credential fragment %q", got, fragment)
		}
	}
}

func TestRemoteKeyStripsUserinfoAndFoldsClones(t *testing.T) {
	for in, want := range map[string]string{
		"https://u:secret@github.com/a/tools.git":      "github.com/a/tools",
		"https://token@github.com/a/tools":             "github.com/a/tools",
		"ssh://deploy@host/a/tools":                    "host/a/tools",
		"git@github.com:a/tools.git":                   "github.com/a/tools",
		"https://github.com/a/tools":                   "github.com/a/tools",
		"ftp://u:secret@host/a/tools.git":              "ftp://host/a/tools",
		"ftps://token@host/a/tools":                    "ftps://host/a/tools",
		"https://u:pass@secret@github.com/a/tools.git": "github.com/a/tools",
		"https://u:pass@still@secret@host/a/tools":     "host/a/tools",
		"ftp://u:pass@secret@host/a/tools.git":         "ftp://host/a/tools",
		"https://u:p@ss@host/a@b/tools":                "host/a@b/tools",
		"deploy@host:a/tools":                          "host:a/tools",
		"ftp://host/a@b/tools":                         "ftp://host/a@b/tools",
		"/var/folders/x/weft-1/origin":                 "/var/folders/x/weft-1/origin",
		"/var/user@home/origin":                        "/var/user@home/origin",
	} {
		if got := remoteKey(in); got != want {
			t.Errorf("remoteKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRemoteSetsCollisions(t *testing.T) {
	sets := remoteSets{}
	sets.add("tools", "github.com/b/tools")
	sets.add("tools", "github.com/a/tools")
	sets.add("tools", "github.com/a/tools")
	sets.add("loom", "github.com/enderrealm/loom")
	sets.add("origin", "/var/b/origin")
	sets.add("origin", "/var/a/origin")

	want := []ScopeCollision{
		{Name: "origin", Remotes: []string{"/var/a/origin", "/var/b/origin"}},
		{Name: "tools", Remotes: []string{"github.com/a/tools", "github.com/b/tools"}},
	}
	if got := sets.collisions(); !reflect.DeepEqual(got, want) {
		t.Fatalf("collisions = %+v, want %+v", got, want)
	}
}
