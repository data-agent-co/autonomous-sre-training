#!/usr/bin/env bash
# Shared helpers for the demo-cluster scripts. Source it, don't run it.
#
# Every script acts on one kind cluster only: CLUSTER_NAME (default
# demo-cluster). kubectl and helm calls go through demo_kubectl and
# demo_helm, which pin them to the kind-$CLUSTER_NAME context, so your
# current kubectl context never matters.
#
#   CLUSTER_NAME=demo-cluster  kind cluster to act on. Set it to run a
#                              second demo cluster next to the first.

# The scripts that source this file read the variables it sets.
# shellcheck disable=SC2034
DEMO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CLUSTER_NAME="${CLUSTER_NAME:-demo-cluster}"
CLUSTER_PROVIDER="${CLUSTER_PROVIDER:-kind}"
case "$CLUSTER_PROVIDER" in
  kind|k3d) ;;
  *)
    echo "ERROR: CLUSTER_PROVIDER must be kind or k3d, not '$CLUSTER_PROVIDER'." >&2
    exit 1
    ;;
esac
KUBE_CONTEXT="${CLUSTER_PROVIDER}-${CLUSTER_NAME}"
# PID files and logs of this cluster's background helpers, and up.sh logs.
STATE_DIR="$DEMO_DIR/.run/$CLUSTER_NAME"

# Background helpers: their PID file, and a piece of the command line each
# one runs with. stop_pidfile kills a PID only when its command line still
# contains that piece, so it never stops a process of yours.
GRAFANA_PF_PIDFILE="$STATE_DIR/grafana-pf.pid"
GRAFANA_PF_MATCH="--context $KUBE_CONTEXT -n monitoring port-forward svc/prometheus-stack-grafana"
OOM_LOOP_PIDFILE="$STATE_DIR/trigger-oomkill.pid"
OOM_LOOP_MATCH="trigger-oomkill.sh"

# Prefix for the commands the scripts print as hints, so that a hint run
# as printed acts on this cluster too.
RUN_PREFIX=""
if [[ "$CLUSTER_PROVIDER" != "kind" ]]; then
  RUN_PREFIX="CLUSTER_PROVIDER=$CLUSTER_PROVIDER "
fi
if [[ "$CLUSTER_NAME" != "demo-cluster" ]]; then
  RUN_PREFIX+="CLUSTER_NAME=$CLUSTER_NAME "
fi

die() {
  echo "ERROR: $*" >&2
  exit 1
}

# cluster_exists: succeeds when $CLUSTER_PROVIDER has a cluster named
# $CLUSTER_NAME.
cluster_exists() {
  case "$CLUSTER_PROVIDER" in
    kind) kind get clusters 2>/dev/null | grep -Fxq -- "$CLUSTER_NAME" ;;
    k3d) k3d cluster get "$CLUSTER_NAME" >/dev/null 2>&1 ;;
  esac
}

# cluster_kubeconfig: print the kubeconfig $CLUSTER_PROVIDER holds for
# $CLUSTER_NAME.
cluster_kubeconfig() {
  case "$CLUSTER_PROVIDER" in
    kind) kind get kubeconfig --name "$CLUSTER_NAME" ;;
    k3d) k3d kubeconfig get "$CLUSTER_NAME" ;;
  esac
}

# kubeconfig_fix_hint: print the command that writes the $KUBE_CONTEXT
# context back into your kubeconfig.
kubeconfig_fix_hint() {
  case "$CLUSTER_PROVIDER" in
    kind) echo "kind export kubeconfig --name $CLUSTER_NAME" ;;
    k3d) echo "k3d kubeconfig merge $CLUSTER_NAME --kubeconfig-merge-default" ;;
  esac
}

# load_images IMAGE...: copy local Docker images into the cluster's nodes.
load_images() {
  case "$CLUSTER_PROVIDER" in
    kind) kind load docker-image "$@" --name "$CLUSTER_NAME" ;;
    k3d) k3d image import "$@" --cluster "$CLUSTER_NAME" ;;
  esac
}

demo_kubectl() { command kubectl --context "$KUBE_CONTEXT" "$@"; }
demo_helm() { command helm --kube-context "$KUBE_CONTEXT" "$@"; }

# demo_cluster_check: succeeds when $CLUSTER_PROVIDER cluster $CLUSTER_NAME
# exists and the kubeconfig context $KUBE_CONTEXT points at it. Otherwise
# says why on stderr and fails.
demo_cluster_check() {
  if ! command -v "$CLUSTER_PROVIDER" >/dev/null 2>&1; then
    echo "ERROR: $CLUSTER_PROVIDER is not installed." >&2
    return 1
  fi
  if ! cluster_exists; then
    echo "ERROR: $CLUSTER_PROVIDER cluster '$CLUSTER_NAME' not found. Create it with ${RUN_PREFIX}./scripts/up.sh." >&2
    return 1
  fi
  local ctx_cluster have want
  ctx_cluster="$(command kubectl config view \
    -o "jsonpath={.contexts[?(@.name==\"$KUBE_CONTEXT\")].context.cluster}" 2>/dev/null || true)"
  if [[ -z "$ctx_cluster" ]]; then
    echo "ERROR: your kubeconfig has no '$KUBE_CONTEXT' context." >&2
    echo "       Add it back with: $(kubeconfig_fix_hint)" >&2
    return 1
  fi
  have="$(command kubectl config view \
    -o "jsonpath={.clusters[?(@.name==\"$ctx_cluster\")].cluster.server}" 2>/dev/null || true)"
  want="$(cluster_kubeconfig 2>/dev/null | awk '$1 == "server:" { print $2; exit }')"
  if [[ -z "$want" || "$have" != "$want" ]]; then
    echo "ERROR: context '$KUBE_CONTEXT' points at '${have:-?}', not at $CLUSTER_PROVIDER cluster '$CLUSTER_NAME' (${want:-?})." >&2
    echo "       Fix it with: $(kubeconfig_fix_hint)" >&2
    return 1
  fi
}

# require_demo_cluster: exit unless demo_cluster_check passes.
require_demo_cluster() {
  demo_cluster_check || exit 1
}

# load_env: export the settings in $ENV_FILE (default demo-cluster/.env).
# A missing default .env is fine: Slack and the LLM are optional and every
# other setting has a default.
load_env() {
  local file="${ENV_FILE:-$DEMO_DIR/.env}"
  if [[ -f "$file" ]]; then
    set -a
    # shellcheck disable=SC1090
    source "$file"
    set +a
  elif [[ -n "${ENV_FILE:-}" ]]; then
    die "ENV_FILE=$ENV_FILE not found"
  fi
}

# pidfile_alive FILE MATCH: succeeds when FILE holds the PID of a running
# process whose command line contains MATCH.
pidfile_alive() {
  local file="$1" match="$2" pid cmd
  [[ -f "$file" ]] || return 1
  pid="$(cat "$file" 2>/dev/null || true)"
  [[ "$pid" =~ ^[0-9]+$ ]] || return 1
  cmd="$(ps -ww -o command= -p "$pid" 2>/dev/null || true)"
  [[ -n "$cmd" && "$cmd" == *"$match"* ]]
}

# stop_pidfile FILE MATCH: stop the process in FILE if pidfile_alive says
# it is still ours, then remove FILE. Succeeds only when it stopped one.
stop_pidfile() {
  local file="$1" match="$2" rc=1
  if pidfile_alive "$file" "$match"; then
    kill "$(cat "$file")" 2>/dev/null && rc=0
  fi
  rm -f "$file"
  return "$rc"
}
