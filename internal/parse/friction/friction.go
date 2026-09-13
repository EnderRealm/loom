// Package friction names the kinds of harness friction a parser emits — the
// hook asks and denies, refused permissions, tool errors and user interrupts
// a session's transcript carried — and normalizes a raw message to the
// signature those events are grouped by. It is a leaf under the parsers, the
// way parse/lens is; the ranked view over the stored rows is
// internal/friction.
package friction

import (
	"regexp"
	"strings"
)

// Kinds of friction event, exactly these six. bash.repeat is deliberately
// absent: it measured 2,073 events dominated by `echo idle` and `git status`
// polling and carries no signal.
const (
	KindHookAsk          = "hook.ask"
	KindHookDeny         = "hook.deny"
	KindDeniedByUser     = "permission.denied_by_user"
	KindClassifierDenied = "permission.classifier_denied"
	KindToolError        = "tool.error"
	KindUserInterrupt    = "user.interrupt"
)

// Kinds lists every kind in declaration order.
var Kinds = []string{
	KindHookAsk,
	KindHookDeny,
	KindDeniedByUser,
	KindClassifierDenied,
	KindToolError,
	KindUserInterrupt,
}

// signatureLimit caps a normalized signature.
const signatureLimit = 300

var (
	uuidRe = regexp.MustCompile(`(?i)[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
	pidRe  = regexp.MustCompile(`(?i)\bpid[ =:]*\d+`)
	// A path is a `/` or `~/` that opens a token: one embedded in a word
	// (`input/output`) is a separator, not a path.
	pathRe   = regexp.MustCompile(`(^|[^A-Za-z0-9_.~-])(~?/[^\s"')]+)`)
	digitsRe = regexp.MustCompile(`\d{3,}`)
	spaceRe  = regexp.MustCompile(`\s+`)
)

// Normalize reduces a raw message to the signature it is grouped by: its
// first line, with uuids, pids, absolute paths and runs of three or more
// digits replaced by placeholders. Order matters — a uuid and a path both
// contain digit runs, so each is replaced before the digit pass sees it.
func Normalize(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	s = uuidRe.ReplaceAllString(s, "<uuid>")
	s = pidRe.ReplaceAllString(s, "pid <pid>")
	s = pathRe.ReplaceAllString(s, "${1}<path>")
	s = digitsRe.ReplaceAllString(s, "<n>")
	s = strings.TrimSpace(spaceRe.ReplaceAllString(s, " "))
	if len(s) > signatureLimit {
		s = s[:signatureLimit]
	}
	return s
}
