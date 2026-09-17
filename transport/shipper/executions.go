package shipper

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"

	"loom/internal/config"
	"loom/transport/cursor"
	"loom/transport/internal/source"
	"loom/transport/internal/staging"
)

const registrySession = "executions"

func registryProjectPath() string { return filepath.Join(config.TransportDir(), "executions-project") }
func validRegistryProject(project string) bool {
	return project != "" && project != "." && !strings.Contains(project, "..") && !strings.ContainsAny(project, "/\\") && strings.IndexFunc(project, unicode.IsControl) < 0
}
func readRegistryProject() (string, error) {
	b, err := os.ReadFile(registryProjectPath())
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	project := strings.TrimSpace(string(b))
	if !validRegistryProject(project) {
		return "", fmt.Errorf("invalid execution registry project binding")
	}
	return project, nil
}
func atomicRegistryWrite(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".executions-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// The registry's offsets have one key. Its staging and receiver destination
// must therefore survive hostname changes. Adopt an existing single stream
// on upgrade; a split stream requires explicit byte-checked reconciliation.
func registryDestination(proposed string) (string, error) {
	project, err := readRegistryProject()
	if err != nil {
		return "", err
	}
	bound := project
	entries, err := staging.List(source.ExecutionsAgent)
	if err != nil {
		return "", err
	}
	if len(entries) > 1 {
		return "", fmt.Errorf("split execution registry: run loom shipper reconcile-executions")
	}
	from, err := cursor.Read(cursor.KindSource, source.ExecutionsAgent, registrySession)
	if err != nil {
		return "", err
	}
	var size int64
	if len(entries) == 1 {
		e := entries[0]
		if e.SessionID != registrySession || e.ParentID() != "" || (project != "" && project != e.Project) {
			return "", fmt.Errorf("execution registry destination conflicts with binding")
		}
		project = e.Project
		size, err = source.Size(e.Path)
		if err != nil {
			return "", err
		}
	}
	if from < 0 || from != size {
		return "", fmt.Errorf("execution registry staging bytes=%d differ from source cursor=%d; reconciliation required", size, from)
	}
	if project == "" {
		project = proposed
	}
	if !validRegistryProject(project) {
		return "", fmt.Errorf("invalid execution registry project %q", project)
	}
	if bound != project {
		if err := atomicRegistryWrite(registryProjectPath(), []byte(project+"\n")); err != nil {
			return "", err
		}
	}
	return project, nil
}

// ReconcileExecutions restores the captured prefix of the authoritative source
// to one destination. receivedRoot must be a local, current receiver snapshot.
// All original bytes are retained before any staging change; neither cursor nor
// source nor receiver is rewritten. A partial application can be safely rerun.
func ReconcileExecutions(project, receivedRoot string, w io.Writer) error {
	if !validRegistryProject(project) {
		return fmt.Errorf("invalid project %q", project)
	}
	unlock, err := acquireLock()
	if err != nil {
		return fmt.Errorf("lock shipper: %w", err)
	}
	defer unlock()
	bound, err := readRegistryProject()
	if err != nil {
		return err
	}
	if bound != "" && bound != project {
		return fmt.Errorf("project differs from existing binding %q", bound)
	}
	srcPath := filepath.Join(config.Home(), "executions.jsonl")
	src, err := os.ReadFile(srcPath)
	if err != nil {
		return err
	}
	captured, err := cursor.Read(cursor.KindSource, source.ExecutionsAgent, registrySession)
	if err != nil {
		return err
	}
	shipped, err := cursor.Read(cursor.KindShip, source.ExecutionsAgent, registrySession)
	if err != nil {
		return err
	}
	if captured < 0 || captured > int64(len(src)) || shipped < 0 || shipped > captured {
		return fmt.Errorf("invalid registry offsets: source=%d ship=%d bytes=%d", captured, shipped, len(src))
	}
	prefix := src[:captured]
	if captured > 0 && prefix[len(prefix)-1] != '\n' {
		return fmt.Errorf("source cursor is not a complete record boundary")
	}
	recvPath := filepath.Join(receivedRoot, source.ExecutionsAgent, project, "executions.jsonl")
	recv, err := os.ReadFile(recvPath)
	if err != nil {
		return err
	}
	offPath := strings.TrimSuffix(recvPath, ".jsonl") + ".offset"
	off, err := os.ReadFile(offPath)
	if err != nil {
		return err
	}
	recvOffset, err := strconv.ParseInt(strings.TrimSpace(string(off)), 10, 64)
	if err != nil {
		return err
	}
	if recvOffset != shipped || int64(len(recv)) != shipped || !bytes.Equal(recv, src[:shipped]) {
		return fmt.Errorf("receiver bytes/offset do not match the acknowledged source prefix")
	}
	entries, err := staging.List(source.ExecutionsAgent)
	if err != nil {
		return err
	}
	originals := map[string][]byte{"source.jsonl": src, "received.jsonl": recv, "received.offset": off, "source.cursor": []byte(strconv.FormatInt(captured, 10)), "ship.cursor": []byte(strconv.FormatInt(shipped, 10))}
	canonical := false
	for _, e := range entries {
		if e.SessionID != registrySession || e.ParentID() != "" {
			return fmt.Errorf("unexpected registry staging entry %s", e.Path)
		}
		data, err := os.ReadFile(e.Path)
		if err != nil {
			return err
		}
		if len(data) > 0 && data[len(data)-1] != '\n' {
			return fmt.Errorf("incomplete staged record: %s", e.Path)
		}
		if e.Project == project {
			canonical = true
			if !bytes.HasPrefix(prefix, data) || len(data) < len(recv) {
				return fmt.Errorf("canonical staging is not the acknowledged source prefix")
			}
		} else {
			// Replays after a partial repair may already have the entire prefix in
			// the canonical file. Every remaining fragment must still be in source.
			at := bytes.Index(prefix, data)
			if at < 0 || (at > 0 && prefix[at-1] != '\n') {
				return fmt.Errorf("staged fragment absent from source: %s", e.Path)
			}
			other := filepath.Join(receivedRoot, source.ExecutionsAgent, e.Project, "executions.jsonl")
			b, err := os.ReadFile(other)
			if err != nil && !os.IsNotExist(err) {
				return err
			}
			if len(b) != 0 {
				return fmt.Errorf("alternate destination already has received bytes: %s", other)
			}
		}
		originals[filepath.Join("staging", e.Project+".jsonl")] = data
	}
	if !canonical {
		return fmt.Errorf("no canonical staging file for %q", project)
	}
	backup, err := os.MkdirTemp(config.TransportDir(), "executions-reconcile-")
	if err != nil {
		return err
	}
	for name, data := range originals {
		if err := atomicRegistryWrite(filepath.Join(backup, name), data); err != nil {
			return err
		}
	}
	manifest, _ := json.MarshalIndent(struct {
		Project           string
		Captured, Shipped int64
	}{project, captured, shipped}, "", "  ")
	if err := atomicRegistryWrite(filepath.Join(backup, "manifest.json"), manifest); err != nil {
		return err
	}
	fmt.Fprintf(w, "preserved registry bytes: %s\n", backup)
	if err := atomicRegistryWrite(staging.Path(source.ExecutionsAgent, project, "", registrySession), prefix); err != nil {
		return err
	}
	for _, e := range entries {
		if e.Project == project {
			continue
		}
		// The backup holds the original before removal from active staging.
		if err := os.Remove(e.Path); err != nil {
			return err
		}
	}
	if err := atomicRegistryWrite(registryProjectPath(), []byte(project+"\n")); err != nil {
		return err
	}
	fmt.Fprintf(w, "reconciled project=%s captured=%d acknowledged=%d pending=%d; run loom shipper once\n", project, captured, shipped, captured-shipped)
	return nil
}
