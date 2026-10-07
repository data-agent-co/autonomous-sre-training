#!/usr/bin/env bash
# Soft reset: return the demo cluster to its just-installed state without
# deleting it. Idempotent, safe to re-run. Acts only on the kind cluster
# named $CLUSTER_NAME (default demo-cluster).
#
# What it does:
#   1. Stop the slow leak and the OOMKill loop
#   2. Replace payments-api (restart count and back-off start from zero)
#      and recommend-svc (memory back to baseline)
#   3. Restart Prometheus (wipes its TSDB, so Grafana graphs start flat)
#   4. Restart slack-relay, if Slack is on, so it forgets what it posted
#   5. Restart the watcher, so it forgets what it reported
#   6. Delete all K8sGPT Results and leftover watcher helm-test pods
#
# For a full teardown and reinstall use reset.sh (kind delete + up.sh,
# much slower).
set -uo pipefail  # no -e; a failed step is reported at the end, not fatal

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "$SCRIPT_DIR/lib.sh"
require_demo_cluster

NS_APPS="demo-apps"
NS_K8SGPT="k8sgpt-system"
NS_MON="monitoring"
WATCHER_RELEASE="k8s-watcher"
# k8s-watcher.fullname in charts/k8s-watcher: the release name, which contains the
# chart name.
WATCHER_DEPLOY="$WATCHER_RELEASE"

# Each step's output goes to a log of its own, as in up.sh; the summary
# prints the path and the last lines of every step that failed.
LOG_DIR="$STATE_DIR/soft-reset-$(date +%Y%m%d-%H%M%S)"
mkdir -p "$LOG_DIR"
STEP_NO=0
STEP_LOG=""
failed=()
failed_logs=()

# next_log LABEL: set STEP_LOG to the next step's log file. (Not $(...):
# STEP_NO must count up in this shell.)
next_log() {
  STEP_NO=$((STEP_NO + 1))
  STEP_LOG="$(printf '%s/%02d-%s.log' "$LOG_DIR" "$STEP_NO" "${1//[^A-Za-z0-9.+-]/_}")"
}

# fail LABEL LOG: remember a failed step and its log for the summary.
fail() {
  failed+=("$1")
  failed_logs+=("$2")
}

# try LABEL CMD...: run CMD with its output in its own log. If it fails,
# remember LABEL and the log for the summary and go on with the next step.
try() {
  local label="$1"
  shift
  next_log "$label"
  "$@" >"$STEP_LOG" 2>&1 || fail "$label" "$STEP_LOG"
}

# deploy_state NAME LOG: "present", "absent" (NotFound) or "error" for
# deploy/NAME in k8sgpt-system. On "error", kubectl's output is in LOG.
deploy_state() {
  local out
  if out="$(demo_kubectl -n "$NS_K8SGPT" get "deploy/$1" -o name 2>&1)"; then
    echo present
  elif [[ "$out" == *NotFound* ]]; then
    echo absent
  else
    printf '%s\n' "$out" >"$2"
    echo error
  fi
}

echo "=== soft reset of '$CLUSTER_NAME' ==="

echo "  1. stop the slow leak and the OOMKill loop"
try "stop the slow leak" "$SCRIPT_DIR/trigger-slowleak.sh" --off
try "stop the OOMKill loop" "$SCRIPT_DIR/trigger-oomkill.sh" --stop

echo "  2. replace payments-api and recommend-svc"
# Each OOMKill run crashes payments-api again, and the kubelet's restart
# back-off grows with the restart count (up to 5 minutes). A pod carrying
# dozens of restarts from earlier runs comes back in long, irregular
# windows; K8sGPT catches it running in one of them, drops its Result and
# reports it again on the next crash. A fresh pod starts from zero.
try "replace payments-api and recommend-svc" \
  demo_kubectl -n "$NS_APPS" rollout restart deploy/payments-api deploy/recommend-svc
try "payments-api rollout" demo_kubectl -n "$NS_APPS" rollout status deploy/payments-api --timeout=120s
try "recommend-svc rollout" demo_kubectl -n "$NS_APPS" rollout status deploy/recommend-svc --timeout=120s

echo "  3. restart Prometheus (wipe TSDB)"
try "restart Prometheus" \
  demo_kubectl -n "$NS_MON" rollout restart statefulset/prometheus-prometheus-stack-kube-prom-prometheus
try "Prometheus rollout" \
  demo_kubectl -n "$NS_MON" rollout status statefulset/prometheus-prometheus-stack-kube-prom-prometheus --timeout=90s

echo "  4. restart slack-relay (forget what it posted)"
next_log "look up slack-relay"
case "$(deploy_state slack-relay "$STEP_LOG")" in
  present)
    try "restart slack-relay" demo_kubectl -n "$NS_K8SGPT" rollout restart deploy/slack-relay
    try "slack-relay rollout" demo_kubectl -n "$NS_K8SGPT" rollout status deploy/slack-relay --timeout=60s
    ;;
  absent) echo "     Slack is off, skip" ;;
  *) fail "look up slack-relay" "$STEP_LOG" ;;
esac

echo "  5. restart the watcher (forget what it reported)"
next_log "look up the watcher"
case "$(deploy_state "$WATCHER_DEPLOY" "$STEP_LOG")" in
  present)
    try "restart the watcher" demo_kubectl -n "$NS_K8SGPT" rollout restart "deploy/$WATCHER_DEPLOY"
    try "watcher rollout" demo_kubectl -n "$NS_K8SGPT" rollout status "deploy/$WATCHER_DEPLOY" --timeout=90s
    ;;
  absent) echo "     watcher not installed, skip" ;;
  *) fail "look up the watcher" "$STEP_LOG" ;;
esac

echo "  6. delete K8sGPT Results and leftover helm-test pods"
# Last, so a Result written while the steps above ran is gone too.
try "delete Results" demo_kubectl delete results.core.k8sgpt.ai --all -A --wait=false
# helm keeps failed test-hook pods; only those carry the helm.sh/chart label.
try "delete helm-test pods" demo_kubectl -n "$NS_K8SGPT" delete pod \
  -l "app.kubernetes.io/instance=$WATCHER_RELEASE,helm.sh/chart" \
  --ignore-not-found --wait=false

if [[ ${#failed[@]} -gt 0 ]]; then
  echo "=== done with errors: these steps failed ===" >&2
  for i in "${!failed[@]}"; do
    echo "  - ${failed[$i]}" >&2
    echo "    log: ${failed_logs[$i]}" >&2
    tail -10 "${failed_logs[$i]}" | sed 's/^/      /' >&2
  done
  echo "Re-run ${RUN_PREFIX}./scripts/soft-reset.sh, or start from scratch with ${RUN_PREFIX}./scripts/reset.sh." >&2
  exit 1
fi
echo "=== done: '$CLUSTER_NAME' is back to its just-installed state ===   (logs: $LOG_DIR)"
