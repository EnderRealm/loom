# Loom

Loom captures agent session transcripts, ships them to a central receiver, folds them into a normalized summary database, and surfaces both raw state and durable knowledge through an interactive TUI. See `docs/overview.md` for the project philosophy.

## Status

| Subsystem    | State      | What it does                                                       |
| ------------ | ---------- | ------------------------------------------------------------------ |
| `transport`  | v1, usable | Ships agent session JSONL files to a central receiver              |
| `summarizer` | v1, usable | Folds received sessions into `~/.loom/summaries.db` (sqlite)       |
| `tui`        | v1, usable | Interactive dashboard for projects, sessions, and knowledge        |
| `loom` CLI   | v1, usable | Unified entry point (`tui`, `summarize`, `install`, `status`, ...) |

**Agents supported:** Claude Code (sessions at `~/.claude/projects/<sanitized-cwd>/<uuid>.jsonl`) and Codex CLI (rollouts at `~/.codex/sessions/**/rollout-<ts>-<uuid>.jsonl`).

`extractors/` is a Python research project (truth/decision extraction from session summaries) that operates over the durable knowledge store at `~/.loom/knowledge/` — its own git repo, see that store's `SCHEMA.md`. The TUI reads that store; the Go code does not write to it.

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

Everything is driven by the unified `loom` binary. Two install paths:

### Option A — Homebrew (no Go toolchain required)

```sh
brew install --formula enderrealm/tools/loom
```

Installs a prebuilt binary from the shared `EnderRealm/homebrew-tools` tap.
Updates: `brew upgrade enderrealm/tools/loom`. The release pipeline that
builds those binaries and the formula lives in
[`docs/releasing.md`](./docs/releasing.md).

Two things Homebrew will tell you about, both expected:

- **Always qualify the name.** A bare `brew install loom` resolves to the
  unrelated Loom.com screen-recorder *cask*, not this formula — hence the
  `--formula enderrealm/tools/loom`. On a machine that already has that
  cask installed, Homebrew skips linking our binary into the prefix
  ("loom cask is installed, skipping link"); `loom` deploys (shipper /
  receiver hosts) won't have the cask, so this only affects desktops.
- **Trust the tap** if you have `HOMEBREW_REQUIRE_TAP_TRUST` enabled (the
  default in a future Homebrew): `brew trust --formula enderrealm/tools/loom`.

### Option B — Source checkout (recommended for development and auto-update)

```sh
git clone git@github.com:EnderRealm/loom.git ~/code/loom
cd ~/code/loom
go install ./cmd/loom        # or `go build -o ~/.local/bin/loom ./cmd/loom`
```

Updates: pull and rebuild manually, or `loom install updater` to have a daemon do it for you (see "Auto-update" below).

### Common: install launchd agents

```sh
loom install server             # receiver + summarizer
loom install receiver           # receiver only
loom install summarizer         # summarizer only
loom install shipper            # shipper (client side)
loom install updater            # source checkouts only — see Auto-update
loom uninstall                  # remove all loom launchd agents
loom status                     # show state of all installed components
loom ui                         # open the dashboard (alias: loom tui)
```

`install.sh` is preserved as a thin forwarder for muscle memory: each `--install-X` flag does `go build -o $LOOM_BIN_DIR/loom ./cmd/loom` then `loom install X`. Either entry point works; new docs prefer the `loom` binary directly.

State lives under `$LOOM_HOME` (default `~/.loom`); the binary lives wherever your installer put it (`/opt/homebrew/bin/loom` for Homebrew, `~/.local/bin/loom` for `go install`).

```sh
LOOM_BIN_DIR=/usr/local/bin ./install.sh --install-shipper
LOOM_HOME=/srv/loom         loom install receiver
```

> If `~/.local/bin` isn't in your `$PATH`, add it to your shell rc file. launchd runs the binary by absolute path regardless, so this is purely for interactive use.

### Auto-update

The `loom updater` daemon polls `origin/main` on the source checkout, pulls + rebuilds + kickstarts every loom agent (itself last) when new commits land. Source checkouts only — Homebrew users get updates via `brew upgrade`.

```sh
loom install updater
tail -f ~/.loom/updater.log
```

Defaults: 5-minute poll, source at `~/code/loom`. Override via plist environment:

| env var                          | default            | purpose                                   |
| -------------------------------- | ------------------ | ----------------------------------------- |
| `LOOM_SOURCE`                    | `~/code/loom`      | git checkout the updater pulls + rebuilds |
| `LOOM_UPDATER_INTERVAL_MINUTES`  | `5`                | poll cadence                              |

The reusable pattern (Go binary on launchd that updates itself from git push) is documented in [`docs/auto-update-pattern.md`](./docs/auto-update-pattern.md).

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

The same value goes into the client's `~/.loom/config.json` on each shipping machine.

### 2. Install the receiver agent

```sh
loom install receiver
```

This:
1. Creates `$LOOM_HOME/received/`
2. Writes `~/Library/LaunchAgents/com.loom.receiver.plist` with `KeepAlive=true` (restarts on exit), `RunAtLoad=true` (starts immediately), and `LOOM_RECEIVER_TOKEN` + `LOOM_HOME` baked into `EnvironmentVariables`
3. Validates the plist with `plutil -lint`
4. Boots out any prior instance, bootstraps the new one, and `kickstart`s it
5. Polls `http://127.0.0.1:8765/healthz` for up to 10s to confirm it came up

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

Auth token can also come from `LOOM_RECEIVER_TOKEN`. If neither is set, the server accepts all requests (dev/localhost only).

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
| `loom ui`                     | Interactive dashboard (alias: `loom tui`).                                  |
| `loom install <component>`    | Components: `server` / `receiver` / `summarizer` / `shipper`.               |
| `loom uninstall`              | Remove every loom launchd agent. State preserved.                           |
| `loom status`                 | Launchctl state per component + sync health + config presence.              |

**`./install.sh`** is a transitional forwarder (`--install-X` → `loom install X`); use the loom binary directly going forward.

---

## Updates

The key design fact: **the launchd plist pins an absolute binary path at install time**. If you rebuild to the same path, the next scheduled tick runs the new binary — no reinstall needed.

### Simple updates (code change only)

Rebuild the loom binary in place; running daemons pick it up on the next respawn. The shipper reads `interval_minutes` at daemon startup, the receiver re-execs on `KeepAlive`, and the summarizer's watch loop is interruptible — kickstarting all three is enough.

```sh
cd ~/code/loom
git pull
go build -o ~/.local/bin/loom ./cmd/loom

launchctl kickstart -k gui/$(id -u)/com.loom.receiver
launchctl kickstart -k gui/$(id -u)/com.loom.summarizer
launchctl kickstart -k gui/$(id -u)/com.loom.shipper
```

### When a reinstall is required

Re-run `loom install <component>` when any of these change:

- **Binary path** — you moved the loom binary off the absolute path the plist pinned
- **`interval_minutes`** in config — only takes effect at daemon startup; reinstall (or kickstart -k) the shipper after editing
- **`LOOM_HOME`** — if non-default, it's baked into `EnvironmentVariables` on every plist
- **`LOOM_RECEIVER_TOKEN`** — baked into the receiver plist at install time

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

**`LOOM_RECEIVER_TOKEN is not set`** (from `loom install receiver`) — export the token in your shell first: `export LOOM_RECEIVER_TOKEN="$(openssl rand -hex 32)"`, then re-run the install.

**All POSTs return 401** — token mismatch. Check that `LOOM_RECEIVER_TOKEN` on the server matches `auth_token` in `~/.loom/config.json` on the client. A token change on the server requires `loom install receiver` to rewrite the plist.

**Shipper logs `shipped=0 skipped=60 failed=0`** — nothing to ship. All cursors are at EOF for every session. This is the steady state.

**Shipper logs `shipped=0 skipped=0 failed=60`** — everything failed. Check `server_url` (is it reachable? `curl <url>/healthz`), check auth, and check the receiver's log at `~/.loom/receiver.log`.

**`another shipper is running, skipping`** — the flock is held. Usually means the daemon is running and you tried `loom shipper once` against it. That's expected; the daemon already covers the workload.

**`launchctl print` says "Could not find service"** — the agent isn't loaded. Re-run `loom install <component>`, or check the relevant plist exists under `~/Library/LaunchAgents/` and is valid (`plutil -lint <path>`).

**Shipper runs but nothing appears on the server** — partial lines aren't shipped until the trailing `\n` arrives. If you're watching an active session, the last turn may lag the shipper by one tick. Confirm steady-state by reading the log a few ticks later.

**Shipper hasn't fired in hours despite `KeepAlive=true`** — check `launchctl print gui/$UID/com.loom.shipper`. `state = running` means the daemon is alive; if so, look at `~/.loom/transport/shipper.log` for `done captured=…` ticks. `state = not running` with `last exit code != 0` means the daemon is crash-looping; the log will show why.
