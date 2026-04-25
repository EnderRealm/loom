#!/usr/bin/env bash
#
# loom install script — one entry point for building + managing the launchd
# agents on both sides of the transport. Idempotent: re-running any install
# command is safe and picks up the latest binary.
#
# Configuration via environment:
#   LOOM_HOME       state root (default: ~/.loom)
#   LOOM_BIN_DIR    where binaries are installed (default: ~/.local/bin)
#   LOOM_RECEIVER_TOKEN   required for --install-receiver
#
set -euo pipefail

# ---------- paths and labels ----------

LOOM_HOME="${LOOM_HOME:-$HOME/.loom}"
LOOM_BIN_DIR="${LOOM_BIN_DIR:-$HOME/.local/bin}"

SHIPPER_BIN="$LOOM_BIN_DIR/loom-shipper"
RECEIVER_BIN="$LOOM_BIN_DIR/loom-receiver"
SUMMARIZER_BIN="$LOOM_BIN_DIR/loom-summarize"
TUI_BIN="$LOOM_BIN_DIR/loom-tui"

SHIPPER_LABEL="com.loom.shipper"
RECEIVER_LABEL="com.loom.receiver"
SUMMARIZER_LABEL="com.loom.summarizer"

LAUNCH_AGENTS_DIR="$HOME/Library/LaunchAgents"
SHIPPER_PLIST="$LAUNCH_AGENTS_DIR/$SHIPPER_LABEL.plist"
RECEIVER_PLIST="$LAUNCH_AGENTS_DIR/$RECEIVER_LABEL.plist"
SUMMARIZER_PLIST="$LAUNCH_AGENTS_DIR/$SUMMARIZER_LABEL.plist"

RECEIVER_LOG="$LOOM_HOME/receiver.log"
SUMMARIZER_LOG="$LOOM_HOME/summarizer.log"
SUMMARY_DB="$LOOM_HOME/summaries.db"

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# ---------- output helpers ----------

log()  { echo "[loom-install] $*"; }
warn() { echo "[loom-install] warn: $*" >&2; }
err()  { echo "[loom-install] error: $*" >&2; exit 1; }

# ---------- usage ----------

usage() {
    cat <<EOF
loom install script

usage:
  install.sh --install-server      Build + install all server-side agents (receiver + summarizer + tui)
  install.sh --install-receiver    Build loom-receiver, install launchd agent, verify health
  install.sh --install-summarizer  Build loom-summarize, install launchd agent in watch mode
  install.sh --install-shipper     Build loom-shipper, install launchd agent, verify status
  install.sh --install-tui         Build loom-tui to $LOOM_BIN_DIR (no launchd; runs interactively)
  install.sh --uninstall           Remove all loom launchd agents (preserves state + binaries)
  install.sh --status              Show launchd state for installed components
  install.sh --help                Show this message

environment:
  LOOM_HOME=$LOOM_HOME
  LOOM_BIN_DIR=$LOOM_BIN_DIR

before --install-server / --install-receiver:
  export LOOM_RECEIVER_TOKEN="\$(openssl rand -hex 32)"

before --install-shipper:
  Create $LOOM_HOME/config.json with server_url, auth_token, interval_minutes.
  See README.md for the schema.
EOF
}

# ---------- prereq checks ----------

check_prereqs() {
    [[ "$(uname -s)" == "Darwin" ]] || err "loom is macOS-only"
    command -v go >/dev/null 2>&1 || err "go not found in PATH — install Go 1.22+"
    command -v launchctl >/dev/null 2>&1 || err "launchctl not found — are you on macOS?"
    command -v plutil >/dev/null 2>&1 || err "plutil not found — are you on macOS?"
}

ensure_dirs() {
    mkdir -p "$LOOM_BIN_DIR"
    mkdir -p "$LOOM_HOME"
    mkdir -p "$LAUNCH_AGENTS_DIR"
}

# ---------- component installed-state probes ----------
#
# A component is "installed" when its launchd plist exists. Used to make
# --status and --uninstall behave conditionally.

shipper_installed()    { [[ -f "$SHIPPER_PLIST" ]]; }
receiver_installed()   { [[ -f "$RECEIVER_PLIST" ]]; }
summarizer_installed() { [[ -f "$SUMMARIZER_PLIST" ]]; }

# ---------- build ----------

build_shipper() {
    log "building loom-shipper → $SHIPPER_BIN"
    ( cd "$REPO_ROOT" && go build -o "$SHIPPER_BIN" ./transport/cmd/loom-shipper )
}

build_receiver() {
    log "building loom-receiver → $RECEIVER_BIN"
    ( cd "$REPO_ROOT" && go build -o "$RECEIVER_BIN" ./transport/cmd/loom-receiver )
}

build_summarizer() {
    log "building loom-summarize → $SUMMARIZER_BIN"
    ( cd "$REPO_ROOT" && go build -o "$SUMMARIZER_BIN" ./cmd/loom-summarize )
}

build_tui() {
    log "building loom-tui → $TUI_BIN"
    ( cd "$REPO_ROOT" && go build -o "$TUI_BIN" ./cmd/loom-tui )
}

# install_tui builds the interactive dashboard binary into $LOOM_BIN_DIR.
# No launchd agent — this is a foreground tool the user runs themselves.
install_tui() {
    check_prereqs
    ensure_dirs
    build_tui
    log "loom-tui installed → $TUI_BIN"
    log "  run it: $TUI_BIN"
}

# ---------- install shipper ----------

install_shipper() {
    check_prereqs
    ensure_dirs

    if [[ ! -f "$LOOM_HOME/config.json" ]]; then
        err "$LOOM_HOME/config.json not found — create it with server_url, auth_token, interval_minutes (see README.md)"
    fi

    build_shipper

    log "installing launchd agent"
    "$SHIPPER_BIN" install-agent

    log "kickstarting first run"
    launchctl kickstart "gui/$(id -u)/$SHIPPER_LABEL" 2>/dev/null || true
    sleep 2

    if [[ -f "$LOOM_HOME/transport/shipper.log" ]]; then
        log "first run output:"
        tail -5 "$LOOM_HOME/transport/shipper.log"
    else
        warn "no log output yet — check: tail $LOOM_HOME/transport/shipper.log"
    fi

    log "shipper installed; current status:"
    "$SHIPPER_BIN" status 2>&1 | head -20 || true
}

# ---------- install receiver ----------

install_receiver() {
    check_prereqs
    ensure_dirs

    if [[ -z "${LOOM_RECEIVER_TOKEN:-}" ]]; then
        err "LOOM_RECEIVER_TOKEN is not set. Generate one: export LOOM_RECEIVER_TOKEN=\"\$(openssl rand -hex 32)\""
    fi

    build_receiver

    mkdir -p "$LOOM_HOME/received"

    log "writing plist: $RECEIVER_PLIST"
    cat > "$RECEIVER_PLIST" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>${RECEIVER_LABEL}</string>
    <key>ProgramArguments</key>
    <array>
        <string>${RECEIVER_BIN}</string>
    </array>
    <key>EnvironmentVariables</key>
    <dict>
        <key>LOOM_RECEIVER_TOKEN</key>
        <string>${LOOM_RECEIVER_TOKEN}</string>
        <key>LOOM_HOME</key>
        <string>${LOOM_HOME}</string>
    </dict>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>${RECEIVER_LOG}</string>
    <key>StandardErrorPath</key>
    <string>${RECEIVER_LOG}</string>
</dict>
</plist>
EOF

    if ! plutil -lint "$RECEIVER_PLIST" >/dev/null; then
        rm -f "$RECEIVER_PLIST"
        err "plutil -lint rejected the generated plist"
    fi

    # Idempotency: bootout any prior instance before bootstrapping.
    launchctl bootout "gui/$(id -u)/$RECEIVER_LABEL" 2>/dev/null || true
    if ! launchctl bootstrap "gui/$(id -u)" "$RECEIVER_PLIST"; then
        err "launchctl bootstrap failed for $RECEIVER_LABEL"
    fi

    # RunAtLoad alone can leave the job speculative; kickstart forces first run.
    launchctl kickstart -p "gui/$(id -u)/$RECEIVER_LABEL" >/dev/null 2>&1 || true

    log "receiver installed; verifying health..."
    local addr="http://127.0.0.1:8765/healthz"
    local ok=0
    for i in 1 2 3 4 5 6 7 8 9 10; do
        if curl -sS --max-time 1 "$addr" >/dev/null 2>&1; then
            ok=1
            break
        fi
        sleep 1
    done
    if [[ $ok -eq 1 ]]; then
        log "healthz ok at $addr"
    else
        warn "healthz did not respond at $addr within 10s — check: tail $RECEIVER_LOG"
    fi
}

# ---------- install summarizer ----------

install_summarizer() {
    check_prereqs
    ensure_dirs

    build_summarizer

    log "writing plist: $SUMMARIZER_PLIST"
    # Run in -watch mode: cold-start sweep handles catch-up, then a 30s
    # ticker keeps the DB current. KeepAlive restarts on crash.
    cat > "$SUMMARIZER_PLIST" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>${SUMMARIZER_LABEL}</string>
    <key>ProgramArguments</key>
    <array>
        <string>${SUMMARIZER_BIN}</string>
        <string>-watch</string>
        <string>-received</string>
        <string>${LOOM_HOME}/received</string>
        <string>-db</string>
        <string>${SUMMARY_DB}</string>
    </array>
    <key>EnvironmentVariables</key>
    <dict>
        <key>LOOM_HOME</key>
        <string>${LOOM_HOME}</string>
    </dict>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>ProcessType</key>
    <string>Background</string>
    <key>LowPriorityIO</key>
    <true/>
    <key>Nice</key>
    <integer>10</integer>
    <key>StandardOutPath</key>
    <string>${SUMMARIZER_LOG}</string>
    <key>StandardErrorPath</key>
    <string>${SUMMARIZER_LOG}</string>
</dict>
</plist>
EOF

    if ! plutil -lint "$SUMMARIZER_PLIST" >/dev/null; then
        rm -f "$SUMMARIZER_PLIST"
        err "plutil -lint rejected the generated plist"
    fi

    launchctl bootout "gui/$(id -u)/$SUMMARIZER_LABEL" 2>/dev/null || true
    if ! launchctl bootstrap "gui/$(id -u)" "$SUMMARIZER_PLIST"; then
        err "launchctl bootstrap failed for $SUMMARIZER_LABEL"
    fi
    launchctl kickstart -p "gui/$(id -u)/$SUMMARIZER_LABEL" >/dev/null 2>&1 || true

    log "summarizer installed; first sweep is running in the background"
    log "  log:  $SUMMARIZER_LOG"
    log "  db:   $SUMMARY_DB"
}

# ---------- install server (receiver + summarizer) ----------

install_server() {
    install_receiver
    echo
    install_summarizer
    echo
    install_tui
}

# ---------- uninstall (all) ----------

uninstall() {
    log "uninstalling loom launchd agents"

    # Shipper: prefer the binary's own uninstall-agent (handles the plist
    # tolerantly), fall back to manual cleanup if the binary is gone.
    if shipper_installed; then
        if [[ -x "$SHIPPER_BIN" ]]; then
            "$SHIPPER_BIN" uninstall-agent || true
        else
            launchctl bootout "gui/$(id -u)/$SHIPPER_LABEL" 2>/dev/null || true
            rm -f "$SHIPPER_PLIST"
        fi
        log "shipper agent removed"
    fi

    if receiver_installed; then
        launchctl bootout "gui/$(id -u)/$RECEIVER_LABEL" 2>/dev/null || true
        rm -f "$RECEIVER_PLIST"
        log "receiver agent removed"
    fi

    if summarizer_installed; then
        launchctl bootout "gui/$(id -u)/$SUMMARIZER_LABEL" 2>/dev/null || true
        rm -f "$SUMMARIZER_PLIST"
        log "summarizer agent removed"
    fi

    log "state preserved at $LOOM_HOME"
    log "binaries preserved in $LOOM_BIN_DIR — remove manually for a full wipe"
}

# ---------- status ----------

# print_launchd_block prints a block of fields from `launchctl print` for
# one label. Used by the per-component status sections.
print_launchd_block() {
    local label="$1"
    local uid
    uid="$(id -u)"
    if launchctl print "gui/$uid/$label" >/dev/null 2>&1; then
        launchctl print "gui/$uid/$label" | awk '
            /^[[:space:]]*state[[:space:]]*=/         { print "  " $0 }
            /^[[:space:]]*pid[[:space:]]*=/           { print "  " $0 }
            /^[[:space:]]*program[[:space:]]*=/       { print "  " $0 }
            /^[[:space:]]*last exit code[[:space:]]*=/ { print "  " $0 }
            /^[[:space:]]*run interval[[:space:]]*=/  { print "  " $0 }
        '
    else
        echo "  installed but not loaded — try: launchctl bootstrap gui/$uid \$plist"
    fi
}

status() {
    local printed_any=0

    if receiver_installed; then
        printed_any=1
        echo "=== loom-receiver ==="
        print_launchd_block "$RECEIVER_LABEL"
        echo "  plist: $RECEIVER_PLIST"
        echo "  log:   $RECEIVER_LOG"
        echo
    fi

    if shipper_installed; then
        printed_any=1
        echo "=== loom-shipper ==="
        print_launchd_block "$SHIPPER_LABEL"
        if [[ -f "$SHIPPER_PLIST" ]]; then
            echo "  plist: $SHIPPER_PLIST"
        fi
        echo
        echo "=== sync health ==="
        if [[ -x "$SHIPPER_BIN" ]]; then
            LOOM_HOME="$LOOM_HOME" "$SHIPPER_BIN" health 2>/dev/null \
                || echo "  (shipper has not run yet — no notify.state)"
        else
            echo "  shipper binary not built"
        fi
        echo
    fi

    if summarizer_installed; then
        printed_any=1
        echo "=== loom-summarizer ==="
        print_launchd_block "$SUMMARIZER_LABEL"
        echo "  plist: $SUMMARIZER_PLIST"
        echo "  log:   $SUMMARIZER_LOG"
        if [[ -f "$SUMMARY_DB" ]]; then
            local size
            size="$(du -h "$SUMMARY_DB" | awk '{print $1}')"
            echo "  db:    $SUMMARY_DB ($size)"
            if command -v sqlite3 >/dev/null 2>&1; then
                local sessions tools errors unknown
                sessions="$(sqlite3 "$SUMMARY_DB" 'SELECT COUNT(*) FROM sessions' 2>/dev/null || echo "?")"
                tools="$(sqlite3 "$SUMMARY_DB" 'SELECT COUNT(*) FROM tool_calls' 2>/dev/null || echo "?")"
                errors="$(sqlite3 "$SUMMARY_DB" 'SELECT COUNT(*) FROM errors' 2>/dev/null || echo "?")"
                unknown="$(sqlite3 "$SUMMARY_DB" 'SELECT COUNT(*) FROM unknown_records' 2>/dev/null || echo "?")"
                echo "  sessions=$sessions tool_calls=$tools errors=$errors unknown_records=$unknown"
            fi
        else
            echo "  db:    not yet created (first sweep may still be running)"
        fi
        echo
    fi

    if [[ $printed_any -eq 0 ]]; then
        echo "no loom components installed"
        echo "  install with one of:"
        echo "    install.sh --install-server"
        echo "    install.sh --install-shipper"
        echo
    fi

    echo "=== config ==="
    echo "  LOOM_HOME=$LOOM_HOME"
    echo "  LOOM_BIN_DIR=$LOOM_BIN_DIR"
    if [[ -f "$LOOM_HOME/config.json" ]]; then
        echo "  config.json: present"
    elif shipper_installed; then
        echo "  config.json: missing ($LOOM_HOME/config.json)"
    fi
}

# ---------- main ----------

main() {
    if [[ $# -eq 0 ]]; then
        usage
        exit 0
    fi

    case "$1" in
        --install-server)     install_server ;;
        --install-receiver)   install_receiver ;;
        --install-summarizer) install_summarizer ;;
        --install-shipper)    install_shipper ;;
        --install-tui)        install_tui ;;
        --uninstall)          uninstall ;;
        --status)             status ;;
        --help|-h)            usage ;;
        *)
            echo "unknown argument: $1" >&2
            echo
            usage
            exit 2
            ;;
    esac
}

main "$@"
