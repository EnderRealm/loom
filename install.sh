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

SHIPPER_LABEL="com.loom.shipper"
RECEIVER_LABEL="com.loom.receiver"

LAUNCH_AGENTS_DIR="$HOME/Library/LaunchAgents"
SHIPPER_PLIST="$LAUNCH_AGENTS_DIR/$SHIPPER_LABEL.plist"
RECEIVER_PLIST="$LAUNCH_AGENTS_DIR/$RECEIVER_LABEL.plist"

RECEIVER_LOG="$LOOM_HOME/receiver.log"

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
  install.sh --install-receiver   Build loom-receiver, install launchd agent, verify health
  install.sh --install-shipper    Build loom-shipper, install launchd agent, verify status
  install.sh --uninstall          Remove both launchd agents (preserves state + binaries)
  install.sh --status             Show launchd state for both agents and basic config
  install.sh --help               Show this message

environment:
  LOOM_HOME=$LOOM_HOME
  LOOM_BIN_DIR=$LOOM_BIN_DIR

before --install-receiver:
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

# ---------- build ----------

build_shipper() {
    log "building loom-shipper → $SHIPPER_BIN"
    ( cd "$REPO_ROOT" && go build -o "$SHIPPER_BIN" ./transport/cmd/loom-shipper )
}

build_receiver() {
    log "building loom-receiver → $RECEIVER_BIN"
    ( cd "$REPO_ROOT" && go build -o "$RECEIVER_BIN" ./transport/cmd/loom-receiver )
}

# ---------- install shipper ----------

install_shipper() {
    check_prereqs
    ensure_dirs

    if [[ ! -f "$LOOM_HOME/config.json" ]]; then
        err "$LOOM_HOME/config.json not found — create it with server_url, auth_token, interval_minutes (see README.md)"
    fi

    build_shipper

    log "dry check: loom-shipper once"
    if ! "$SHIPPER_BIN" once; then
        warn "dry check returned non-zero — inspect output above and the receiver log"
    fi

    log "installing launchd agent"
    "$SHIPPER_BIN" install-agent

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

    log "receiver installed; verifying health..."
    local addr="http://127.0.0.1:8765/healthz"
    local ok=0
    for i in 1 2 3 4 5; do
        if curl -sS --max-time 1 "$addr" >/dev/null 2>&1; then
            ok=1
            break
        fi
        sleep 1
    done
    if [[ $ok -eq 1 ]]; then
        log "healthz ok at $addr"
    else
        warn "healthz did not respond at $addr within 5s — check: tail $RECEIVER_LOG"
    fi
}

# ---------- uninstall (both) ----------

uninstall() {
    log "uninstalling loom launchd agents"

    # Shipper: prefer the binary's own uninstall-agent (handles the plist
    # tolerantly), fall back to manual cleanup if the binary is gone.
    if [[ -x "$SHIPPER_BIN" ]]; then
        "$SHIPPER_BIN" uninstall-agent || true
    else
        launchctl bootout "gui/$(id -u)/$SHIPPER_LABEL" 2>/dev/null || true
        rm -f "$SHIPPER_PLIST"
    fi
    log "shipper agent removed (if present)"

    # Receiver: managed entirely by this script.
    launchctl bootout "gui/$(id -u)/$RECEIVER_LABEL" 2>/dev/null || true
    rm -f "$RECEIVER_PLIST"
    log "receiver agent removed (if present)"

    log "state preserved at $LOOM_HOME"
    log "binaries preserved at $SHIPPER_BIN, $RECEIVER_BIN"
    log "remove those manually if you want a full wipe"
}

# ---------- status ----------

status() {
    local uid
    uid="$(id -u)"

    echo "=== loom-receiver ==="
    if launchctl print "gui/$uid/$RECEIVER_LABEL" >/dev/null 2>&1; then
        launchctl print "gui/$uid/$RECEIVER_LABEL" | awk '
            /^[[:space:]]*state[[:space:]]*=/ { print "  " $0 }
            /^[[:space:]]*pid[[:space:]]*=/ { print "  " $0 }
            /^[[:space:]]*program[[:space:]]*=/ { print "  " $0 }
            /^[[:space:]]*last exit code[[:space:]]*=/ { print "  " $0 }
        '
        if [[ -f "$RECEIVER_PLIST" ]]; then
            echo "  plist: $RECEIVER_PLIST"
        fi
    else
        echo "  not loaded"
    fi

    echo
    echo "=== loom-shipper ==="
    if [[ -x "$SHIPPER_BIN" ]] && launchctl print "gui/$uid/$SHIPPER_LABEL" >/dev/null 2>&1; then
        launchctl print "gui/$uid/$SHIPPER_LABEL" | awk '
            /^[[:space:]]*state[[:space:]]*=/ { print "  " $0 }
            /^[[:space:]]*last exit code[[:space:]]*=/ { print "  " $0 }
            /^[[:space:]]*run interval[[:space:]]*=/ { print "  " $0 }
            /^[[:space:]]*program[[:space:]]*=/ { print "  " $0 }
        '
        if [[ -f "$SHIPPER_PLIST" ]]; then
            echo "  plist: $SHIPPER_PLIST"
        fi
    else
        echo "  not loaded"
    fi

    echo
    echo "=== config ==="
    echo "  LOOM_HOME=$LOOM_HOME"
    echo "  LOOM_BIN_DIR=$LOOM_BIN_DIR"
    if [[ -f "$LOOM_HOME/config.json" ]]; then
        echo "  config.json: present"
    else
        echo "  config.json: missing ($LOOM_HOME/config.json)"
    fi
    if [[ -x "$SHIPPER_BIN" ]]; then
        echo "  shipper binary: $SHIPPER_BIN"
    else
        echo "  shipper binary: not built"
    fi
    if [[ -x "$RECEIVER_BIN" ]]; then
        echo "  receiver binary: $RECEIVER_BIN"
    else
        echo "  receiver binary: not built"
    fi
}

# ---------- main ----------

main() {
    if [[ $# -eq 0 ]]; then
        usage
        exit 0
    fi

    case "$1" in
        --install-receiver) install_receiver ;;
        --install-shipper)  install_shipper ;;
        --uninstall)        uninstall ;;
        --status)           status ;;
        --help|-h)          usage ;;
        *)
            echo "unknown argument: $1" >&2
            echo
            usage
            exit 2
            ;;
    esac
}

main "$@"
