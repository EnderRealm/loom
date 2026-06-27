package summaries

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	"loom/internal/parse/summary"
)

// commitRecord is one git commit derived from a session's bash tool output.
// filesChanged is nil when git's summary stat line wasn't captured.
type commitRecord struct {
	committedAt  time.Time
	commitHash   string
	branch       string
	subject      string
	filesChanged *int
}

// commitLineRe matches git's commit confirmation line. The hash is anchored
// on the closing bracket as the whitespace-delimited token immediately before
// it (hex, 7–40 chars); the branch/description is everything before that token
// and the subject is the remainder. Matches "[main 2bbeb99] Release v1.2.2",
// "[main (root-commit) abc1234] Initial commit", "[detached HEAD abc1234] x".
var commitLineRe = regexp.MustCompile(`^\[(.+) ([0-9a-f]{7,40})\] (.+)$`)

// filesChangedRe pulls the file count from git's summary stat line, e.g.
// " 3 files changed, 12 insertions(+)" or " 1 file changed, 2 insertions(+)".
var filesChangedRe = regexp.MustCompile(`(\d+) files? changed`)

// extractCommits derives commit records from a session's tool calls. Only
// successful git commits leave a "[branch hash] subject" line in a bash
// result; failed commits and non-bash output produce nothing. Git hooks can
// print preamble, so every line is scanned, and a single bash call may commit
// more than once, so every matching line yields a record.
func extractCommits(calls []summary.ToolCall) []commitRecord {
	var recs []commitRecord
	for _, tc := range calls {
		if tc.Kind != summary.KindBash {
			continue
		}
		lines := strings.Split(tc.ResultSummary, "\n")
		for i, raw := range lines {
			m := commitLineRe.FindStringSubmatch(strings.TrimSpace(raw))
			if m == nil {
				continue
			}
			rec := commitRecord{
				committedAt: tc.StartedAt,
				branch:      m[1],
				commitHash:  m[2],
				subject:     m[3],
			}
			// Look ahead for git's stat line, stopping at the next commit so a
			// commit without stats doesn't borrow the following one's count.
			for _, fl := range lines[i+1:] {
				if commitLineRe.MatchString(strings.TrimSpace(fl)) {
					break
				}
				if fm := filesChangedRe.FindStringSubmatch(fl); fm != nil {
					if n, err := strconv.Atoi(fm[1]); err == nil {
						rec.filesChanged = &n
					}
					break
				}
			}
			recs = append(recs, rec)
		}
	}
	return recs
}
