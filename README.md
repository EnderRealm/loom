# Loom

Loom is a minimal pipeline for capturing, storing, and (eventually) processing agent sessions. See `docs/overview.md` for the project philosophy — this README covers how to build and run what exists today.

## Status

| Subsystem   | State      | What it does                                                |
| ----------- | ---------- | ----------------------------------------------------------- |
| `transport` | v1, usable | Ships agent session JSONL files to a central receiver       |
| everything else | not built yet | Extraction, storage, processing — deliberately deferred |

Transport is the only subsystem today. Anything else in this repo (`extractors/`, `docs/`, `knowledge/*-eval/`) is notes, prompts, or scaffolding. Durable extracted knowledge lives outside the repo at `~/.loom/knowledge/` (its own git repo); see that store's `SCHEMA.md`.

## Prerequisites

- macOS (the shipper uses launchd; Linux support is not planned until there is a real reason)
- Go 1.22 or newer (1.26 tested)

## Repo layout

```
loom/
  go.mod                               # module "loom"
  install.sh                           # install/uninstall/status entry point
  internal/config/                     # loom-wide config (Home, config.json schema)
  transport/
    cmd/
      loom-shipper/                    # client binary
      loom-receiver/                   # server binary
    internal/                          # transport-private packages (wire, cursor, source)
  docs/  extractors/                   # non-Go subsystems
  knowledge/                           # eval fixtures only — durable store is ~/.loom/knowledge/
```

Everything (build + launchd agent management) is driven by `./install.sh`. Run it with no arguments for usage.

```sh
./install.sh                           # help
./install.sh --install-receiver        # server side
./install.sh --install-shipper         # client side
./install.sh --uninstall               # remove both launchd agents
./install.sh --status                  # show state of both agents
```

The script builds to `$LOOM_BIN_DIR` (default `~/.local/bin`, no sudo required), writes state under `$LOOM_HOME` (default `~/.loom`), and manages both launchd agents. Override either via environment:

```sh
LOOM_BIN_DIR=/usr/local/bin ./install.sh --install-shipper
LOOM_HOME=/srv/loom         ./install.sh --install-receiver
```

If you prefer to build manually (e.g., you don't want the launchd agent, or you're running the receiver in a container):

```sh
go build -o ~/.local/bin/loom-shipper  ./transport/cmd/loom-shipper
go build -o ~/.local/bin/loom-receiver ./transport/cmd/loom-receiver
```

> If `~/.local/bin` isn't in your `$PATH`, add it to your shell rc file so you can invoke the binaries by name. launchd runs them by absolute path regardless, so this is purely for interactive use.

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

### 1. Generate and export a shared bearer token

```sh
export LOOM_RECEIVER_TOKEN="$(openssl rand -hex 32)"
```

The same value goes into the client's `~/.loom/config.json` on each shipping machine.

### 2. Install the receiver agent

```sh
./install.sh --install-receiver
```

This:
1. Builds `loom-receiver` to `$LOOM_BIN_DIR/loom-receiver`
2. Creates `$LOOM_HOME/received/`
3. Writes `~/Library/LaunchAgents/com.loom.receiver.plist` with `KeepAlive=true` (restarts on exit) and `RunAtLoad=true` (starts immediately)
4. Bakes `LOOM_RECEIVER_TOKEN` and `LOOM_HOME` into the plist's `EnvironmentVariables`
5. Validates the plist with `plutil -lint`
6. Boots out any prior instance and bootstraps the new one into launchd
7. Polls `http://127.0.0.1:8765/healthz` for up to 5s to confirm it came up

For a remote server, replace `127.0.0.1` with its reachable address when verifying from elsewhere; also open port 8765 or set up a tunnel as needed.

### Receiver flags (for manual runs)

If you'd rather run the receiver manually (e.g., foreground in a tmux for debugging), skip `install.sh --install-receiver` and invoke the binary directly:

```sh
LOOM_RECEIVER_TOKEN=<token> loom-receiver
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

Each machine you want to ship sessions from runs its own `loom-shipper` + config + launchd agent.

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
| `server_url`       | yes      | —       | Base URL of `loom-receiver`; no trailing `/v1/ingest`         |
| `auth_token`       | no       | empty   | Bearer token; must match the server                           |
| `interval_minutes` | no       | `10`    | launchd `StartInterval` = `interval_minutes * 60`             |

Permissions: `chmod 600 ~/.loom/config.json` — it contains the bearer token.

### 2. Install the shipper agent

```sh
./install.sh --install-shipper
```

This:
1. Checks that `$LOOM_HOME/config.json` exists (errors out if not)
2. Builds `loom-shipper` to `$LOOM_BIN_DIR/loom-shipper`
3. Runs `loom-shipper install-agent`, which writes `~/Library/LaunchAgents/com.loom.shipper.plist` (interval baked in, binary path captured via `os.Executable()`), validates it with `plutil -lint`, and bootstraps it into launchd
4. Kickstarts the agent immediately via `launchctl kickstart` so the first run happens now (through launchd, writing to the log file) rather than waiting for the first interval tick
5. Tails the log to show the first run's output
6. Prints the current status so you can confirm it's loaded

### 3. Verify

```sh
./install.sh --status
tail -f ~/.loom/transport/shipper.log
```

First run will walk every session under `~/.claude/projects/` and ship each one in full. Subsequent runs ship only new bytes per session.

---

## Uninstall

```sh
./install.sh --uninstall
```

Removes both launchd agents (tolerant of either being absent) and both plist files. **Preserves** `~/.loom/` state and the installed binaries. If you want a full wipe:

```sh
rm -rf ~/.loom
rm -f ~/.local/bin/loom-shipper ~/.local/bin/loom-receiver
```

---

## Commands reference

**Install script** (`./install.sh`):

| Command                  | What it does                                                          |
| ------------------------ | --------------------------------------------------------------------- |
| `--install-receiver`     | Build loom-receiver, write plist, bootstrap, verify `/healthz`.       |
| `--install-shipper`      | Build loom-shipper, dry-run `once`, run `install-agent`, show status. |
| `--uninstall`            | Remove both agents. Preserves state and binaries.                     |
| `--status`               | Show launchd state for both agents + config presence.                 |
| `--help` (or no args)    | Show usage.                                                           |

**Shipper binary** (`loom-shipper`), for when you need fine control:

| Command                        | What it does                                                         |
| ------------------------------ | -------------------------------------------------------------------- |
| `loom-shipper once`            | Single pass. Idempotent; reentrant; safe to run by hand any time.    |
| `loom-shipper install-agent`   | Generate plist, lint, bootstrap into launchd.                        |
| `loom-shipper uninstall-agent` | Bootout and remove the plist. Tolerates "not loaded".                |
| `loom-shipper status`          | `launchctl print` for the agent.                                     |

---

## Updates

The key design fact: **the launchd plist pins an absolute binary path at install time**. If you rebuild to the same path, the next scheduled tick runs the new binary — no reinstall needed.

### Simple updates (code change only)

Easiest path — just re-run the install script. Both commands are idempotent and bootstrap-safe:

```sh
cd ~/code/loom
git pull
./install.sh --install-shipper     # on each client machine
./install.sh --install-receiver    # on the server machine (needs LOOM_RECEIVER_TOKEN in env)
```

Alternatively, if nothing structural changed and you only want to swap the binary without touching the plist, rebuild in place:

```sh
cd ~/code/loom
git pull
go build -o ~/.local/bin/loom-shipper  ./transport/cmd/loom-shipper   # next tick runs it
go build -o ~/.local/bin/loom-receiver ./transport/cmd/loom-receiver  # needs a restart
launchctl kickstart -k gui/$(id -u)/com.loom.receiver                 # if running under launchd
```

### When a reinstall is required

Re-run `./install.sh --install-shipper` (or `--install-receiver`) when any of these change:

- **Binary path** — you moved `LOOM_BIN_DIR` (the plist pins the absolute path)
- **`interval_minutes`** in config — the interval is baked into the shipper plist
- **`LOOM_HOME`** — if non-default, it's baked into `EnvironmentVariables` on both plists
- **`LOOM_RECEIVER_TOKEN`** — baked into the receiver plist at install time

`install-agent` (and the install script) boot out any prior instance with the same label before bootstrapping the new plist, so reinstall is always safe.

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

First stop for any issue: `./install.sh --status`. It reports which agents are loaded, whether the binaries and config exist, and prints key launchctl fields.

**`config not found at ~/.loom/config.json`** — client hasn't been configured. Create the file (see "Client setup" step 1), then re-run `./install.sh --install-shipper`.

**`LOOM_RECEIVER_TOKEN is not set`** (from `install.sh --install-receiver`) — export the token in your shell first: `export LOOM_RECEIVER_TOKEN="$(openssl rand -hex 32)"`, then re-run the install.

**All POSTs return 401** — token mismatch. Check that `LOOM_RECEIVER_TOKEN` on the server matches `auth_token` in `~/.loom/config.json` on the client. A token change on the server requires `./install.sh --install-receiver` to rewrite the plist.

**`loom-shipper: shipped=0 skipped=60 failed=0 total=60`** — nothing to ship. All cursors are at EOF for every session. This is the steady state.

**`loom-shipper: shipped=0 skipped=0 failed=60 total=60`** — everything failed. Check `server_url` (is it reachable? `curl <url>/healthz`), check auth, and check the receiver's log at `~/.loom/receiver.log`.

**`another shipper is running, skipping`** — the flock is held. Either a long-running `once` hasn't finished yet, or a previous process crashed without releasing the lock. The lock releases automatically on process exit; if it's truly stuck, inspect `~/.loom/transport/shipper.lock` and, if no `loom-shipper` process exists, remove the file.

**`launchctl print` says "Could not find service"** — the agent isn't loaded. Re-run `./install.sh --install-shipper` (or `--install-receiver`), or check the relevant plist exists under `~/Library/LaunchAgents/` and is valid (`plutil -lint <path>`).

**Shipper runs but nothing appears on the server** — partial lines aren't shipped until the trailing `\n` arrives. If you're watching an active session, the last turn may lag the shipper by one tick. Confirm steady-state by reading the log a few ticks later.
