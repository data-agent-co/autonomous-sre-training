#!/usr/bin/env bash
# Start or stop the Perses port-forward for the demo cluster.
#
#   ./scripts/perses.sh              start (replaces one this script started)
#   ./scripts/perses.sh --stop       stop
#
# The port-forward runs in the background; its PID file and log live in
# demo-cluster/.run/<cluster>/. up.sh starts it and down.sh stops it. A
# port-forward dies with its pod or cluster (reset, Docker restart), so run
# this again to get Perses back.
#
#   PERSES_PORT=8081    local port; pick another one for a second cluster
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "$SCRIPT_DIR/lib.sh"

PF_NAME="Perses"
PF_SVC="perses"
PF_SVC_PORT=8080
PF_PORT="${PERSES_PORT:-$PERSES_PORT_DEFAULT}"
PF_PORT_VAR="PERSES_PORT"
PF_HEALTH_PATH="/api/v1/health"
PF_PAGE_PATH="/projects/demo-cluster/dashboards/demo-cluster-overview"
PF_PIDFILE="$PERSES_PF_PIDFILE"
PF_MATCH="$PERSES_PF_MATCH"
PF_LOG="$STATE_DIR/perses-pf.log"

port_forward_main "$@"
