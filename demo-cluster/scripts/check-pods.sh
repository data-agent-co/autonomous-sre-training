#!/usr/bin/env bash
# Fail loudly on ImagePullBackOff / ErrImagePull / CrashLoopBackOff /
# long-Pending pods within a given namespace and (optionally) a label
# selector, in the demo cluster. up.sh runs it as its last check.
#
# Usage:
#   check-pods.sh <namespace> [label-selector] [wait-seconds]
#
# Examples:
#   check-pods.sh k8sgpt-system
#   check-pods.sh k8sgpt-system 'app.kubernetes.io/instance=k8s-watcher' 90
#
# Exit codes: 0 = all pods healthy, 1 = something bad after wait window.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "$SCRIPT_DIR/lib.sh"
require_demo_cluster

NS="${1:?namespace required}"
SELECTOR="${2:-}"
WAIT_S="${3:-60}"

RED=$'\033[31m'
YELLOW=$'\033[33m'
GREEN=$'\033[32m'
BOLD=$'\033[1m'
NC=$'\033[0m'

bad_reasons() {
  # kubectl JSONPath over container waiting reasons + phase
  local args=(get pods -n "$NS")
  [[ -n "$SELECTOR" ]] && args+=(-l "$SELECTOR")
  args+=(-o 'jsonpath={range .items[*]}{.metadata.name}{"\t"}{.status.phase}{"\t"}{range .status.containerStatuses[*]}{.state.waiting.reason}{" "}{end}{"\n"}{end}')
  demo_kubectl "${args[@]}" 2>/dev/null
}

echo "  ${BOLD}pod health check${NC}   ns=${NS}  selector=${SELECTOR:-<all>}  wait=${WAIT_S}s"

deadline=$(( SECONDS + WAIT_S ))
while (( SECONDS < deadline )); do
  bad=""
  # A failed kubectl call counts as unhealthy, not as "no pods".
  if ! pods="$(bad_reasons)"; then
    bad="  ${RED}✗${NC}  could not list pods in $NS"$'\n'
    pods=""
  fi
  while IFS=$'\t' read -r name phase reasons; do
    [[ -z "$name" ]] && continue
    # Any known bad waiting reason?
    if echo "$reasons" | grep -qE 'ImagePullBackOff|ErrImagePull|CrashLoopBackOff|CreateContainerConfigError|InvalidImageName'; then
      bad+="  ${RED}✗${NC}  $name  ${YELLOW}[${reasons}]${NC}"$'\n'
    elif [[ "$phase" == "Pending" ]]; then
      bad+="  ${YELLOW}…${NC}  $name  [Pending]"$'\n'
    fi
  done <<<"$pods"

  if [[ -z "$bad" ]]; then
    echo "  ${GREEN}✓${NC} all pods healthy in $NS"
    exit 0
  fi
  sleep 3
done

echo "  ${RED}${BOLD}FAILURE${NC}  after ${WAIT_S}s some pods are still unhealthy:"
echo -n "$bad"
echo
echo "  Hint: look at the pod's events and logs, e.g."
echo "    kubectl --context $KUBE_CONTEXT -n $NS describe pod <name>"
echo "  ImagePullBackOff on a locally built image (payments-api, recommend-svc,"
echo "  k8s-watcher) means it was not kind-loaded: re-run ${RUN_PREFIX}./scripts/up.sh."
exit 1
