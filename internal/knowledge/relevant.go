package knowledge

import (
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// TicketRef is the subset of a ticket the ranker reads. Body holds the
// description plus acceptance text (everything tk parses after the title).
type TicketRef struct {
	ID      string // namespaced "project/id"
	BareID  string
	Parent  string // namespaced or bare; "" when none
	Project string
	Title   string
	Body    string
}

// Match is one ranked artifact with its score and the strongest matched
// signal label ("citation" | "file-overlap" | "keyword").
type Match struct {
	Artifact Artifact
	Score    int
	Signal   string

	// tier is the strongest matched signal's precedence band, used as the
	// primary sort key so signal precedence beats raw score.
	tier int
}

// signal tiers, strongest first. Ordering is tiered so a citation always
// outranks any file-overlap match, which always outranks any keyword match,
// regardless of composite score.
const (
	tierKeyword     = 0
	tierFileOverlap = 1
	tierCitation    = 2
)

const (
	scoreCitation     = 100
	scorePerEvidence  = 10
	scorePerKeyword   = 1
	minKeywordMatches = 2 // a single shared word is noise
)

// stopWords are common English glue plus generic domain words that carry no
// discriminating signal in this corpus.
var stopWords = map[string]bool{
	"the": true, "and": true, "for": true, "with": true, "this": true,
	"that": true, "not": true, "are": true, "from": true, "when": true,
	"ticket": true, "file": true, "files": true, "via": true,
	"into": true, "its": true, "was": true, "were": true, "has": true,
	"have": true, "had": true, "will": true, "can": true, "all": true,
	"any": true, "but": true, "out": true, "use": true,
}

// keywordToken matches a run of lowercase alphanumerics for keyword overlap.
var keywordToken = regexp.MustCompile(`[a-z0-9]+`)

// pathLike matches a candidate path-like token in ticket text: either it
// contains a slash, or it looks like a <name>.<ext> filename.
var pathLike = regexp.MustCompile(`[A-Za-z0-9_./\-]+`)
var fileNameLike = regexp.MustCompile(`^[A-Za-z0-9_\-]+\.[A-Za-z0-9_]+$`)

// Rank scores validated artifacts against the ticket and returns matches
// ordered by signal tier (citation > file-overlap > keyword), then composite
// score, then scope match, then artifact ID. Zero-signal artifacts are
// excluded — scope alone never includes an artifact.
func Rank(ref TicketRef, arts []Artifact) []Match {
	idTokens := citationIDs(ref)
	ticketText := ref.Title + "\n" + ref.Body
	wantPaths := pathTokens(ticketText)
	wantWords := keywordSet(ticketText)

	var out []Match
	for _, a := range arts {
		if a.Status != "validated" {
			continue
		}

		cited := false
		body := a.Body
		for _, id := range idTokens {
			if strings.Contains(body, id) {
				cited = true
				break
			}
		}

		evMatches := evidenceOverlap(wantPaths, a.EvidencePaths)
		kwMatches := keywordOverlap(wantWords, a)

		score := 0
		tier := tierKeyword
		signal := ""
		if len(kwMatches) >= minKeywordMatches {
			score += len(kwMatches) * scorePerKeyword
			signal = "keyword"
		}
		if evMatches > 0 {
			score += evMatches * scorePerEvidence
			tier = tierFileOverlap
			signal = "file-overlap"
		}
		if cited {
			score += scoreCitation
			tier = tierCitation
			signal = "citation"
		}

		if signal == "" {
			continue
		}
		out = append(out, Match{Artifact: a, Score: score, Signal: signal, tier: tier})
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].tier != out[j].tier {
			return out[i].tier > out[j].tier
		}
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		si, sj := scopeMatch(out[i].Artifact.Scope, ref.Project), scopeMatch(out[j].Artifact.Scope, ref.Project)
		if si != sj {
			return si
		}
		return out[i].Artifact.ID < out[j].Artifact.ID
	})
	return out
}

// citationIDs returns the ID variants a citation may contain: the ticket's
// namespaced and bare IDs, and (when set) the parent's namespaced and bare
// IDs.
func citationIDs(ref TicketRef) []string {
	var ids []string
	add := func(s string) {
		if s == "" || slices.Contains(ids, s) {
			return
		}
		ids = append(ids, s)
	}
	add(ref.ID)
	add(ref.BareID)
	if ref.Parent != "" {
		add(ref.Parent)
		_, bare := splitNamespaced(ref.Parent)
		add(bare)
	}
	return ids
}

func splitNamespaced(id string) (project, bare string) {
	if p, b, ok := strings.Cut(id, "/"); ok {
		return p, b
	}
	return "", id
}

// pathTokens extracts path-like tokens from ticket text: tokens containing a
// slash or matching a <name>.<ext> filename, stripped of backticks and
// surrounding punctuation.
func pathTokens(text string) []string {
	text = strings.ReplaceAll(text, "`", " ")
	seen := map[string]bool{}
	var out []string
	for _, tok := range pathLike.FindAllString(text, -1) {
		tok = strings.Trim(tok, "./-")
		if tok == "" {
			continue
		}
		if strings.Contains(tok, "/") || fileNameLike.MatchString(tok) {
			if !seen[tok] {
				seen[tok] = true
				out = append(out, tok)
			}
		}
	}
	return out
}

// evidenceOverlap counts evidence paths that match any ticket path token.
// A match is an equal string, a path-component suffix in either direction,
// or an equal basename.
func evidenceOverlap(wantPaths, evidence []string) int {
	count := 0
	for _, ev := range evidence {
		ev = strings.Trim(ev, "`")
		for _, want := range wantPaths {
			if pathMatch(want, ev) {
				count++
				break
			}
		}
	}
	return count
}

func pathMatch(a, b string) bool {
	a = path.Clean(a)
	b = path.Clean(b)
	if a == b {
		return true
	}
	if path.Base(a) == path.Base(b) {
		return true
	}
	return suffixComponents(a, b) || suffixComponents(b, a)
}

// suffixComponents reports whether short is a trailing path-component slice
// of long (e.g. "cmd/loom/main.go" is a suffix of "loom/cmd/loom/main.go").
func suffixComponents(long, short string) bool {
	lp := strings.Split(long, "/")
	sp := strings.Split(short, "/")
	if len(sp) == 0 || len(sp) > len(lp) {
		return false
	}
	off := len(lp) - len(sp)
	for i := range sp {
		if lp[off+i] != sp[i] {
			return false
		}
	}
	return true
}

// keywordSet returns distinct lowercase alphanumeric tokens (≥3 chars, minus
// stop words) from text.
func keywordSet(text string) map[string]bool {
	out := map[string]bool{}
	for _, tok := range keywordToken.FindAllString(strings.ToLower(text), -1) {
		if len(tok) < 3 || stopWords[tok] {
			continue
		}
		out[tok] = true
	}
	return out
}

// keywordOverlap returns the distinct ticket keywords also present in the
// artifact's title + claim.
func keywordOverlap(wantWords map[string]bool, a Artifact) []string {
	artWords := keywordSet(a.Title + "\n" + a.Claim)
	var shared []string
	for w := range wantWords {
		if artWords[w] {
			shared = append(shared, w)
		}
	}
	return shared
}

// scopeMatch reports whether an artifact's scope ranks as a tie-break match:
// the ticket's project scope, or the cross-project "universal" scope.
func scopeMatch(scope, project string) bool {
	return scope == "universal" || (project != "" && scope == project)
}
