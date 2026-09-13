package claudeparse

import (
	"encoding/json"
	"strings"
	"time"

	"loom/internal/parse/friction"
	"loom/internal/parse/summary"
)

// Texts the harness writes into a transcript when a tool use is refused or
// a turn is cut short. Matched by prefix: the rejection form varies by CLI
// version and the interrupt form by whether a tool use was pending.
const (
	classifierDeniedMarker = "denied by the Claude Code auto mode classifier"
	classifierReasonMarker = "Reason:"
	userDeclinedPrefix     = "The user doesn't want to proceed with this tool use"
	userRejectedPrefix     = "User rejected tool use"
	interruptPrefix        = "[Request interrupted by user"
)

func (st *state) addFriction(kind, signature, tool, detail string, turnIdx int, ts time.Time) {
	st.s.Friction = append(st.s.Friction, summary.FrictionEvent{
		TurnIdx:   turnIdx,
		Time:      ts,
		Kind:      kind,
		Signature: friction.Normalize(signature),
		Tool:      tool,
		Detail:    truncate(firstLine(detail), resultTextLimit),
	})
}

// recordHookFriction emits a hook.ask or hook.deny for a hook that asked or
// refused. An allow, a stdout that is not a decision, and a clean exit with
// no decision are the hook doing nothing worth counting.
func (st *state) recordHookFriction(p hookSuccessPayload, ts time.Time) {
	var d hookDecision
	// Non-JSON stdout leaves d empty, which is the "no decision" case.
	_ = json.Unmarshal([]byte(p.Stdout), &d)

	var kind, reason string
	switch {
	case d.HookSpecificOutput.PermissionDecision == "ask":
		kind = friction.KindHookAsk
		reason = d.HookSpecificOutput.PermissionDecisionReason
	case d.HookSpecificOutput.PermissionDecision == "deny", d.Decision == "block", p.ExitCode == 2:
		kind = friction.KindHookDeny
		reason = d.HookSpecificOutput.PermissionDecisionReason
		if reason == "" {
			reason = d.Reason
		}
		if reason == "" {
			reason = firstLine(p.Stderr)
		}
	default:
		return
	}
	_, tool, _ := strings.Cut(p.HookName, ":")
	st.addFriction(kind, p.HookName+": "+reason, tool, reason, st.currentTurnIdx, ts)
}

// recordToolResultFriction classifies an is_error tool result: the auto-mode
// classifier's refusal, the user's, or the tool's own failure.
func (st *state) recordToolResultFriction(tc *summary.ToolCall, content string, ts time.Time) {
	trimmed := strings.TrimSpace(content)
	switch {
	case strings.Contains(content, classifierDeniedMarker):
		signature := content
		if _, after, ok := strings.Cut(content, classifierReasonMarker); ok {
			signature = strings.TrimSpace(after)
		}
		st.addFriction(friction.KindClassifierDenied, signature, tc.ToolName, signature, tc.TurnIdx, ts)
	case strings.HasPrefix(trimmed, userDeclinedPrefix), strings.HasPrefix(trimmed, userRejectedPrefix):
		st.addFriction(friction.KindDeniedByUser, tc.ToolName, tc.ToolName, trimmed, tc.TurnIdx, ts)
	default:
		st.addFriction(friction.KindToolError, tc.ToolName+": "+firstLine(content), tc.ToolName, trimmed, tc.TurnIdx, ts)
	}
}

// recordInterrupts emits one user.interrupt per text block the harness wrote
// when the user cut the turn short.
func (st *state) recordInterrupts(blocks []userContentBlock, ts time.Time) {
	for _, b := range blocks {
		if b.Type == "text" && strings.HasPrefix(b.Text, interruptPrefix) {
			st.addFriction(friction.KindUserInterrupt, b.Text, "", b.Text, st.currentTurnIdx, ts)
		}
	}
}

// firstLine is the first non-empty line of s, trimmed.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}
