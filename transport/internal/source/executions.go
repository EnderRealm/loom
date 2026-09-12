package source

import (
	"os"
	"path/filepath"

	"loom/internal/config"
)

// ExecutionsAgent is the Adapter.Agent() value for the execution-record
// registry. Not an agent at all, but the transport is generic over the agent
// segment and the receiver lands the file under it like any transcript:
// received/loom-executions/<host>/executions.jsonl.
const ExecutionsAgent = "loom-executions"

// executionsFile is the registry's path under the loom state root. Producers
// append to it per docs/execution-records.md; it ships as one ever-growing
// session, so unlike attribution.jsonl it must never be truncated — the
// source cursor is a byte offset into it.
const executionsFile = "executions.jsonl"

// executionsSessionID is the one session the registry ships as. The host
// name is the project segment, so two machines' registries land apart.
const executionsSessionID = "executions"

type executionsAdapter struct{}

func (executionsAdapter) Agent() string { return ExecutionsAgent }

// List returns the registry as a single Session when the file exists. Cwd is
// left empty on purpose: the capture pass writes an identity sidecar only
// for a session with one, and a registry has no project.
func (executionsAdapter) List() ([]Session, error) {
	path := filepath.Join(config.Home(), executionsFile)
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	host, _ := os.Hostname()
	return []Session{{
		Project:   encodeProjectPath(host),
		SessionID: executionsSessionID,
		Path:      path,
	}}, nil
}
