package staging

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"loom/transport/internal/source"
)

// snapshotDelta compares against the journal itself, so there is no second
// cursor write that can lag a successful capture across a process restart.
func snapshotDelta(path string, records []source.SnapshotRecord) ([]byte, []byte, error) {
	previous, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, nil, err
	}
	latest := map[string][]byte{}
	keys := map[string]source.SnapshotRecord{}
	if len(previous) > 0 && previous[len(previous)-1] != '\n' {
		return nil, nil, fmt.Errorf("incomplete snapshot journal %s", path)
	}
	for _, line := range bytes.Split(bytes.TrimSuffix(previous, []byte{'\n'}), []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		var r source.SnapshotRecord
		if err := json.Unmarshal(line, &r); err != nil || r.Format != source.CursorStoreFormat || r.Kind == "" {
			return nil, nil, fmt.Errorf("invalid snapshot journal record in %s", path)
		}
		key := r.Kind + "\x00" + r.Key
		if r.Deleted {
			delete(latest, key)
			delete(keys, key)
		} else {
			latest[key] = line
			keys[key] = r
		}
	}
	var delta []byte
	seen := map[string]bool{}
	for _, r := range records {
		key := r.Kind + "\x00" + r.Key
		if seen[key] {
			return nil, nil, fmt.Errorf("duplicate snapshot key %q", key)
		}
		seen[key] = true
		line, err := json.Marshal(r)
		if err != nil {
			return nil, nil, err
		}
		if !bytes.Equal(latest[key], line) {
			delta = append(delta, line...)
			delta = append(delta, '\n')
		}
	}
	var removed []string
	for key := range latest {
		if !seen[key] {
			removed = append(removed, key)
		}
	}
	sort.Strings(removed)
	for _, key := range removed {
		r := keys[key]
		r.Value = nil
		r.ValueType = "null"
		r.Deleted = true
		line, err := json.Marshal(r)
		if err != nil {
			return nil, nil, err
		}
		delta = append(delta, line...)
		delta = append(delta, '\n')
	}
	return previous, delta, nil
}

// CaptureSnapshot atomically extends the journal. A crash leaves either the
// old complete prefix or the new one; the next capture can derive its state
// from either. The shipper lock serializes capture and shipping.
func CaptureSnapshot(agent, project, parent, session string, records []source.SnapshotRecord) (int, error) {
	path := Path(agent, project, parent, session)
	previous, delta, err := snapshotDelta(path, records)
	if err != nil || len(delta) == 0 {
		return 0, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return 0, err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".snapshot-*")
	if err != nil {
		return 0, err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err := f.Write(previous); err != nil {
		return 0, err
	}
	if _, err := f.Write(delta); err != nil {
		return 0, err
	}
	if err := f.Sync(); err != nil {
		return 0, err
	}
	if err := f.Close(); err != nil {
		return 0, err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return 0, err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return 0, err
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return 0, err
	}
	return len(delta), nil
}

// SnapshotPending uses the same comparison as capture without changing state.
func SnapshotPending(path string, records []source.SnapshotRecord) (int64, error) {
	_, delta, err := snapshotDelta(path, records)
	return int64(len(delta)), err
}
