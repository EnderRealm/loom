package drift

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// TestUnmodeledSubtype pins the three ways a decode error composes into a
// subtype: an error that is not a type error, or a type error naming no field,
// is the bare marker; a field name past the length bound or carrying a rune
// outside the grammar degrades to the bare marker rather than being rewritten;
// a well-formed field name, wrapped or not, is carried into the subtype.
func TestUnmodeledSubtype(t *testing.T) {
	typeErr := func(field string) error {
		return &json.UnmarshalTypeError{Value: "string", Field: field}
	}
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"plain error", errors.New("x"), UnmodeledPayloadMarker},
		{"syntax error", &json.SyntaxError{Offset: 1}, UnmodeledPayloadMarker},
		{"type error without field", typeErr(""), UnmodeledPayloadMarker},
		{"field past the bound", typeErr(strings.Repeat("a", unmodeledFieldMax+1)), UnmodeledPayloadMarker},
		{"field at the bound", typeErr(strings.Repeat("a", unmodeledFieldMax)),
			UnmodeledPayloadMarker + ":" + strings.Repeat("a", unmodeledFieldMax)},
		{"field with a space", typeErr("a b"), UnmodeledPayloadMarker},
		{"field with a pipe", typeErr("x|y"), UnmodeledPayloadMarker},
		{"top-level field", typeErr("source"), UnmodeledPayloadMarker + ":source"},
		{"dotted path", typeErr("git.branch"), UnmodeledPayloadMarker + ":git.branch"},
		{"wrapped type error", fmt.Errorf("line 3: %w", typeErr("cwd")), UnmodeledPayloadMarker + ":cwd"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := UnmodeledSubtype(c.err); got != c.want {
				t.Errorf("UnmodeledSubtype(%v): got %q, want %q", c.err, got, c.want)
			}
		})
	}
}
