# Host metrics

A host can ship a JSON-lines log of metric samples it writes, and studio folds
the samples into `summaries.db` and serves them by host and time range. The
first producer is engine's sampler (engine repo, README `## Sample log`): one
line a minute at `/var/log/engine/samples.jsonl`, rotated by newsyslog. The
JSON the read command prints is the contract engine's dashboard reads.

## Shipping

The adapter is off unless the host's `$LOOM_HOME/config.json` names the log:

```json
{
  "server_url": "https://studio…",
  "auth_token": "…",
  "host_metrics_log": "/var/log/engine/samples.jsonl"
}
```

`host_metrics_log` must be absolute. loom names no default path: the log is
host-specific, and every other host ships nothing new. The shipper's user has
to be able to read the live file and its rotated copies.

The log ships as agent `loom-host-metrics`, session `samples`, under the
hostname-derived project segment the execution registry uses, and lands at
`received/loom-host-metrics/<host>/samples.jsonl`. The project is bound at the
first capture and kept, so a hostname change cannot split the stream.

### Rotation

newsyslog hard-links the live file to `<log>.0` (shifting `.0`–`.5` up one
first) and renames a new empty file over the live name. A byte cursor into
the live name would sit past the end of the new file and never read again, so
capture follows the file by inode instead. Its position is one file,
`$LOOM_HOME/transport/host-metrics-follow.json`:
`{project, dev, ino, offset}` — `offset` bytes into the file with that device
and inode. The file and offset change in one atomic write; as two files a
crash between them would re-read or skip bytes.

Each tick:

- **Same inode as the live file:** stage its complete lines past `offset`.
- **Different inode:** find it among the `<log>.<n>` siblings, stage its
  complete lines past `offset` (a line written through the old inode after
  the rename is there), then each newer generation whole, then the live file
  from 0. Two rotations between ticks are followed the same way. Logged as
  `capture agent=loom-host-metrics … rotated=<file> bytes=… offset=a→b size=…`.
- **Inode in no sibling** (more rotations missed than are kept): a
  `warn stage=capture … gap:` line names the lost position, and the live file
  is read from 0.
- **Same inode, smaller than `offset`** (truncated in place): a
  `warn stage=capture … truncated:` line, and the file is read from 0.
- **First sight:** the live file from 0; no rotated copy is backfilled.

A partial trailing line is left for the next tick. Staging stays one
append-only file, so the ship cursor and the receiver's offset never see a
rotation.

## Fold

`loom summarize` reads each `received/loom-host-metrics/<host>/*.jsonl` from
the byte its last import reached (`host_sample_imports`: `source_path`,
`offset`, `lines`), so a sweep reads only the new lines. A file smaller than
that offset, or `--force`, is re-read from 0, first deleting the rows that
file wrote. `loom summarize --rebuild` deletes the database, so the tables are
re-folded from `received/` with the same rows. The sweep logs one line per
file: `host samples <path> stored=N malformed=N unknown_version=N reset=bool`,
never a sample's content.

`host_samples` has one row per `(host, ts)`, both taken from the line; a
later line for the same pair replaces the row.

| Column | From the line |
| --- | --- |
| `host`, `ts` | `host`; `ts` as UTC `YYYY-MM-DDTHH:MM:SSZ` |
| `v` | `v` |
| `cpu_user_pct`, `cpu_sys_pct` | `cpu.user_pct`, `cpu.sys_pct` |
| `gpu_device_pct` | `gpu.device_pct` |
| `memory_pressure_level` | `memory.pressure_level` |
| `budget_wired_mib` | `budget.wired_mib` |
| `disk_avail_bytes` | `disk.avail_bytes` |
| `tailnet_path`, `tailnet_latency_ms` | `tailnet.path`, `tailnet.latency_ms` |
| `updates_restart_required` | `updates.restart_required` (0/1) |
| `sample` | the whole line, verbatim |
| `source_path`, `source_line` | the received file and 1-based line |

The scalar columns are a convenience for the obvious dashboard figures; any
other field is `json_extract(sample, …)`. A section the producer wrote as
`{"error": …}` leaves its columns NULL. A line that is not a JSON object, or
lacks `host` or a parseable `ts`, is skipped and counted `malformed`. A line
at a `v` other than 1 is stored with its `sample` and `v` and every scalar
column NULL, counted `unknown_version`: the scalar columns are read by the v1
field names, but the raw line still reaches a reader that knows the newer
version.

The tables are created on open with no schema version bump (see `schemaSQL`
in `internal/summaries/schema.go`).

## Read command

```sh
loom samples --host engine --since 2026-11-14T00:00:00Z --until 2026-11-15T00:00:00Z
```

Prints one JSON array: every sample whose `host` is `--host` and whose `ts` is
in `[--since, --until)`, ordered by `ts`, each element the line exactly as the
producer wrote it, whatever its `v`. Bounds take `YYYY-MM-DD` (local midnight)
or RFC3339. A window with no samples prints `[]` and exits 0. A missing
`summaries.db`, or one that predates the table, is an error on stderr with a
non-zero exit. The database is opened read-only.
