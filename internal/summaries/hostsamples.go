package summaries

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

// HostMetricsAgent is the agent the shipper files a host's metric sample log
// under, so it lands at received/loom-host-metrics/<host>/samples.jsonl.
// Declared here as well as in the transport, as ExecutionsAgent is.
const HostMetricsAgent = "loom-host-metrics"

// hostSampleVersion is the sample-line schema whose fields the scalar columns
// are read from. A line at any other version is still stored whole, with
// those columns NULL: the sample column keeps what the producer wrote, and a
// reader that knows the newer version can still read it.
const hostSampleVersion = 1

// sampleTSLayout is the producer's ts form and the stored one. Fixed width
// and UTC, so text order is time order and a window is a range scan.
const sampleTSLayout = "2006-01-02T15:04:05Z"

// sampleHead is what every line must carry to be stored.
type sampleHead struct {
	V    *int   `json:"v"`
	TS   string `json:"ts"`
	Host string `json:"host"`
}

// sampleV1 is the v1 fields the scalar columns hold. A section the producer
// wrote as {"error": ...} has none of them, so each lands NULL.
type sampleV1 struct {
	CPU struct {
		UserPct *float64 `json:"user_pct"`
		SysPct  *float64 `json:"sys_pct"`
	} `json:"cpu"`
	GPU struct {
		DevicePct *float64 `json:"device_pct"`
	} `json:"gpu"`
	Memory struct {
		PressureLevel *int64 `json:"pressure_level"`
	} `json:"memory"`
	Budget struct {
		WiredMiB *int64 `json:"wired_mib"`
	} `json:"budget"`
	Disk struct {
		AvailBytes *int64 `json:"avail_bytes"`
	} `json:"disk"`
	Tailnet struct {
		Path      *string  `json:"path"`
		LatencyMS *float64 `json:"latency_ms"`
	} `json:"tailnet"`
	Updates struct {
		RestartRequired *bool `json:"restart_required"`
	} `json:"updates"`
}

// HostSampleCounts is what one ImportHostSamples pass did, for the sweep's
// log line. Malformed counts lines skipped as not JSON or missing host or a
// parseable ts; UnknownVersion counts lines stored with no scalar columns.
// Reset is set when the import started over from byte 0 of a file it had
// already read part of.
type HostSampleCounts struct {
	Stored         int
	Malformed      int
	UnknownVersion int
	Reset          bool
}

// HostSamplesCurrent reports whether sourcePath was imported up to size.
func (s *Store) HostSamplesCurrent(sourcePath string, size int64) (bool, error) {
	var offset int64
	err := s.db.QueryRow(`SELECT offset FROM host_sample_imports WHERE source_path = ?`,
		sourcePath).Scan(&offset)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil && offset == size, err
}

// ImportHostSamples folds the complete lines of one received sample log past
// the offset its last import reached, so an append-only file is read once. A
// file smaller than that offset, or force, starts over from 0, first deleting
// the rows this file wrote, so the table is what a rebuild would produce. A
// trailing line with no newline is left for the next import. A later line for
// the same (host, ts) replaces the earlier row. Nothing from a line body
// reaches the returned error or the counts.
func (s *Store) ImportHostSamples(ctx context.Context, sourcePath string, r io.ReadSeeker,
	size int64, force bool) (HostSampleCounts, error) {
	var counts HostSampleCounts
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return counts, err
	}
	defer tx.Rollback()

	var offset, lines int64
	err = tx.QueryRowContext(ctx, `SELECT offset, lines FROM host_sample_imports WHERE source_path = ?`,
		sourcePath).Scan(&offset, &lines)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return counts, err
	}
	if offset > 0 && (force || size < offset) {
		counts.Reset = size < offset
		offset, lines = 0, 0
		if _, err := tx.ExecContext(ctx, `DELETE FROM host_samples WHERE source_path = ?`, sourcePath); err != nil {
			return counts, fmt.Errorf("clear samples: %w", err)
		}
	}
	if _, err := r.Seek(offset, io.SeekStart); err != nil {
		return counts, err
	}

	insert, err := tx.PrepareContext(ctx, `
		INSERT OR REPLACE INTO host_samples (host, ts, v, cpu_user_pct, cpu_sys_pct,
		    gpu_device_pct, memory_pressure_level, budget_wired_mib, disk_avail_bytes,
		    tailnet_path, tailnet_latency_ms, updates_restart_required,
		    sample, source_path, source_line)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return counts, err
	}
	defer insert.Close()

	br := bufio.NewReaderSize(io.LimitReader(r, size-offset), 64*1024)
	for {
		raw, err := br.ReadBytes('\n')
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return counts, err
		}
		offset += int64(len(raw))
		lines++
		text := bytes.TrimSpace(raw)
		if len(text) == 0 {
			continue
		}
		var head sampleHead
		if json.Unmarshal(text, &head) != nil || head.Host == "" {
			counts.Malformed++
			continue
		}
		ts, err := time.Parse(time.RFC3339, head.TS)
		if err != nil {
			counts.Malformed++
			continue
		}
		var v1 sampleV1
		if head.V != nil && *head.V == hostSampleVersion {
			// The line already parsed as JSON, so an error here is a field of
			// another type; Unmarshal still fills every field it could.
			_ = json.Unmarshal(text, &v1)
		} else {
			counts.UnknownVersion++
		}
		if _, err := insert.ExecContext(ctx, head.Host, ts.UTC().Format(sampleTSLayout), head.V,
			v1.CPU.UserPct, v1.CPU.SysPct, v1.GPU.DevicePct, v1.Memory.PressureLevel,
			v1.Budget.WiredMiB, v1.Disk.AvailBytes, v1.Tailnet.Path, v1.Tailnet.LatencyMS,
			v1.Updates.RestartRequired, string(text), sourcePath, lines); err != nil {
			return counts, fmt.Errorf("%s:%d: %w", sourcePath, lines, err)
		}
		counts.Stored++
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO host_sample_imports (source_path, offset, lines, imported_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(source_path) DO UPDATE SET
		    offset = excluded.offset, lines = excluded.lines, imported_at = excluded.imported_at`,
		sourcePath, offset, lines, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return counts, fmt.Errorf("mark import: %w", err)
	}
	return counts, tx.Commit()
}

// LoadHostSamples returns host's samples with ts in [since, until), ordered
// by ts, each exactly as the producer wrote it. A missing database, and one
// no build carrying the table has opened, are errors rather than an empty
// answer, which would read as the host having sent nothing.
func LoadHostSamples(dbPath, host string, since, until time.Time) ([]json.RawMessage, error) {
	if _, err := os.Stat(dbPath); err != nil {
		return nil, fmt.Errorf("summaries.db: %w — run `loom summarize`", err)
	}
	db, err := sql.Open("sqlite", readOnlyDSN(dbPath))
	if err != nil {
		return nil, fmt.Errorf("open summaries.db: %w", err)
	}
	defer db.Close()

	var n int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'host_samples'`).Scan(&n); err != nil {
		return nil, fmt.Errorf("read summaries.db: %w", err)
	}
	if n == 0 {
		return nil, fmt.Errorf("%s has no host_samples table — run `loom summarize` with a build that writes it", dbPath)
	}

	rows, err := db.Query(`SELECT sample FROM host_samples WHERE host = ? AND ts >= ? AND ts < ? ORDER BY ts`,
		host, sampleBound(since), sampleBound(until))
	if err != nil {
		return nil, fmt.Errorf("query host_samples: %w", err)
	}
	defer rows.Close()
	out := []json.RawMessage{}
	for rows.Next() {
		var sample string
		if err := rows.Scan(&sample); err != nil {
			return nil, err
		}
		out = append(out, json.RawMessage(sample))
	}
	return out, rows.Err()
}

// sampleBound is t in the stored ts form, rounded up to the second: samples
// fall on whole seconds, so ts >= ceil(t) is ts >= t, for either bound.
func sampleBound(t time.Time) string {
	t = t.UTC()
	if s := t.Truncate(time.Second); !s.Equal(t) {
		t = s.Add(time.Second)
	}
	return t.Format(sampleTSLayout)
}
