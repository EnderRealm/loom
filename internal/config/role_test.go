package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRoleRoundTrip(t *testing.T) {
	t.Setenv("LOOM_HOME", t.TempDir())

	for _, role := range []string{RoleServer, RoleRemote} {
		if err := WriteRole(role); err != nil {
			t.Fatalf("WriteRole(%q): %v", role, err)
		}
		if got := ReadRole(); got != role {
			t.Fatalf("ReadRole after WriteRole(%q) = %q, want %q", role, got, role)
		}
	}
}

func TestReadRoleMissing(t *testing.T) {
	t.Setenv("LOOM_HOME", t.TempDir())
	if got := ReadRole(); got != "" {
		t.Fatalf("ReadRole on missing file = %q, want \"\"", got)
	}
}

func TestWriteRoleRejectsInvalid(t *testing.T) {
	t.Setenv("LOOM_HOME", t.TempDir())
	if err := WriteRole("bogus"); err == nil {
		t.Fatal("WriteRole(\"bogus\") = nil, want error")
	}
	if got := ReadRole(); got != "" {
		t.Fatalf("ReadRole after rejected write = %q, want \"\"", got)
	}
}

func TestReadRoleJunk(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LOOM_HOME", dir)
	if err := os.WriteFile(filepath.Join(dir, "role"), []byte("not-a-role\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := ReadRole(); got != "" {
		t.Fatalf("ReadRole on junk file = %q, want \"\"", got)
	}
}
