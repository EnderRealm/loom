# Loom

Loom is a minimal pipeline for capturing, storing, and (eventually) processing agent sessions. See `docs/overview.md` for the project philosophy — this README covers how to build and run what exists today.

## Status

| Subsystem   | State      | What it does                                                |
| ----------- | ---------- | ----------------------------------------------------------- |
| `transport` | v1, usable | Ships agent session JSONL files to a central receiver       |
| everything else | not built yet | Extraction, storage, processing — deliberately deferred |

Transport is the only subsystem today. Anything else in this repo (`knowledge/`, `extractors/`, `docs/`) is notes, prompts, or scaffolding.

## Prerequisites

- macOS (the shipper uses launchd; Linux support is not planned until there is a real reason)
- Go 1.22 or newer (1.26 tested)

## Repo layout

```
loom/
  go.mod                               # module "loom"
  internal/config/                     # loom-wide config (Home, config.json schema)
  transport/
    cmd/
      loom-shipper/                    # client binary
      loom-receiver/                   # server binary
    internal/                          # transport-private packages (wire, cursor, source)
  docs/  extractors/  knowledge/       # non-Go subsystems
```

Build everything:

```sh
go build ./...
```

Build the two binaries to a stable location so launchd and your `$PATH` can find them:

```sh
go build -o /usr/local/bin/loom-shipper  ./transport/cmd/loom-shipper
go build -o /usr/local/bin/loom-receiver ./transport/cmd/loom-receiver
```

---

# Transport

A lightweight agent-session shipper. The client (`loom-shipper`) walks agent session files, ships byte-delta batches to the server (`loom-receiver`) over HTTP, and persists per-session cursors so subsequent runs are incremental and idempotent.

**Agents supported in v1:** Claude Code (sessions at `~/.claude/projects/<sanitized-cwd>/<uuid>.jsonl`).

## State locations

```
~/.loom/
  config.json                          # client: server URL, auth token, interval
  transport/
    cursors/claude-code/<uuid>.cursor  # client: next byte to ship per session
    shipper.lock                       # client: flock, one shipper at a time
    shipper.log                        # client: launchd stdout/stderr capture
  received/                            # server: default storage root
    claude-code/
      <sanitized-project>/
        <uuid>.jsonl                   # appended-to session file
        <uuid>.offset                  # next expected byte (idempotency)
```

State lives per-user per-machine. Override the root with `LOOM_HOME=/some/path`.

## Server setup

The server can be any Mac reachable from the client(s). It can be the same machine as the client for experimentation.

### 1. Pick a shared bearer token

```sh
openssl rand -hex 32
```

Write it down. The same value goes into the client config and the server env.

### 2. Run the receiver

Foreground, for development:

```sh
LOOM_RECEIVER_TOKEN=<your-token> loom-receiver
# → listening on :8765, storage=/Users/<you>/.loom/received
```

Flags (all optional):

| Flag            | Default                          | Purpose                                  |
| --------------- | -------------------------------- | ---------------------------------------- |
| `--addr`        | `:8765`                          | Listen address                           |
| `--storage`     | `$LOOM_HOME/received` (or `~/.loom/received`) | Where to write session files |
| `--auth-token`  | (empty; falls back to env)       | Shared bearer token; empty disables auth |

Auth token can also come from `LOOM_RECEIVER_TOKEN`. If neither is set, the server accepts all requests (dev/localhost only).

### 3. (Optional) Run the receiver as a launchd agent

The shipper ships itself as a self-managed launchd agent. The receiver does not — it's expected to be long-running, not scheduled, so the shape is different. If you want it under launchd, create this plist by hand as `~/Library/LaunchAgents/com.loom.receiver.plist`:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.loom.receiver</string>
    <key>ProgramArguments</key>
    <array>
        <string>/usr/local/bin/loom-receiver</string>
    </array>
    <key>EnvironmentVariables</key>
    <dict>
        <key>LOOM_RECEIVER_TOKEN</key>
        <string>YOUR-TOKEN-HERE</string>
    </dict>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>/Users/YOU/.loom/receiver.log</string>
    <key>StandardErrorPath</key>
    <string>/Users/YOU/.loom/receiver.log</string>
</dict>
</plist>
```

Replace `YOUR-TOKEN-HERE` and `/Users/YOU/` with real values. Then:

```sh
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.loom.receiver.plist
launchctl print gui/$(id -u)/com.loom.receiver    # confirm loaded
```

To uninstall: `launchctl bootout gui/$(id -u)/com.loom.receiver && rm ~/Library/LaunchAgents/com.loom.receiver.plist`.

### 4. Verify the server is up

```sh
curl http://127.0.0.1:8765/healthz
# → ok
```

For a remote server, replace `127.0.0.1` with its reachable address and open the port / set up a tunnel.

---

## Client (remote machine) setup

Each machine you want to ship sessions from runs its own `loom-shipper` + config + launchd agent.

### 1. Install the shipper binary to a stable path

```sh
go build -o /usr/local/bin/loom-shipper ./transport/cmd/loom-shipper
```

This path gets pinned into the launchd plist at install time. As long as you rebuild to the same path, updates are transparent.

### 2. Create `~/.loom/config.json`

```json
{
  "server_url": "http://your-server:8765",
  "auth_token": "the-same-token-as-the-server",
  "interval_minutes": 10
}
```

| Field              | Required | Default | Notes                                                         |
| ------------------ | -------- | ------- | ------------------------------------------------------------- |
| `server_url`       | yes      | —       | Base URL of `loom-receiver`; no trailing `/v1/ingest`         |
| `auth_token`       | no       | empty   | Bearer token; must match the server                           |
| `interval_minutes` | no       | `10`    | launchd `StartInterval` = `interval_minutes * 60`             |

Permissions: `chmod 600 ~/.loom/config.json` — it contains the bearer token.

### 3. First run (dry check)

Run once by hand before installing the agent, so you catch config mistakes interactively:

```sh
loom-shipper once
# → loom-shipper: shipped=N skipped=M failed=K total=T
```

If `failed` is high, check `server_url`, `auth_token`, and that the receiver is actually running.

### 4. Install the launchd agent

```sh
loom-shipper install-agent
```

This writes `~/Library/LaunchAgents/com.loom.shipper.plist`, validates it with `plutil -lint`, and bootstraps it into launchd. `RunAtLoad=true` means any backlog ships immediately on install and on login.

### 5. Verify

```sh
loom-shipper status              # launchctl print output, or "not loaded"
tail -f ~/.loom/transport/shipper.log
```

First run will walk every session under `~/.claude/projects/` and ship each one in full. Subsequent runs ship only new bytes per session.

### Commands

| Command                     | What it does                                                             |
| --------------------------- | ------------------------------------------------------------------------ |
| `loom-shipper once`         | Single pass. Safe to run manually at any time; idempotent; reentrant.    |
| `loom-shipper install-agent`| Generate plist, lint it, bootstrap into launchd.                         |
| `loom-shipper uninstall-agent` | Bootout and remove the plist. Tolerates "not loaded".                 |
| `loom-shipper status`       | `launchctl print` for the agent.                                         |

---

## Updates

The key design fact: **the launchd plist pins an absolute binary path at install time**. If you rebuild to the same path, the next scheduled tick runs the new binary — no reinstall needed.

### Client updates (rebuild only)

```sh
cd ~/code/loom
git pull
go build -o /usr/local/bin/loom-shipper ./transport/cmd/loom-shipper
# done — next tick (within interval_minutes) runs the new binary
```

### Client updates (reinstall required)

Run `loom-shipper install-agent` again if any of these change:

- The binary path (e.g., you moved from `/usr/local/bin` to `~/.local/bin`)
- `interval_minutes` in `~/.loom/config.json` (the interval is baked into the plist)
- `LOOM_HOME` (if non-default, it's baked into `EnvironmentVariables`)

`install-agent` is idempotent — it boots out any existing agent with the same label before bootstrapping the new plist.

### Server updates

```sh
cd ~/code/loom
git pull
go build -o /usr/local/bin/loom-receiver ./transport/cmd/loom-receiver

# If running under launchd:
launchctl kickstart -k gui/$(id -u)/com.loom.receiver

# If running in a terminal/tmux:
# kill the old process, then start the new one
```

### Wire protocol changes

Both sides import `loom/transport/internal/wire`. If `IngestRequest` or `IngestResponse` ever changes shape, **update both sides at roughly the same time**. There's no version byte and no negotiation. A mismatch produces 400-level responses on the server and `failed=` counts on the client until both are on the same version. Failed batches retry on the next tick — no data loss during a brief skew.

### Cursor and offset file formats

Both are plain decimal integers in files on disk. Not negotiated, not serialized across the wire. Unlikely to change; if they ever do, a one-shot migration would run on each side independently.

### What persists across updates

- `~/.loom/config.json` — user-owned, never touched by the binary
- `~/.loom/transport/cursors/` — client cursors; keep them, they make updates resumable
- `~/.loom/received/` — server storage; keep it
- `~/.loom/transport/shipper.log` — append-only, rotate manually if it grows

Nothing in the update flow requires wiping state. If an update ever did — for example, a cursor format change — it would be called out explicitly in that release.

---

## Troubleshooting

**`config not found at ~/.loom/config.json`** — client hasn't been configured. Create the file (see "Client setup" step 2).

**All POSTs return 401** — token mismatch. Check that `LOOM_RECEIVER_TOKEN` on the server equals `auth_token` in `~/.loom/config.json` on the client.

**`loom-shipper: shipped=0 skipped=60 failed=0 total=60`** — nothing to ship. All cursors are at EOF for every session. This is the steady state.

**`loom-shipper: shipped=0 skipped=0 failed=60 total=60`** — everything failed. Check `server_url` (is it reachable? `curl <url>/healthz`), check auth, check the receiver's log.

**`another shipper is running, skipping`** — the flock is held. Either a long-running `once` hasn't finished yet, or a previous process crashed without releasing the lock. The lock releases automatically on process exit; if it's truly stuck, inspect `~/.loom/transport/shipper.lock` and, if no `loom-shipper` process exists, remove the file.

**`launchctl print` says "Could not find service"** — the agent isn't loaded. Re-run `loom-shipper install-agent`, or check `~/Library/LaunchAgents/com.loom.shipper.plist` exists and is valid (`plutil -lint <path>`).

**Shipper runs but nothing appears on the server** — partial lines aren't shipped until the trailing `\n` arrives. If you're watching an active session, the last turn may lag the shipper by one tick. Confirm steady-state by reading the log a few ticks later.
