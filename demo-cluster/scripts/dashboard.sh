#!/usr/bin/env bash
# Start or stop the demo dashboard port-forward for the demo cluster.
#
#   ./scripts/dashboard.sh           start (replaces one this script started)
#   ./scripts/dashboard.sh --stop    stop
#
# The port-forward runs in the background; its PID file and log live in
# demo-cluster/.run/<cluster>/. up.sh starts it and down.sh stops it. A
# port-forward dies with its pod or cluster (reset, Docker restart), so run
# this again to get the dashboard back.
#
# Perses was on port 8080 before this dashboard. Starting on 8080 also stops
# a Perses port-forward of this cluster that is still there; perses.sh
# starts it again on its own port.
#
#   DASHBOARD_PORT=8080    local port; pick another one for a second cluster
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "$SCRIPT_DIR/lib.sh"

PF_NAME="The demo dashboard"
PF_SVC="demo-dashboard"
PF_SVC_PORT=8080
PF_PORT="${DASHBOARD_PORT:-$DASHBOARD_PORT_DEFAULT}"
PF_PORT_VAR="DASHBOARD_PORT"
PF_HEALTH_PATH="/healthz"
PF_PAGE_PATH=""
PF_PIDFILE="$DASHBOARD_PF_PIDFILE"
PF_MATCH="$DASHBOARD_PF_MATCH"
PF_LOG="$STATE_DIR/dashboard-pf.log"

port_forward_main "$@"
