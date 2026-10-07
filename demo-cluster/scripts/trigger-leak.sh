#!/usr/bin/env bash
# Toggle a fast memory leak in the recommend-svc pod's leak-fixture
# sidecar. trigger-slowleak.sh does the same at 20 MB/min (OOM in ~6 min).
#
# The recommend-svc pod has two containers:
#   1. `recommend-svc`: a clean HTTP service with Prometheus metrics and no
#      fault code.
#   2. `leak-fixture`: a sidecar running leak.py. Idle by default
#      (LEAK_RATE_MB_PER_MIN=0). Setting the variable to N > 0 makes it
#      consume N MiB/min until it is OOMKilled at its own 128Mi limit.
#
# This script sets that variable on the Deployment, so the leak state
# survives container restarts (it lives in the pod spec, not in RAM).
#
# Default rate 280 MB/min: from a ~15 MiB baseline the sidecar reaches its
# 128Mi limit and is OOMKilled about 28 seconds after this script runs
# (the new pod's rollout and Python's start-up included). Memory climbs
# while K8sGPT stays silent, because nothing is broken yet.
#
# Usage:
#   ./trigger-leak.sh                  leak ON at 280 MB/min (OOM in ~30 s)
#   ./trigger-leak.sh --rate 100       leak ON at 100 MB/min (OOM in ~1 min)
#   ./trigger-leak.sh --off            leak OFF (the pod restarts at baseline)
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "$SCRIPT_DIR/lib.sh"

NS="demo-apps"
RATE=280
ACTION="on"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --off) ACTION="off"; shift ;;
    --rate)
      [[ $# -ge 2 ]] || die "--rate needs a value in MB/min, e.g. --rate 20"
      RATE="$2"; shift 2 ;;
    *) echo "usage: $0 [--rate MB_PER_MIN | --off]" >&2; exit 2 ;;
  esac
done

# A rate of 0 means "off" to the fixture; refuse it here so `--rate 0`
# can't look like a leak that never grows.
if [[ "$ACTION" == "on" ]] && ! [[ "$RATE" =~ ^[1-9][0-9]*$ ]]; then
  die "--rate must be a whole number of MB/min above 0 (got '$RATE'). Use --off to stop the leak."
fi

require_demo_cluster

if [[ "$ACTION" == "on" ]]; then
  demo_kubectl -n "$NS" set env deploy/recommend-svc \
    --containers=leak-fixture \
    LEAK_RATE_MB_PER_MIN="$RATE" >/dev/null
  echo "==> leak-fixture enabled at ${RATE}MB/min in '$CLUSTER_NAME' (persists across pod restarts)"
  echo "    Watch container_memory_working_set_bytes{container=\"leak-fixture\"} climb,"
  echo "    or run ${RUN_PREFIX}./scripts/leak-progress.sh."
  # ~113 MiB of headroom above the ~15 MiB baseline, plus ~4 s for the
  # new pod to start.
  oom_s=$(( 113 * 60 / RATE + 4 ))
  if (( oom_s < 90 )); then oom_eta="~${oom_s} s"; else oom_eta="~$(( (oom_s + 30) / 60 )) min"; fi
  echo "    Expect the OOMKill in ${oom_eta}; K8sGPT stays silent until then."
else
  demo_kubectl -n "$NS" set env deploy/recommend-svc \
    --containers=leak-fixture \
    LEAK_RATE_MB_PER_MIN=0 >/dev/null
  echo "==> leak-fixture disabled (rate set to 0; sidecar idles)"
fi
