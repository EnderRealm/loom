package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReceiverTokenRoundTrip(t *testing.T) {
	t.Setenv("LOOM_HOME", t.TempDir())

	if err := WriteReceiverToken("  s3cret  \n"); err != nil {
		t.Fatalf("WriteReceiverToken: %v", err)
	}
	if got := ReadReceiverToken(); got != "s3cret" {
		t.Fatalf("ReadReceiverToken = %q, want %q (trimmed round-trip)", got, "s3cret")
	}

	fi, err := os.Stat(ReceiverTokenPath())
	if err != nil {
		t.Fatalf("stat token file: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("token file perm = %o, want 600", perm)
	}
}

func TestWriteReceiverTokenTightensExistingPerms(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LOOM_HOME", dir)
	// Pre-seed a token file with loose perms (e.g. a manual migration under a
	// default umask). The overwrite must re-tighten it to 0600.
	if err := os.WriteFile(filepath.Join(dir, "receiver-token"), []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteReceiverToken("new"); err != nil {
		t.Fatalf("WriteReceiverToken: %v", err)
	}
	fi, err := os.Stat(ReceiverTokenPath())
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("overwritten token file perm = %o, want 600", perm)
	}
}

func TestReadReceiverTokenMissing(t *testing.T) {
	t.Setenv("LOOM_HOME", t.TempDir())
	if got := ReadReceiverToken(); got != "" {
		t.Fatalf("ReadReceiverToken on missing file = %q, want \"\"", got)
	}
}

func TestReadReceiverTokenEmptyFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LOOM_HOME", dir)
	if err := os.WriteFile(filepath.Join(dir, "receiver-token"), []byte("  \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ReadReceiverToken(); got != "" {
		t.Fatalf("ReadReceiverToken on whitespace-only file = %q, want \"\"", got)
	}
}

func TestWriteReceiverTokenRejectsEmpty(t *testing.T) {
	t.Setenv("LOOM_HOME", t.TempDir())
	if err := WriteReceiverToken("   "); err == nil {
		t.Fatal("WriteReceiverToken(\"   \") = nil, want error")
	}
}
