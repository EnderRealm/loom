package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// The receiver's shared bearer token. Persisted under Home() so the
// updater's re-bootstrap of the receiver (which shells `loom install
// receiver` with no token in its env) and interactive installs both
// resolve it without re-exporting the secret. Kept out of the receiver
// plist so it isn't visible via the plist file or `launchctl print`.

// ReceiverTokenPath returns the path to the receiver token file under Home().
func ReceiverTokenPath() string {
	return filepath.Join(Home(), "receiver-token")
}

// ReadReceiverToken returns the persisted receiver token, or "" when the
// file is missing, unreadable, or empty. Callers treat "" as "no token".
func ReadReceiverToken() string {
	data, err := os.ReadFile(ReceiverTokenPath())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// WriteReceiverToken persists tok (trimmed) as the receiver token in a
// 0600 file under Home(), creating Home() if needed. An empty token is
// rejected.
func WriteReceiverToken(tok string) error {
	tok = strings.TrimSpace(tok)
	if tok == "" {
		return errors.New("receiver token is empty")
	}
	if err := os.MkdirAll(Home(), 0o700); err != nil {
		return err
	}
	path := ReceiverTokenPath()
	if err := os.WriteFile(path, []byte(tok+"\n"), 0o600); err != nil {
		return err
	}
	// WriteFile only applies the mode on create; reapply so an overwrite of a
	// pre-existing file (e.g. one seeded with a looser umask) can't leave the
	// secret world-readable.
	return os.Chmod(path, 0o600)
}
