package shipper

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"loom/internal/config"
	"loom/transport/internal/source"
	"loom/transport/internal/staging"
)

// followState is the host-metrics capture position: Offset bytes into the
// file whose device and inode are Dev and Ino. It stands in for the plain
// source cursor because a rotation moves the position to another file, and
// the file and offset have to change in one write: as two files, a crash
// between them either re-reads a rotated file from 0 or reads the new one
// from the old file's offset. Project is the staging destination bound at
// first capture, so a hostname change cannot split the one-key stream the
// way registryDestination guards against for the execution registry.
type followState struct {
	Project string `json:"project"`
	Dev     uint64 `json:"dev"`
	Ino     uint64 `json:"ino"`
	Offset  int64  `json:"offset"`
}

type fileID struct{ dev, ino uint64 }

func (s followState) id() fileID { return fileID{s.Dev, s.Ino} }

func followStatePath() string {
	return filepath.Join(config.TransportDir(), "host-metrics-follow.json")
}

func readFollowState() (followState, error) {
	var st followState
	b, err := os.ReadFile(followStatePath())
	if os.IsNotExist(err) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(b, &st); err != nil {
		return st, fmt.Errorf("parse %s: %w", followStatePath(), err)
	}
	return st, nil
}

func writeFollowState(st followState) error {
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return atomicRegistryWrite(followStatePath(), b)
}

func idOf(info os.FileInfo) (fileID, error) {
	sys, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fileID{}, fmt.Errorf("%s: no inode in stat", info.Name())
	}
	return fileID{uint64(sys.Dev), uint64(sys.Ino)}, nil
}

// generation is one rotated copy of the log: <path>.<n>, newest at n=0.
type generation struct {
	path string
	n    int
	id   fileID
}

// generations lists path's rotated copies, newest first. A sibling whose
// suffix is not a number is not newsyslog's.
func generations(path string) ([]generation, error) {
	matches, err := filepath.Glob(path + ".*")
	if err != nil {
		return nil, err
	}
	var out []generation
	for _, m := range matches {
		n, err := strconv.Atoi(strings.TrimPrefix(m, path+"."))
		if err != nil || n < 0 {
			continue
		}
		info, err := os.Stat(m)
		if err != nil {
			continue
		}
		id, err := idOf(info)
		if err != nil {
			continue
		}
		out = append(out, generation{path: m, n: n, id: id})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].n < out[j].n })
	return out, nil
}

// captureFollowed stages the sample log's new complete lines, following it by
// inode across rotation. newsyslog hard-links the live file to <path>.0 and
// renames a new one over the live name, so the bytes past the position are
// the tail of whichever generation now holds the position's inode, then each
// newer generation whole, then the live file from 0. First sight starts the
// live file at 0 and backfills no rotated copy. Staging stays one append-only
// file, so the ship cursor and the receiver's offset never see a rotation.
func captureFollowed(s source.Session, counts *tickCounts) {
	agent := source.HostMetricsAgent
	fail := func(err error) {
		log.Printf("fail stage=capture agent=%s project=%s session=%s class=io err=%q", agent, s.Project, s.SessionID, err)
		counts.captureFailed++
	}
	st, err := readFollowState()
	if err != nil {
		fail(err)
		return
	}
	saved := st
	if st.Project == "" {
		st.Project = s.Project
	}
	live, err := os.Open(s.Path)
	if err != nil {
		fail(err)
		return
	}
	defer live.Close()
	liveInfo, err := live.Stat()
	if err != nil {
		fail(err)
		return
	}
	liveID, err := idOf(liveInfo)
	if err != nil {
		fail(err)
		return
	}
	if st.id() == (fileID{}) {
		st.Dev, st.Ino, st.Offset = liveID.dev, liveID.ino, 0
	}

	for st.id() != liveID {
		gens, err := generations(s.Path)
		if err != nil {
			fail(err)
			return
		}
		at := -1
		for i, g := range gens {
			if g.id == st.id() {
				at = i
				break
			}
		}
		if at < 0 {
			log.Printf("warn stage=capture agent=%s project=%s session=%s gap: dev=%d ino=%d offset=%d is in no %s.<n>; reading the live file from 0",
				agent, st.Project, s.SessionID, st.Dev, st.Ino, st.Offset, s.Path)
			st.Dev, st.Ino, st.Offset = liveID.dev, liveID.ino, 0
			break
		}
		g := gens[at]
		next := liveID
		if at > 0 {
			next = gens[at-1].id
		}
		f, err := os.Open(g.path)
		if err != nil {
			fail(err)
			return
		}
		info, err := f.Stat()
		var id fileID
		if err == nil {
			id, err = idOf(info)
		}
		if err == nil && id != g.id {
			// Renamed again between the listing and the open; the next tick
			// lists the generations afresh.
			err = fmt.Errorf("%s rotated during capture", g.path)
		}
		var data []byte
		var to int64
		if err == nil {
			data, to, err = source.ReadDeltaFile(f, st.Offset)
		}
		f.Close()
		if err != nil {
			fail(err)
			return
		}
		if err := staging.Append(agent, st.Project, "", s.SessionID, data); err != nil {
			fail(fmt.Errorf("append: %w", err))
			return
		}
		log.Printf("capture agent=%s project=%s session=%s rotated=%s bytes=%d offset=%d→%d size=%d",
			agent, st.Project, s.SessionID, g.path, len(data), st.Offset, to, info.Size())
		if len(data) > 0 {
			counts.captured++
		}
		st.Dev, st.Ino, st.Offset = next.dev, next.ino, 0
		if err := writeFollowState(st); err != nil {
			fail(fmt.Errorf("state: %w", err))
			return
		}
		saved = st
	}

	if liveInfo.Size() < st.Offset {
		log.Printf("warn stage=capture agent=%s project=%s session=%s truncated: size=%d below offset=%d; reading from 0",
			agent, st.Project, s.SessionID, liveInfo.Size(), st.Offset)
		st.Offset = 0
	}
	data, to, err := source.ReadDeltaFile(live, st.Offset)
	if err != nil {
		fail(err)
		return
	}
	if err := staging.Append(agent, st.Project, "", s.SessionID, data); err != nil {
		fail(fmt.Errorf("append: %w", err))
		return
	}
	from := st.Offset
	st.Offset = to
	if st == saved {
		return
	}
	if err := writeFollowState(st); err != nil {
		fail(fmt.Errorf("state: %w", err))
		return
	}
	if len(data) > 0 {
		log.Printf("capture agent=%s project=%s session=%s bytes=%d offset=%d→%d",
			agent, st.Project, s.SessionID, len(data), from, to)
		counts.captured++
	}
}
