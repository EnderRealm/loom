// Package source enumerates agent session files and reads deltas from them.
// Each supported agent (Claude Code, Codex, …) is an Adapter that knows where
// its session files live and what stable IDs to use; downstream code treats
// every source the same way.
package source

import (
	"bytes"
	"io"
	"os"
)

// Session is one agent session file on disk, as produced by an Adapter.
//
// Project is the slug that becomes the storage directory; Cwd is the
// raw, unsanitized working directory the agent reported. Cwd is the
// authoritative project handle and feeds wire.ProjectIdentity. Cwd may
// be empty when the adapter cannot resolve it (e.g. a Claude session
// whose first line hasn't been written yet); the capture pass treats
// empty Cwd as "check back next tick".
type Session struct {
	Project   string
	SessionID string
	Path      string
	Cwd       string
}

// Adapter is one agent's source-file enumerator.
type Adapter interface {
	// Agent is the stable identifier sent in IngestRequest.Agent and used as a
	// path segment under staging/cursors. Must not contain path separators.
	Agent() string

	// List returns every session file currently visible for this adapter, or an
	// empty slice if the agent's source dir is missing.
	List() ([]Session, error)
}

// Adapters returns the set of enabled source adapters, in stable order.
// Each adapter's List() is tolerant of its agent not being installed (missing
// source dir → empty slice), so the set is static. Per-adapter opt-out is a
// later concern.
func Adapters() []Adapter {
	return []Adapter{claudeAdapter{}, codexAdapter{}}
}

// ReadDeltaBytes returns the raw complete-line byte range [from, toOffset) of
// path. Any partial trailing line is left for the next tick. Used by the
// capture pass so staging is a byte-for-byte copy of source (preserves blank
// lines and exact framing).
func ReadDeltaBytes(path string, from int64) (data []byte, toOffset int64, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, from, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, from, err
	}
	size := info.Size()
	if from >= size {
		return nil, from, nil
	}
	if _, err := f.Seek(from, io.SeekStart); err != nil {
		return nil, from, err
	}
	buf, err := io.ReadAll(f)
	if err != nil {
		return nil, from, err
	}
	lastNL := bytes.LastIndexByte(buf, '\n')
	if lastNL < 0 {
		return nil, from, nil
	}
	return buf[:lastNL+1], from + int64(lastNL+1), nil
}

// ReadDelta opens path, seeks to from, and returns only COMPLETE lines plus the
// new safe offset (byte after the last \n). Any partial trailing line is left
// for the next tick — guarantees we never ship a half-written JSON record.
// Used by the ship pass: the wire protocol takes []string.
func ReadDelta(path string, from int64) (lines []string, toOffset int64, err error) {
	data, to, err := ReadDeltaBytes(path, from)
	if err != nil || len(data) == 0 {
		return nil, to, err
	}
	raw := bytes.Split(bytes.TrimSuffix(data, []byte("\n")), []byte("\n"))
	lines = make([]string, 0, len(raw))
	for _, l := range raw {
		if len(l) == 0 {
			continue
		}
		lines = append(lines, string(l))
	}
	return lines, to, nil
}

// Size returns the current byte size of path, or 0 if the file does not exist.
// Used by the capture pass to bound reads and by the notifier to compute
// pending-session counts.
func Size(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	return info.Size(), nil
}
