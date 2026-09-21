package extract

import (
	"sort"
	"strings"
)

// ScopeCollision is a scope more than one distinct git remote resolves to. The
// scope is a remote's basename, so github.com/a/tools and github.com/b/tools
// both derive tools and file their truths into one namespace; nothing about
// the merged directory says so afterwards, which is why the remotes are kept.
// Detection only — the resolution is a .loom-project marker in one of the
// repos (docs/knowledge-scopes.md, "Collisions").
type ScopeCollision struct {
	Name string
	// Remotes are the stored keys as recorded, sorted. Print them through
	// EchoRemotes rather than directly: they are client-supplied and unrendered.
	Remotes []string
}

// EchoRemotes renders the remotes for a log line or `loom status`, joined for
// one line. Each is redacted through stripUserinfo — a ledger written before
// remoteKey stripped userinfo may still carry a credential — and then bounded
// and quoted by echoRemote, since a remote never went through validScope and
// carries whatever the resolver's own disagreement line guards against.
func (c ScopeCollision) EchoRemotes() string {
	out := make([]string, 0, len(c.Remotes))
	for _, remote := range c.Remotes {
		out = append(out, echoRemote(stripUserinfo(remote)))
	}
	return strings.Join(out, ", ")
}

// remoteSets is the distinct remote keys seen per scope.
type remoteSets map[string]map[string]bool

func (r remoteSets) add(scope, remote string) {
	if r[scope] == nil {
		r[scope] = map[string]bool{}
	}
	r[scope][remote] = true
}

// collisions lists every scope holding more than one remote, sorted by name
// with its remotes sorted, so the report reads the same on every run.
func (r remoteSets) collisions() []ScopeCollision {
	var out []ScopeCollision
	for scope, remotes := range r {
		if len(remotes) < 2 {
			continue
		}
		c := ScopeCollision{Name: scope}
		for remote := range remotes {
			c.Remotes = append(c.Remotes, remote)
		}
		sort.Strings(c.Remotes)
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// reportCollision states, once per run, that the scope a session was just filed
// under is shared by more than one remote. Read from the ledger after the mark
// committed, so the other writer's records count too. The first remote for a
// scope is recorded and not reported: one remote is the ordinary case.
func reportCollision(st *state, scope string, seen logger) {
	for _, c := range st.remotes().collisions() {
		if c.Name == scope {
			seen.printf("scope %s is filed under %d remotes: %s — two repos share a basename; give one a %s marker",
				scope, len(c.Remotes), c.EchoRemotes(), markerName)
		}
	}
}
