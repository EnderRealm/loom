# Loom

Loom captures agent session transcripts, ships them to a central receiver, folds them into a normalized summary database, and surfaces both raw state and durable knowledge through an interactive TUI. See `docs/overview.md` for the project philosophy.

## Status

| Subsystem    | State      | What it does                                                       |
| ------------ | ---------- | ------------------------------------------------------------------ |
| `transport`  | v1, usable | Ships agent session JSONL files to a central receiver              |
| `summarizer` | v1, usable | Folds received sessions into `~/.loom/summaries.db` (sqlite)       |
| `extractor`  | v1, usable | Runs `extractors/extract.py` over newly summarized sessions        |
| `tui`        | v1, usable | Interactive dashboard for projects, sessions, and knowledge        |
| `loom` CLI   | v1, usable | Unified entry point (`tui`, `summarize`, `install`, `status`, ...) |

**Agents supported:** Claude Code (sessions at `~/.claude/projects/<sanitized-cwd>/<uuid>.jsonl`) and Codex CLI (rollouts at `~/.codex/sessions/**/rollout-<ts>-<uuid>.jsonl`).

`extractors/` is a Python project (truth/decision extraction from session summaries) that operates over the durable knowledge store at `~/.loom/knowledge/` — its own git repo, see that store's `SCHEMA.md`. The TUI reads that store and the `loom extract` agent invokes `extract.py` against new sessions; the Go code never writes the store itself.

## Prerequisites

- macOS (the shipper uses launchd; Linux support is not planned until there is a real reason)
- Go 1.22 or newer (1.26 tested)

## Repo layout

```
loom/
  go.mod                               # module "loom"
  install.sh                           # transitional forwarder over `loom install`
  cmd/loom/                            # the only binary; cobra-based subcommand surface
  internal/
    config/                            # loom-wide config (Home, config.json schema)
    launchd/                           # plist generation + bootout/bootstrap helpers
    parse/                             # session-transcript parsers + sqlite store
    summarize/                         # received → summaries.db sweep
    tui/                               # interactive dashboard
  transport/
    shipper/                           # client logic (capture, ship, daemon)
    receiver/                          # server logic (HTTP ingest)
    internal/                          # wire, cursor, source, staging, notify
  docs/  extractors/                   # non-Go subsystems
  knowledge/                           # eval fixtures only — durable store is ~/.loom/knowledge/
```

Everything is driven by the unified `loom` binary.

### Install (any machine, no toolchain)

Download the latest release tarball for your OS/arch from [GitHub Releases](https://github.com/EnderRealm/loom/releases/latest) and extract `loom` to `~/.local/bin/loom`:

```sh
OS=$(uname -s | tr '[:upper:]' '[:lower:]')          # darwin | linux
ARCH=$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')   # amd64 | arm64
VER=$(curl -fsSL https://api.github.com/repos/EnderRealm/loom/releases/latest | grep -m1 '"tag_name"' | cut -d'"' -f4 | sed 's/^v//')
mkdir -p ~/.local/bin
curl -fsSL "https://github.com/EnderRealm/loom/releases/download/v${VER}/loom_${VER}_${OS}_${ARCH}.tar.gz" | tar -xzf - -C ~/.local/bin loom
```

Then install the launchd agents you want and the updater that keeps the binary on the latest release:

```sh
loom install server     # or remote / receiver / summarizer / extractor / shipper
loom install updater     # downloads + installs each new release; see Auto-update
```

### Develop

Build the binary locally to run unreleased changes — this is independent of the installed/daemon binary:

```sh
git clone git@github.com:EnderRealm/loom.git
cd loom
go build -o ./loom ./cmd/loom    # or: make dev
./loom <command>
```

### Install launchd agents

```sh
loom install server             # receiver + summarizer + extractor; records role=server
loom install remote             # shipper (client side); records role=remote
loom install receiver           # receiver only
loom install summarizer         # summarizer only
loom install extractor          # knowledge extraction trigger only
loom install shipper            # shipper only (no role change)
loom install updater            # keep the binary on the latest release — see Auto-update
loom uninstall                  # remove all loom launchd agents
loom status                     # show state of all installed components
loom ui                         # open the dashboard (alias: loom tui)
```

The `server` and `remote` profiles also record the machine's role under `$LOOM_HOME/role`. `loom dev` and `loom status` scope their health rollup to the daemons that role expects, so a `remote` machine running only the shipper no longer reports "degraded" for the receiver/summarizer it was never meant to run. Installing individual components (`receiver`, `summarizer`, `extractor`, `shipper`, `updater`) leaves the role untouched.

`install.sh` is preserved as a thin forwarder for muscle memory: each `--install-X` flag does `go build -o $LOOM_BIN_DIR/loom ./cmd/loom` then `loom install X`. Either entry point works; new docs prefer the `loom` binary directly.

State lives under `$LOOM_HOME` (default `~/.loom`); the binary lives at `~/.local/bin/loom`, the path the launchd plists pin and the updater installs new releases over.

```sh
LOOM_BIN_DIR=/usr/local/bin ./install.sh --install-shipper
LOOM_HOME=/srv/loom         loom install receiver
```

> If `~/.local/bin` isn't in your `$PATH`, add it to your shell rc file. launchd runs the binary by absolute path regardless, so this is purely for interactive use.

### Auto-update

The `loom updater` daemon polls EnderRealm/loom's latest GitHub Release on an interval. When a newer release than the running binary ships, it downloads the platform tarball, verifies its `checksums.txt` entry, installs the extracted binary over `~/.local/bin/loom`, and kickstarts every loom agent (itself last). No git checkout and no Go toolchain are needed on the host — the installed binary is always a published semver release.

```sh
loom install updater
tail -f ~/.loom/updater.log
```

Default: 5-minute poll. Override via plist environment:

| env var                          | default            | purpose       |
| -------------------------------- | ------------------ | ------------- |
| `LOOM_UPDATER_INTERVAL_MINUTES`  | `5`                | poll cadence  |

The reusable pattern (Go binary on launchd that updates itself) is documented in [`docs/auto-update-pattern.md`](./docs/auto-update-pattern.md); loom runs its artifact-fetch variant.

### Knowledge extraction

The `loom extract` agent closes the loop between capture and the durable knowledge store: every sweep it walks summarized sessions that have not been extracted yet and runs `extractors/extract.py` over each one, so candidates land in `~/.loom/knowledge/_candidates/<type>s/<scope>/` and show up in the TUI's candidate review screen.

```sh
loom install extractor
tail -f ~/.loom/extractor.log
```

- **Scope** is the basename of the session's normalized git remote (`github.com/enderrealm/loom` → `loom`). A session with no remote, one whose remote yields an unsafe scope name, or one whose scope has no `knowledge/truths/<scope>/` directory, is **skipped with a logged reason** — there is no default scope.
- **Forward only.** A watermark is stamped in `~/.loom/extract.state` the first time the agent runs; sessions summarized before it are left alone. The historical backlog is `loom extract --backfill`'s job (below).
- **Claude Code only** for now: `extractors/preprocess.py` reads Claude Code jsonl, so codex sessions are skipped with a logged reason.
- **At most once**: every visited session is recorded in `~/.loom/extract.state` with its outcome (`extracted` / `skipped` / `failed`), so re-running a sweep is a no-op. Re-running one session means deleting its entry. A run that completed but scored below the extractor's coverage threshold is `extracted`, not `failed` — only a run that never produced a result is a failure.
- **`extract.py` is not in the release tarball.** The agent resolves it from a checkout; a missing script makes each sweep a logged no-op instead of a crash loop, and `loom status` shows the resolved path.

| env var                 | default                  | purpose                          |
| ----------------------- | ------------------------ | -------------------------------- |
| `LOOM_EXTRACTORS_DIR`   | `~/code/loom/extractors` | where `extract.py` lives         |
| `LOOM_KNOWLEDGE_ROOT`   | `$LOOM_HOME/knowledge`   | the knowledge store to write into |
| `LOOM_EXTRACT_PROVIDER` | `claude`                 | extractor LLM provider           |
| `LOOM_EXTRACT_MODEL`    | `sonnet`                 | extractor model                  |

Set them before `loom install extractor`. Install persists them to `$LOOM_HOME/extract-env` and bakes them into the plist, since launchd jobs don't inherit a login shell's environment and the auto-updater re-installs the agent from its own. `loom status` prints what the agent resolves, which is the persisted value unless the invoking environment overrides it.

#### Backfilling the historical backlog

`loom extract --backfill` is the operator-run pass over the sessions the watermark excludes. It runs once — `--watch` is rejected — and ignores the per-sweep cap, so clearing hundreds of sessions isn't paced at four per quarter hour. It shares `~/.loom/extract.state` and the same provider/model tunables with the trigger, so neither re-extracts what the other already visited, and an interrupted run resumes where it stopped.

```sh
loom extract --backfill --dry-run                 # what a real run would do, spending nothing
loom extract --backfill --scope loom --limit 20
```

- `--dry-run` reports the selection — how many sessions resolve to each scope, and how many are excluded and why — and spends nothing: no LLM call, no write to the knowledge store, no ledger entry (not even the watermark, on a host where the trigger has never run).
- `--scope` restricts the run to named scopes (repeatable, or comma-separated) so one can be judged before committing to the rest. It is matched case-insensitively, and a value with no `knowledge/truths/<scope>/` directory is rejected up front — no scope resolves to it, so the run would otherwise report "0 to extract" and exit 0, which reads exactly like an already-cleared backlog.
- `--limit` bounds the run, stopping between sessions rather than mid-session. A negative value is rejected; `0` is the default and means unbounded. All three flags require `--backfill`, and that is judged on the flag being passed, so `--limit 0` or `--dry-run=false` handed to a sweep is an error rather than a silent no-op.
- Skips are logged but **not** recorded, unlike a sweep's. Most of the backlog's skips are `unknown scope`; recording them would mean creating `knowledge/truths/<scope>/` later could never rescue the sessions it was created for.
- Progress — the selection report, one line per session as it completes, and a running count every 10 — prints to the terminal and is appended to `~/.loom/extractor.log`, so a run that spends hours of LLM time leaves its trail in the same audit log the agent writes, without a shell redirect.
- **Safe to run while the agent sweeps**, in this specific sense: ledger writes merge under a file lock, so a backfill holding its snapshot for hours and a quarter-hour sweep holding its own don't erase each other's records; and the backfill re-reads the ledger immediately before each extraction, dropping — with a logged line, and a `skipped=` count in its final tally — any session the trigger recorded since the plan was computed. The plan's up-front counts are therefore a forecast: a run can extract fewer sessions than it announced. What is not covered is the overlap window itself — neither side records a session until it finishes, so the trigger can still start on one the backfill is mid-way through.

---

# Transport

A lightweight agent-session shipper. The client (`loom shipper daemon`) walks agent session files, ships byte-delta batches to the server (`loom receiver`) over HTTP, and persists per-session cursors so subsequent runs are incremental and idempotent. The shipper runs as a long-lived KeepAlive daemon with an in-process ticker (driven by `interval_minutes` in config); it does not rely on launchd's `StartInterval`, which dasd coalesces aggressively on modern macOS.

**Agents supported in v1:** Claude Code (sessions at `~/.claude/projects/<sanitized-cwd>/<uuid>.jsonl`) and Codex CLI (rollouts at `~/.codex/sessions/<yyyy>/<mm>/<dd>/rollout-<ts>-<uuid>.jsonl`).

## State locations

```
~/.loom/
  config.json                                  # client: server URL, auth token, interval
  transport/
    cursors/source/<agent>/<uuid>.cursor       # client: next byte read from source
    cursors/ship/<agent>/<uuid>.cursor         # client: next byte shipped to receiver
    staging/<agent>/<project>/<uuid>.jsonl     # client: bytes captured locally
    staging/<agent>/<project>/<uuid>.meta.json # client: per-session project identity
    shipper.lock                               # client: flock, one shipper at a time
    shipper.log                                # client: launchd stdout/stderr capture
  received/                                    # server: default storage root
    <agent>/
      <sanitized-project>/
        <uuid>.jsonl                           # appended-to session file
        <uuid>.offset                          # next expected byte (idempotency)
        <uuid>.meta.json                       # project identity (git remote, raw cwd)
  summaries.db                                 # server: normalized summary database
  summarizer.log                               # server: summarizer launchd capture
  extract.state                                # server: extractor watermark + visited sessions
  extract-env                                  # server: extractor tunables baked into its plist
  extractor.log                                # server: extractor launchd capture
  knowledge/                                   # durable knowledge store (separate git repo)
```

State lives per-user per-machine. Override the root with `LOOM_HOME=/some/path`.

`<agent>` is `claude-code` or `codex-cli`. The two-stage shipper captures from the agent's source directory into local `staging/`, then ships staging deltas to the receiver — so the agent can clean up its own session files without losing data.

### Project identity

Each session carries an authoritative project identity captured at the shipper:

- **`git_remote`** — `git -C <cwd> remote get-url origin` at capture time, normalized (SSH and HTTPS variants of the same origin collapse). Canonical when present.
- **`cwd`** — the raw, unsanitized working directory the agent reported. Authoritative fallback when there's no git remote.
- **`root_slug`** — the legacy sanitized path used as the on-disk storage directory, kept for backward compatibility.

Identity travels in `wire.IngestRequest.ProjectIdentity` (optional; pre-identity clients omit it), is persisted by the receiver as a `<session>.meta.json` sidecar next to the JSONL, and is folded into `summaries.db` (`sessions.git_remote`, `sessions.cwd_raw`). The TUI groups projects by `git_remote`, then `cwd`, then `root_slug` — so the same repo shipped from two laptops with different sanitized paths rolls up into one project, and basename collisions like `code/loom` vs `other/loom` stay separate.

## Server setup

The server can be any Mac reachable from the client(s). It can be the same machine as the client for experimentation.

### 1. Generate and export a shared bearer token

```sh
export LOOM_RECEIVER_TOKEN="$(openssl rand -hex 32)"
```

On first `loom install receiver`, this value is persisted to `~/.loom/receiver-token` (mode 0600), so later installs and the auto-updater's re-bootstrap don't need it re-exported. If you skip the export, an interactive install prompts for the token; a non-interactive install with no env var and no token file errors instead.

The same value goes into the client's `~/.loom/config.json` on each shipping machine.

### 2. Install the receiver agent

```sh
loom install receiver
```

This:
1. Resolves the bearer token (`LOOM_RECEIVER_TOKEN` → `~/.loom/receiver-token` → interactive prompt) and persists it to `~/.loom/receiver-token` (0600)
2. Creates `$LOOM_HOME/received/`
3. Writes `~/Library/LaunchAgents/com.loom.receiver.plist` with `KeepAlive=true` (restarts on exit), `RunAtLoad=true` (starts immediately), and `LOOM_HOME` baked into `EnvironmentVariables`. The token is read from `~/.loom/receiver-token` at runtime, not the plist, so it's not exposed via the plist or `launchctl print`
4. Validates the plist with `plutil -lint`
5. Boots out any prior instance, bootstraps the new one, and `kickstart`s it
6. Polls `http://127.0.0.1:8765/healthz` for up to 10s to confirm it came up

The plist runs the loom binary at the absolute path of whichever `loom` was on `$PATH` at install time. Rebuild to the same path and the next respawn picks up new code.

For a remote server, replace `127.0.0.1` with its reachable address when verifying from elsewhere; also open port 8765 or set up a tunnel as needed.

### Receiver flags (for manual runs)

For foreground debugging:

```sh
LOOM_RECEIVER_TOKEN=<token> loom receiver
# → listening on :8765, storage=/Users/<you>/.loom/received
```

| Flag            | Default                          | Purpose                                  |
| --------------- | -------------------------------- | ---------------------------------------- |
| `--addr`        | `:8765`                          | Listen address                           |
| `--storage`     | `$LOOM_HOME/received`            | Where to write session files             |
| `--auth-token`  | (empty; falls back to env)       | Shared bearer token; empty disables auth |

Auth token can also come from `LOOM_RECEIVER_TOKEN`, or the persisted `~/.loom/receiver-token` written at install time. If none is set, the server accepts all requests (dev/localhost only).

---

## Client (remote machine) setup

Each machine you want to ship sessions from runs its own loom shipper daemon + config + launchd agent.

### 1. Create `~/.loom/config.json`

```json
{
  "server_url": "http://your-server:8765",
  "auth_token": "the-same-token-as-the-server",
  "interval_minutes": 10
}
```

| Field              | Required | Default | Notes                                                         |
| ------------------ | -------- | ------- | ------------------------------------------------------------- |
| `server_url`       | yes      | —       | Base URL of the receiver; no trailing `/v1/ingest`            |
| `auth_token`       | no       | empty   | Bearer token; must match the server                           |
| `interval_minutes` | no       | `10`    | In-process ticker cadence inside `loom shipper daemon`        |

Permissions: `chmod 600 ~/.loom/config.json` — it contains the bearer token.

### 2. Install the shipper agent

```sh
loom install shipper
```

This:
1. Loads `$LOOM_HOME/config.json` to read `interval_minutes`
2. Writes `~/Library/LaunchAgents/com.loom.shipper.plist` with `KeepAlive=true`, `ThrottleInterval=10`, `RunAtLoad=true`, and `ProgramArguments=[<loom>, shipper, daemon]`
3. Validates the plist with `plutil -lint`
4. Boots out any prior instance and bootstraps the new one

The shipper plist intentionally has no `StartInterval` — dasd coalesces those aggressively on modern macOS (observed gaps of 24h+ in production). The daemon stays resident, runs the capture+ship pass on its own `time.Ticker`, and respawns on crash via `KeepAlive`.

### 3. Verify

```sh
loom status
tail -f ~/.loom/transport/shipper.log
```

First tick walks every session under `~/.claude/projects/` and `~/.codex/sessions/` and ships each one in full. Subsequent ticks ship only new bytes per session.

---

## Uninstall

```sh
loom uninstall
```

Removes every loom launchd agent (tolerant of any being absent) and their plist files. **Preserves** `~/.loom/` state and the installed binary. Full wipe:

```sh
rm -rf ~/.loom
rm -f ~/.local/bin/loom
```

---

## Commands reference

**The `loom` binary** is the single entry point. Subcommands:

| Command                       | What it does                                                                |
| ----------------------------- | --------------------------------------------------------------------------- |
| `loom shipper daemon`         | Long-lived shipper; capture+ship on `interval_minutes`. The launchd target. |
| `loom shipper once`           | Single pass; ship any new bytes and exit. Manual debugging.                 |
| `loom shipper health`         | Last-sync, pending count, uncaptured bytes per project.                     |
| `loom receiver`               | Run the ingest server (`:8765` by default).                                 |
| `loom summarize [--watch]`    | Fold received sessions into `~/.loom/summaries.db`.                         |
| `loom summarize --rebuild`    | Drop and re-fold the summary DB; the upgrade path for schema bumps.         |
| `loom extract [--watch]`      | Run `extract.py` over summarized sessions that haven't been extracted yet.  |
| `loom ui`                     | Interactive dashboard (alias: `loom tui`).                                  |
| `loom install <component>`    | Components: `server` / `remote` / `receiver` / `summarizer` / `extractor` / `shipper`. `server`/`remote` also record the machine role. |
| `loom uninstall`              | Remove every loom launchd agent. State preserved.                           |
| `loom status`                 | Launchctl state per component + sync health + config presence.              |

**`./install.sh`** is a transitional forwarder (`--install-X` → `loom install X`); use the loom binary directly going forward.

---

## Updates

The key design fact: **the launchd plist pins an absolute binary path at install time**. Whatever writes a new binary to that path — the updater installing a release, or you building locally — the next scheduled tick runs it, no reinstall needed.

### Released updates

Install the updater (see [Auto-update](#auto-update)). It polls GitHub Releases, installs each new release over `~/.local/bin/loom`, and kickstarts every agent. This is the production update channel; nothing manual is required.

### Testing an unreleased build

To run code that hasn't been released yet, build in place over the pinned path and kickstart the daemons. The shipper reads `interval_minutes` at daemon startup, the receiver re-execs on `KeepAlive`, and the summarizer's watch loop is interruptible — kickstarting all three is enough.

```sh
cd loom
go build -o ~/.local/bin/loom ./cmd/loom

launchctl kickstart -k gui/$(id -u)/com.loom.receiver
launchctl kickstart -k gui/$(id -u)/com.loom.summarizer
launchctl kickstart -k gui/$(id -u)/com.loom.shipper
```

The updater will reinstate the latest release on its next tick, so use this only for local testing.

### When a reinstall is required

Re-run `loom install <component>` when any of these change:

- **Binary path** — you moved the loom binary off the absolute path the plist pinned
- **`interval_minutes`** in config — only takes effect at daemon startup; reinstall (or kickstart -k) the shipper after editing
- **`LOOM_HOME`** — if non-default, it's baked into `EnvironmentVariables` on every plist
- **`LOOM_RECEIVER_TOKEN`** — persisted to `~/.loom/receiver-token` at install time; re-export and reinstall (or edit the file) to rotate

`loom install` boots out any prior instance with the same label before bootstrapping the new plist, so reinstall is always safe.

### Wire protocol changes

Both sides import `loom/transport/internal/wire`. If `IngestRequest` or `IngestResponse` ever changes shape, **update both sides at roughly the same time**. There's no version byte and no negotiation. A mismatch produces 400-level responses on the server and `failed=` counts on the client until both are on the same version. Failed batches retry on the next tick — no data loss during a brief skew.

### Cursor and offset file formats

Both are plain decimal integers in files on disk. Not negotiated, not serialized across the wire. Unlikely to change; if they ever do, a one-shot migration would run on each side independently.

### What persists across updates

- `~/.loom/config.json` — user-owned, never touched by the binary
- `~/.loom/transport/cursors/` — client cursors; keep them, they make updates resumable
- `~/.loom/received/` — server storage; keep it
- `~/.loom/transport/shipper.log` — append-only, rotate manually if it grows
- `~/.loom/summaries.db` — disposable. The summarizer rebuilds it from `~/.loom/received/` on demand. When this binary's schema is newer than the on-disk DB, the summarizer refuses to open it and instructs the user to re-run with `--rebuild`.

### Summary DB upgrades

```sh
loom summarize --rebuild       # drops summaries.db and re-folds from received/
```

Use this whenever the summarizer reports `summary db schema is outdated`. The DB is treated as a derived artifact — there's no migration path because there doesn't need to be one.

---

## Troubleshooting

First stop for any issue: `loom status`. It reports which agents are loaded, whether the config exists, and prints key launchctl fields.

**`config not found at ~/.loom/config.json`** — client hasn't been configured. Create the file (see "Client setup" step 1), then re-run `loom install shipper`.

**`LOOM_RECEIVER_TOKEN not set and no ~/.loom/receiver-token`** (from a non-interactive `loom install receiver`) — export the token first: `export LOOM_RECEIVER_TOKEN="$(openssl rand -hex 32)"`, then re-run the install, or run the install interactively to be prompted.

**All POSTs return 401** — token mismatch. Check that the server's `~/.loom/receiver-token` matches `auth_token` in `~/.loom/config.json` on the client. To rotate, edit `~/.loom/receiver-token` (or re-export `LOOM_RECEIVER_TOKEN` and reinstall) and reload the receiver.

**Shipper logs `shipped=0 skipped=60 failed=0`** — nothing to ship. All cursors are at EOF for every session. This is the steady state.

**Shipper logs `shipped=0 skipped=0 failed=60`** — everything failed. Check `server_url` (is it reachable? `curl <url>/healthz`), check auth, and check the receiver's log at `~/.loom/receiver.log`.

**`another shipper is running, skipping`** — the flock is held. Usually means the daemon is running and you tried `loom shipper once` against it. That's expected; the daemon already covers the workload.

**`launchctl print` says "Could not find service"** — the agent isn't loaded. Re-run `loom install <component>`, or check the relevant plist exists under `~/Library/LaunchAgents/` and is valid (`plutil -lint <path>`).

**Shipper runs but nothing appears on the server** — partial lines aren't shipped until the trailing `\n` arrives. If you're watching an active session, the last turn may lag the shipper by one tick. Confirm steady-state by reading the log a few ticks later.

**Shipper hasn't fired in hours despite `KeepAlive=true`** — check `launchctl print gui/$UID/com.loom.shipper`. `state = running` means the daemon is alive; if so, look at `~/.loom/transport/shipper.log` for `done captured=…` ticks. `state = not running` with `last exit code != 0` means the daemon is crash-looping; the log will show why.
