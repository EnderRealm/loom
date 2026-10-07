package summaries

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	"loom/internal/parse/summary"
)

// commitRecord is one git commit derived from a session's shell tool output.
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

// gitCommitCmdRe matches a command that runs git commit, allowing global
// options before the subcommand: the argument-taking "-C <dir>" and
// "-c k=v", and long options like "--git-dir=<dir>", "--work-tree=<dir>" and
// "--no-pager". "commit" must end the token so plumbing like
// "git commit-tree" (which prints a bare hash) doesn't qualify.
var gitCommitCmdRe = regexp.MustCompile(`\bgit(?:\s+(?:-[Cc]\s+\S+|--\S+))*\s+commit(?:\s|$)`)

// onelineCommitRe matches a "<hash> <subject>" line as printed by
// "git log --oneline -1" or "git log -1 --format='%h %s'". A push range
// ("4a15f51..0ae1262  master -> master") or a diff index line
// ("index 6ada054..d173914 100644") can't match: the hash must open the line
// and be followed directly by a single space.
var onelineCommitRe = regexp.MustCompile(`^([0-9a-f]{7,40}) (.+)$`)

// truncationMark ends a key argument or result summary the parsers cut to
// their length limit (the truncate helpers in claudeparse, codexparse and
// cursorparse).
const truncationMark = "…"

// minCutSubjectPrefix is how much of a subject a cut key argument must still
// hold for the command to count as having authored it — enough that a
// coincidental match elsewhere in the command is implausible.
const minCutSubjectPrefix = 20

// extractCommits derives commit records from a session's tool calls, from
// shell results only: bash calls, and the custom calls through which a Codex
// code-mode cell runs commands (their results decoded to the commands' own
// output by codexparse, their key argument the cell's source). Other output
// produces nothing.
//
// A plain git commit prints a "[branch hash] subject" line only when it
// succeeds. Git hooks can print preamble, so every line is scanned, and a
// single call may commit more than once, so every matching line yields a
// record.
//
// "git commit -q" suppresses that line, so a commit made quietly and then
// echoed with "git log --oneline -1" (how /work commits) leaves only a
// "<hash> <subject>" line. For a call with no bracket line whose command runs
// git commit, the first such line is taken as the commit — the first only, so
// a multi-line log doesn't record older commits. Branch is unknown there, and
// no stat line is sought: -q prints none, so one found later belongs to other
// output. Leading whitespace is kept on this path so an indented line (a diff
// context line, say) can't pose as the commit.
//
// That line alone doesn't show the commit succeeded: after a failed commit
// (nothing to commit, a hook rejection, an empty message) the echo prints the
// HEAD that was already there, and the call's exit status is the echo's. So
// the line is recorded only when its subject is one the command itself wrote
// (see commandAuthored), which keeps a failed commit from recording an older,
// differently-subjected HEAD and joining the session to that commit's ticket.
// It does not catch a failed retry of a subject that already landed: that
// records the same commit again, under the same ticket. Quiet commits are
// lost when the command was cut before its subject begins, or when the
// subject comes from a substitution such as
// --amend -m "$(git log -1 --format=%B)" (the commit being amended was
// already recorded when it was made).
func extractCommits(calls []summary.ToolCall) []commitRecord {
	var recs []commitRecord
	for _, tc := range calls {
		if tc.Kind != summary.KindBash && tc.Kind != summary.KindCustom {
			continue
		}
		lines := strings.Split(tc.ResultSummary, "\n")
		found := false
		for i, raw := range lines {
			m := commitLineRe.FindStringSubmatch(strings.TrimSpace(raw))
			if m == nil {
				continue
			}
			found = true
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
		if found || !gitCommitCmdRe.MatchString(tc.KeyArg) {
			continue
		}
		for _, raw := range lines {
			m := onelineCommitRe.FindStringSubmatch(strings.TrimRight(raw, " \t\r"))
			if m == nil {
				continue
			}
			if commandAuthored(tc.KeyArg, m[2]) {
				recs = append(recs, commitRecord{
					committedAt: tc.StartedAt,
					commitHash:  m[1],
					subject:     m[2],
				})
			}
			break
		}
	}
	return recs
}

// commandAuthored reports whether a commit subject appears verbatim in the
// command that made the commit. Both sides may be cut by the parsers' length
// limits and end in truncationMark: a subject cut by the result limit is
// compared without the mark, and a command cut mid-subject counts when the
// text from the subject's opening to the cut is a prefix of the subject at
// least minCutSubjectPrefix bytes long.
func commandAuthored(cmd, subject string) bool {
	subject = strings.TrimSuffix(subject, truncationMark)
	if subject == "" {
		return false
	}
	if strings.Contains(cmd, subject) {
		return true
	}
	cmd, cut := strings.CutSuffix(cmd, truncationMark)
	if !cut || len(subject) < minCutSubjectPrefix {
		return false
	}
	// The cut is at the command's end, so the subject's last opening is the
	// one it ran into.
	i := strings.LastIndex(cmd, subject[:minCutSubjectPrefix])
	return i >= 0 && strings.HasPrefix(subject, cmd[i:])
}
