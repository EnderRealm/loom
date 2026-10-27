package shipper

import (
	"fmt"
	"log"
	"sync"

	"loom/transport/internal/source"
	"loom/transport/internal/staging"
)

// snapshotStaged is what a session looked like when it last staged
// successfully. Project and Cwd can come from a parent's sidecar, outside the
// session's own Version, so they are compared too.
type snapshotStaged struct {
	version, path, project, cwd string
}

// lastStaged lets an unchanged snapshot session skip ReadSnapshot: reading a
// Cursor store opens it, and opening a WAL store writes its -shm even
// read-only. Only successes are recorded, so a failed capture is retried.
var lastStaged = struct {
	sync.Mutex
	sessions map[string]snapshotStaged
}{sessions: map[string]snapshotStaged{}}

func captureSnapshot(ad source.SnapshotAdapter, s source.Session, gitCache map[string]string, counts *tickCounts) {
	key := ad.Agent() + "/" + s.Key()
	staged := snapshotStaged{version: s.Version, path: s.Path, project: s.Project, cwd: s.Cwd}
	lastStaged.Lock()
	unchanged := s.Version != "" && lastStaged.sessions[key] == staged
	lastStaged.Unlock()
	if unchanged {
		return
	}
	n, err := stageSnapshot(ad, s, gitCache)
	if err != nil {
		log.Printf("fail stage=capture agent=%s project=%s session=%s parent=%s class=io err=%q",
			ad.Agent(), s.Project, s.SessionID, s.ParentID(), err)
		counts.captureFailed++
		return
	}
	lastStaged.Lock()
	lastStaged.sessions[key] = staged
	lastStaged.Unlock()
	if n > 0 {
		log.Printf("capture agent=%s project=%s session=%s parent=%s bytes=%d format=snapshot",
			ad.Agent(), s.Project, s.SessionID, s.ParentID(), n)
		counts.captured++
	}
}

func stageSnapshot(ad source.SnapshotAdapter, s source.Session, gitCache map[string]string) (int, error) {
	records, err := ad.ReadSnapshot(s)
	if err != nil {
		return 0, err
	}
	remote, ok := gitCache[s.Cwd]
	if !ok {
		remote = resolveGitRemote(s.Cwd)
		gitCache[s.Cwd] = remote
	}
	// Sidecars precede the journal commit: even a crash between writes cannot
	// strand successfully captured bytes without their project or parent.
	if err := staging.WriteIdentity(ad.Agent(), s.Project, s.ParentID(), s.SessionID, staging.Identity{
		GitRemote: remote, Cwd: s.Cwd, RootSlug: s.Project,
	}); err != nil {
		return 0, fmt.Errorf("write identity: %w", err)
	}
	if sub := s.Subagent; sub != nil {
		if err := staging.WriteSubagent(ad.Agent(), s.Project, s.ParentID(), s.SessionID, staging.Subagent{
			ParentSessionID: sub.ParentSessionID, AgentType: sub.AgentType,
			Description: sub.Description, ToolUseID: sub.ToolUseID, SpawnDepth: sub.SpawnDepth,
		}); err != nil {
			return 0, fmt.Errorf("write subagent: %w", err)
		}
	}
	return staging.CaptureSnapshot(ad.Agent(), s.Project, s.ParentID(), s.SessionID, records)
}
