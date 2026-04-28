package source

import (
	"os"
	"path/filepath"
	"testing"
)

// TestReadClaudeCwdSparseFirstLine pins the bug that 882a7aa fixed:
// Claude session JSONLs frequently start with sparse-headered records
// (permission-mode, file-history-snapshot, agent-name, ...) that carry
// no cwd field. readClaudeCwd must keep scanning until a header-bearing
// record answers, otherwise the shipper emits Session{Cwd: ""} and
// capture pass skips writing the identity sidecar.
func TestReadClaudeCwdSparseFirstLine(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "first-line-has-cwd",
			body: `{"type":"user","cwd":"/Users/steve/code/loom","sessionId":"s1","timestamp":"2026-04-27T00:00:00Z"}` + "\n",
			want: "/Users/steve/code/loom",
		},
		{
			name: "permission-mode-then-system",
			body: `{"type":"permission-mode","sessionId":"s1","permissionMode":"acceptEdits"}` + "\n" +
				`{"type":"system","subtype":"info","sessionId":"s1","cwd":"/Users/steve/code/loom","timestamp":"2026-04-27T00:00:00Z"}` + "\n",
			want: "/Users/steve/code/loom",
		},
		{
			name: "many-sparse-headers-then-cwd",
			body: `{"type":"permission-mode","sessionId":"s1","permissionMode":"acceptEdits"}` + "\n" +
				`{"type":"file-history-snapshot","messageId":"m1","snapshot":{}}` + "\n" +
				`{"type":"agent-name","sessionId":"s1","agentName":"main"}` + "\n" +
				`{"type":"custom-title","sessionId":"s1","customTitle":"hi"}` + "\n" +
				`{"type":"user","cwd":"/Users/steve/code/loom","sessionId":"s1","timestamp":"2026-04-27T00:00:00Z"}` + "\n",
			want: "/Users/steve/code/loom",
		},
		{
			name: "malformed-line-then-cwd",
			body: `{not valid json}` + "\n" +
				`{"type":"user","cwd":"/Users/steve/code/loom","sessionId":"s1"}` + "\n",
			want: "/Users/steve/code/loom",
		},
		{
			name: "empty-cwd-fields-then-real-one",
			body: `{"type":"permission-mode","cwd":"","sessionId":"s1"}` + "\n" +
				`{"type":"user","cwd":"/Users/steve/code/loom","sessionId":"s1"}` + "\n",
			want: "/Users/steve/code/loom",
		},
		{
			name: "empty-file",
			body: "",
			want: "",
		},
		{
			name: "no-cwd-anywhere",
			body: `{"type":"permission-mode","sessionId":"s1"}` + "\n" +
				`{"type":"agent-name","sessionId":"s1","agentName":"x"}` + "\n",
			want: "",
		},
		{
			name: "partial-trailing-line",
			body: `{"type":"user","cwd":"/Users/steve/code/loom","sessionId":"s1"}` + "\n" +
				`{"type":"system","cwd":"/other"`, // no closing brace, no newline
			want: "/Users/steve/code/loom",
		},
		{
			name: "only-partial-line",
			body: `{"type":"user","cwd":"/Users/steve/code/loom"`, // never finished
			want: "",
		},
	}

	dir := t.TempDir()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := filepath.Join(dir, c.name+".jsonl")
			if err := os.WriteFile(p, []byte(c.body), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := readClaudeCwd(p)
			if err != nil {
				t.Fatalf("readClaudeCwd: %v", err)
			}
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}
