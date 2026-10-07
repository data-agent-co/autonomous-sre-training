#!/usr/bin/env bash
# Start or stop the Grafana port-forward for the demo cluster.
#
#   ./scripts/grafana.sh             start (replaces one this script started)
#   ./scripts/grafana.sh --stop      stop
#
# The port-forward runs in the background; its PID file and log live in
# demo-cluster/.run/<cluster>/. up.sh starts it and down.sh stops it. A
# port-forward dies with its pod or cluster (reset, Docker restart), so run
# this again to get Grafana back.
#
#   GRAFANA_PORT=3000   local port; pick another one for a second cluster
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "$SCRIPT_DIR/lib.sh"

NS="monitoring"
SVC="prometheus-stack-grafana"
PORT="${GRAFANA_PORT:-3000}"
LOG="$STATE_DIR/grafana-pf.log"

stop() {
  if stop_pidfile "$GRAFANA_PF_PIDFILE" "$GRAFANA_PF_MATCH"; then
    echo "Grafana port-forward stopped"
  else
    echo "No Grafana port-forward running for '$CLUSTER_NAME'"
  fi
}

start() {
  require_demo_cluster
  # Only ever the port-forward this script started (see lib.sh), never one
  # of yours.
  stop_pidfile "$GRAFANA_PF_PIDFILE" "$GRAFANA_PF_MATCH" || true

  if ! demo_kubectl -n "$NS" get svc "$SVC" >/dev/null 2>&1; then
    die "Grafana service $NS/$SVC not found. Is kube-prometheus-stack installed? Run ${RUN_PREFIX}./scripts/up.sh."
  fi

  # Refuse a port something else already listens on (curl exit code 7 means
  # nothing did). Give a port-forward stopped just above a moment to let go.
  local rc
  for _ in 1 2 3 4 5 6; do
    rc=0
    curl -s -o /dev/null --max-time 2 "http://127.0.0.1:${PORT}/" || rc=$?
    [[ $rc -eq 7 ]] && break
    sleep 0.5
  done
  if [[ $rc -ne 7 ]]; then
    die "port $PORT is already in use. Pick a free one: ${RUN_PREFIX}GRAFANA_PORT=<port> $0"
  fi

  mkdir -p "$STATE_DIR"
  # The command line must keep containing $GRAFANA_PF_MATCH.
  nohup kubectl --context "$KUBE_CONTEXT" -n "$NS" port-forward "svc/$SVC" "$PORT:80" \
    >"$LOG" 2>&1 </dev/null &
  local pid=$!
  echo "$pid" >"$GRAFANA_PF_PIDFILE"
  disown "$pid" 2>/dev/null || true

  # Wait for the listener: kubectl logs "Forwarding from" once it listens.
  # Until then, whatever else might grab the port could answer the probe.
  local code
  for _ in $(seq 1 20); do
    if ! kill -0 "$pid" 2>/dev/null; then
      rm -f "$GRAFANA_PF_PIDFILE"
      echo "ERROR: the port-forward exited. Is port $PORT in use? Try ${RUN_PREFIX}GRAFANA_PORT=<free port> $0" >&2
      tail -5 "$LOG" >&2 || true
      exit 1
    fi
    code=""
    if grep -q "Forwarding from" "$LOG" 2>/dev/null; then
      code="$(curl -sS -o /dev/null -w '%{http_code}' "http://127.0.0.1:${PORT}/login" 2>/dev/null || true)"
    fi
    if [[ "$code" =~ ^(200|302)$ ]]; then
      echo "Grafana is up at http://localhost:${PORT}  (admin / admin)"
      echo "  Dashboard: http://localhost:${PORT}/d/demo-cluster-overview"
      echo "  Stop with: ${RUN_PREFIX}$0 --stop"
      exit 0
    fi
    sleep 0.5
  done

  stop_pidfile "$GRAFANA_PF_PIDFILE" "$GRAFANA_PF_MATCH" || true
  echo "ERROR: Grafana not responding on port ${PORT} after 10s. Log $LOG:" >&2
  tail -10 "$LOG" >&2 || true
  exit 1
}

case "${1:-start}" in
  --stop|stop) stop ;;
  start)       start ;;
  *)           echo "usage: $0 [start|--stop]" >&2; exit 2 ;;
esac
