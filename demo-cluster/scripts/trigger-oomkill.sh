#!/usr/bin/env bash
# Trigger OOMKills on the RUNNING payments-api pod by allocating more than
# its memory limit.
#
# A background loop calls payments-api's /allocate endpoint from inside the
# running container, so the kernel OOMKills it in place. The kubelet
# restarts the container in the same pod, so the restart counter climbs on
# the *serving* pod. During each restart window the Service has no healthy
# endpoint, so Prometheus `up` drops to 0 and the error-rate and composite
# health panels react. After a few restarts the pod enters CrashLoopBackOff,
# which K8sGPT's Pod analyzer reports.
#
# (Setting an allocation on the Deployment instead would roll out a new pod
# that OOMs on boot while the old one keeps serving, so nothing visible
# would break.)
#
# Usage:
#   ./trigger-oomkill.sh           start (idempotent)
#   ./trigger-oomkill.sh --stop    stop the loop
#
#   OOM_ALLOC_MB=200   MiB to allocate per call (the limit is 128Mi)
#   OOM_INTERVAL=1     seconds between checks for a running container
#
# The loop's PID file and log live in demo-cluster/.run/<cluster>/;
# down.sh stops it too.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "$SCRIPT_DIR/lib.sh"

NS="demo-apps"
DEPLOY="payments-api"
ALLOC_MB="${OOM_ALLOC_MB:-200}"
INTERVAL="${OOM_INTERVAL:-1}"
LOG="$STATE_DIR/trigger-oomkill.log"

# Re-kills the container the moment the kubelet brings it back, so it never
# serves long enough to look healthy. K8sGPT scans about every 30s and
# deletes its Result for a pod it finds Running, then creates a new one on
# the next crash (and with Slack on, each new Result is a new post). A loop
# that left the container up ~10s per cycle got the same diagnosis posted
# four times. `kubectl exec` with the image's busybox wget fires within ~1s.
oom_loop() {
  trap 'exit 0' TERM INT
  while true; do
    # Stop on our own once the cluster is gone (down.sh normally stops us).
    command kubectl config get-contexts "$KUBE_CONTEXT" >/dev/null 2>&1 || exit 0
    POD=$(demo_kubectl -n "$NS" get pod -l "app=$DEPLOY" \
            -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)
    RUNNING=""
    if [[ -n "$POD" ]]; then
      RUNNING=$(demo_kubectl -n "$NS" get pod "$POD" \
                  -o jsonpath='{.status.containerStatuses[0].state.running.startedAt}' 2>/dev/null || true)
    fi
    if [[ -n "$RUNNING" ]]; then
      # Blocks until the kernel kills the container, then returns non-zero.
      demo_kubectl -n "$NS" exec "$POD" -- \
        wget -q -O /dev/null -T 4 "http://127.0.0.1:8080/allocate?mb=${ALLOC_MB}" >/dev/null 2>&1 || true
    fi
    sleep "$INTERVAL"
  done
}

stop() {
  if stop_pidfile "$OOM_LOOP_PIDFILE" "$OOM_LOOP_MATCH"; then
    echo "OOMKill loop stopped"
  else
    echo "OOMKill loop wasn't running"
  fi

  # Best-effort: free what the surviving container still holds.
  if demo_cluster_check 2>/dev/null; then
    POD=$(demo_kubectl -n "$NS" get pod -l "app=$DEPLOY" -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)
    if [[ -n "$POD" ]]; then
      demo_kubectl -n "$NS" exec "$POD" -- \
        wget -q -O /dev/null -T 3 "http://127.0.0.1:8080/reset" >/dev/null 2>&1 || true
    fi
    echo "==> payments-api should recover within ~30s as Kubernetes restarts the container."
  fi
}

start() {
  require_demo_cluster
  if pidfile_alive "$OOM_LOOP_PIDFILE" "$OOM_LOOP_MATCH"; then
    echo "Already running (PID $(cat "$OOM_LOOP_PIDFILE")). Run ${RUN_PREFIX}$0 --stop first to restart it."
    exit 0
  fi

  echo "==> Starting OOMKill loop on the running $DEPLOY pod in '$CLUSTER_NAME'"
  echo "    Allocates ${ALLOC_MB}MB in-process as soon as the container is running"
  echo "    (checked every ${INTERVAL}s). Each /allocate forces a kernel OOMKill;"
  echo "    the kubelet restarts in place, so the pod ends up in CrashLoopBackOff."

  mkdir -p "$STATE_DIR"
  ( oom_loop ) >"$LOG" 2>&1 </dev/null &
  LOOP_PID=$!
  echo "$LOOP_PID" >"$OOM_LOOP_PIDFILE"
  disown "$LOOP_PID" 2>/dev/null || true

  sleep 0.5
  if ! kill -0 "$LOOP_PID" 2>/dev/null; then
    echo "ERROR: loop failed to start; see $LOG" >&2
    rm -f "$OOM_LOOP_PIDFILE"
    exit 1
  fi

  echo
  echo "Loop running (PID $LOOP_PID), log: $LOG"
  echo "  Watch the dashboard:"
  echo "    - Restart rate per pod: climbs for the serving pod"
  echo "    - Service errors %: spikes during each restart window"
  echo "    - Composite Health: drops as CrashLoopBackOff fires"
  echo "    - OOMKills (last 5m): counts up"
  echo "  And K8sGPT's finding:"
  echo "    kubectl --context $KUBE_CONTEXT -n k8sgpt-system get results"
  echo
  echo "  Stop with: ${RUN_PREFIX}$0 --stop"
}

case "${1:-start}" in
  --stop|stop) stop ;;
  start)       start ;;
  *)           echo "usage: $0 [start|--stop]" >&2; exit 2 ;;
esac
