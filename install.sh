#!/usr/bin/env bash
#
# install.sh - transitional forwarder over `go install ./cmd/loom`
#              + `loom install <component>`. Use the loom binary directly
#              for new work; this script is kept for muscle-memory and
#              will be removed once everyone is on `loom install`.
#
# Configuration via environment:
#   LOOM_BIN_DIR    where the loom binary is installed (default: ~/.local/bin)
#   LOOM_HOME       state root (default: ~/.loom)
#   LOOM_RECEIVER_TOKEN   receiver bearer token; seeds ~/.loom/receiver-token on
#                         first --install-receiver / --install-server. Once
#                         persisted (or set interactively) it's no longer needed.
#
set -euo pipefail

LOOM_BIN_DIR="${LOOM_BIN_DIR:-$HOME/.local/bin}"
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

usage() {
    cat <<EOF
install.sh - forwarder for 'loom install <component>'

usage:
  install.sh --install-server      Build loom and install receiver + summarizer
  install.sh --install-receiver    Build loom and install the receiver agent
  install.sh --install-summarizer  Build loom and install the summarizer agent
  install.sh --install-shipper     Build loom and install the shipper agent
  install.sh --uninstall           Remove all loom launchd agents
  install.sh --status              Show launchd state for installed components
  install.sh --help                Show this message

modern equivalent:
  go install loom/cmd/loom        (or 'go build -o $LOOM_BIN_DIR/loom ./cmd/loom')
  loom install <component>
EOF
}

build() {
    mkdir -p "$LOOM_BIN_DIR"
    ( cd "$REPO_ROOT" && go build -o "$LOOM_BIN_DIR/loom" ./cmd/loom )
}

case "${1:-}" in
    --install-server)
        build
        "$LOOM_BIN_DIR/loom" install server
        ;;
    --install-receiver)
        build
        "$LOOM_BIN_DIR/loom" install receiver
        ;;
    --install-summarizer)
        build
        "$LOOM_BIN_DIR/loom" install summarizer
        ;;
    --install-shipper)
        build
        "$LOOM_BIN_DIR/loom" install shipper
        ;;
    --install-tui)
        build
        echo "the TUI is built into the loom binary; run: $LOOM_BIN_DIR/loom ui"
        ;;
    --uninstall)
        if [[ -x "$LOOM_BIN_DIR/loom" ]]; then
            "$LOOM_BIN_DIR/loom" uninstall
        else
            build
            "$LOOM_BIN_DIR/loom" uninstall
        fi
        ;;
    --status)
        if [[ -x "$LOOM_BIN_DIR/loom" ]]; then
            "$LOOM_BIN_DIR/loom" status
        else
            build
            "$LOOM_BIN_DIR/loom" status
        fi
        ;;
    --help|-h)
        usage
        ;;
    "")
        usage
        ;;
    *)
        echo "unknown argument: $1" >&2
        echo
        usage
        exit 2
        ;;
esac
