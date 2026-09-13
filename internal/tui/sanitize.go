package tui

import "strings"

// sanitize strips terminal controls from a data-derived string before it is
// rendered: run records and lens responses are agent-written and replicate
// across machines, and lipgloss passes what they carry through to the
// terminal. Tab is kept; newline is not, so a multi-line body is split first
// and each line sanitized on its own.
func sanitize(s string) string {
	var b strings.Builder
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case r == 0x1b:
			i = escapeEnd(rs, i)
		case r == '\t':
			b.WriteRune(r)
		case r < 0x20, r == 0x7f, r >= 0x80 && r <= 0x9f:
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// escapeEnd is the index of the last rune of the escape sequence that starts
// at rs[i]: a CSI runs to its final byte, a string control (OSC, DCS, SOS,
// PM, APC) to BEL or ST, and anything else is ESC with its intermediates and
// one final byte.
func escapeEnd(rs []rune, i int) int {
	last := len(rs) - 1
	if i+1 > last {
		return i
	}
	j := i + 1
	switch rs[j] {
	case '[':
		for j++; j <= last; j++ {
			if rs[j] >= 0x40 && rs[j] <= 0x7e {
				return j
			}
		}
		return last
	case ']', 'P', 'X', '^', '_':
		for j++; j <= last; j++ {
			switch {
			case rs[j] == 0x07:
				return j
			case rs[j] == 0x1b && j < last && rs[j+1] == '\\':
				return j + 1
			}
		}
		return last
	}
	for j <= last && rs[j] >= 0x20 && rs[j] <= 0x2f {
		j++
	}
	return min(j, last)
}
