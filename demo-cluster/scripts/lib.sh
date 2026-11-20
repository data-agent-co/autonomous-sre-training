#!/usr/bin/env bash
# Shared helpers for the demo-cluster scripts. Source it, don't run it.
#
# Every script acts on one cluster only: CLUSTER_NAME (default
# demo-cluster), run by CLUSTER_PROVIDER (kind or k3d). kubectl and helm
# calls go through demo_kubectl and demo_helm, which pin them to the
# $CLUSTER_PROVIDER-$CLUSTER_NAME context, so your current kubectl context
# never matters.
#
#   CLUSTER_NAME=demo-cluster  cluster to act on. Set it to run a second
#                              demo cluster next to the first.
#   CLUSTER_PROVIDER=kind      kind or k3d: the tool that runs the cluster
#                              and names its context.

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
# contains that piece, so it never stops a process of yours. The two
# port-forwards go to Services in $PF_NS; the piece of one must never
# occur in the command line of the other.
PF_NS="monitoring"
DASHBOARD_PF_PIDFILE="$STATE_DIR/dashboard-pf.pid"
DASHBOARD_PF_MATCH="--context $KUBE_CONTEXT -n $PF_NS port-forward svc/demo-dashboard"
PERSES_PF_PIDFILE="$STATE_DIR/perses-pf.pid"
PERSES_PF_MATCH="--context $KUBE_CONTEXT -n $PF_NS port-forward svc/perses"
OOM_LOOP_PIDFILE="$STATE_DIR/trigger-oomkill.pid"
OOM_LOOP_MATCH="trigger-oomkill.sh"

# Local ports of the two port-forwards, unless DASHBOARD_PORT or PERSES_PORT
# names another one.
DASHBOARD_PORT_DEFAULT=8080
PERSES_PORT_DEFAULT=8081

# Prefix for the commands the scripts print as hints, so that a hint run
# as printed acts on this cluster too.
RUN_PREFIX=""
if [[ "$CLUSTER_PROVIDER" != "kind" ]]; then
  RUN_PREFIX="CLUSTER_PROVIDER=$CLUSTER_PROVIDER "
fi
if [[ "$CLUSTER_NAME" != "demo-cluster" ]]; then
  RUN_PREFIX+="CLUSTER_NAME=$CLUSTER_NAME "
fi

# up_prefix: print RUN_PREFIX for a hint that says to run up.sh (or reset.sh,
# which runs it), with the ports this cluster's dashboards are on. up.sh
# moves both port-forwards to the ports it is run with, so without them the
# hint would take the dashboards to 8080 and 8081. The ports are the ones
# this run was given, else the ones the running port-forwards are on.
up_prefix() {
  local prefix="$RUN_PREFIX" port
  port="${DASHBOARD_PORT:-$(pf_local_port "$DASHBOARD_PF_PIDFILE" "$DASHBOARD_PF_MATCH")}"
  if [[ -n "$port" && "$port" != "$DASHBOARD_PORT_DEFAULT" ]]; then
    prefix+="DASHBOARD_PORT=$port "
  fi
  port="${PERSES_PORT:-$(pf_local_port "$PERSES_PF_PIDFILE" "$PERSES_PF_MATCH")}"
  if [[ -n "$port" && "$port" != "$PERSES_PORT_DEFAULT" ]]; then
    prefix+="PERSES_PORT=$port "
  fi
  printf '%s' "$prefix"
}

die() {
  echo "ERROR: $*" >&2
  exit 1
}

# cluster_exists: succeeds when $CLUSTER_PROVIDER has a cluster named
# $CLUSTER_NAME.
cluster_exists() {
  case "$CLUSTER_PROVIDER" in
    kind)
      # Let kind finish writing before matching. grep -q leaves at the first
      # match; with pipefail, a kind that dies writing its next line would
      # make an existing cluster look missing.
      local names
      names="$(kind get clusters 2>/dev/null)" || return 1
      grep -Fxq -- "$CLUSTER_NAME" <<<"$names"
      ;;
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
    echo "ERROR: $CLUSTER_PROVIDER cluster '$CLUSTER_NAME' not found. Create it with $(up_prefix)./scripts/up.sh." >&2
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

# pf_local_port FILE MATCH: print the local port of the port-forward whose
# PID is in FILE, and nothing when it is not running (see pidfile_alive).
# Always succeeds.
pf_local_port() {
  local file="$1" match="$2" cmd
  # ... port-forward svc/<name> <local port>:<service port>
  local ports=' ([0-9]+):[0-9]+[[:space:]]*$'
  pidfile_alive "$file" "$match" || return 0
  cmd="$(ps -ww -o command= -p "$(cat "$file")" 2>/dev/null || true)"
  if [[ "$cmd" =~ $ports ]]; then
    echo "${BASH_REMATCH[1]}"
  fi
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

# The demo dashboard and Perses are each reached through a kubectl
# port-forward that runs in the background. dashboard.sh and perses.sh
# describe theirs in these variables, then call port_forward_main "$@":
#
#   PF_NAME         name for the messages, as it starts a sentence
#   PF_SVC          Service in the $PF_NS namespace
#   PF_SVC_PORT     port of that Service
#   PF_PORT         local port
#   PF_PORT_VAR     name of the variable that sets the local port, for hints
#   PF_HEALTH_PATH  path that answers 200 once the server is reachable
#   PF_PAGE_PATH    path of the page to open (empty for /)
#   PF_PIDFILE      PID file of the port-forward
#   PF_MATCH        piece of its command line (see the top of this file)
#   PF_LOG          its log

# port_forward_stop: stop the port-forward and say whether one was running.
port_forward_stop() {
  if stop_pidfile "$PF_PIDFILE" "$PF_MATCH"; then
    echo "$PF_NAME port-forward stopped"
  else
    echo "$PF_NAME port-forward wasn't running for '$CLUSTER_NAME'"
  fi
}

# port_forward_start: start the port-forward in the background, in place of
# one started earlier, and wait until the server answers through it. Exits
# with an error when it cannot start or the server does not answer.
port_forward_start() {
  require_demo_cluster
  # Only ever a port-forward these scripts started (see the PID files at
  # the top of this file), never one of yours.
  stop_pidfile "$PF_PIDFILE" "$PF_MATCH" || true

  if ! demo_kubectl -n "$PF_NS" get svc "$PF_SVC" >/dev/null 2>&1; then
    die "$PF_NAME service $PF_NS/$PF_SVC not found. Is it installed? Run $(up_prefix)./scripts/up.sh."
  fi

  # Perses was on port 8080 before the demo dashboard took it. A Perses
  # port-forward that is still there would make the check below refuse the
  # port, so stop it; perses.sh starts Perses again on its own port. On any
  # other port a Perses port-forward is no leftover, and the check refuses.
  if [[ "$PF_PORT" == "$DASHBOARD_PORT_DEFAULT" ]] &&
     pidfile_alive "$PERSES_PF_PIDFILE" "$PERSES_PF_MATCH $PF_PORT:"; then
    stop_pidfile "$PERSES_PF_PIDFILE" "$PERSES_PF_MATCH" || true
    echo "Stopped the Perses port-forward that was still on port $PF_PORT. Start it again with ${RUN_PREFIX}./scripts/perses.sh."
  fi

  # Refuse a port something else already listens on (curl exit code 7 means
  # nothing did). Give a port-forward stopped just above a moment to let go.
  # Check [::1] too: kubectl silently skips an address it can't bind, so
  # localhost would still reach the other program there.
  local rc addr
  for addr in 127.0.0.1 '[::1]'; do
    for _ in 1 2 3 4 5 6; do
      rc=0
      curl -s -o /dev/null --max-time 2 "http://${addr}:${PF_PORT}/" || rc=$?
      [[ $rc -eq 7 ]] && break
      sleep 0.5
    done
    if [[ $rc -ne 7 ]]; then
      die "port $PF_PORT is already in use on $addr. Pick a free one: ${RUN_PREFIX}${PF_PORT_VAR}=<port> $0"
    fi
  done

  mkdir -p "$STATE_DIR"
  # The command line must keep containing $PF_MATCH.
  nohup kubectl --context "$KUBE_CONTEXT" -n "$PF_NS" port-forward \
    "svc/$PF_SVC" "$PF_PORT:$PF_SVC_PORT" >"$PF_LOG" 2>&1 </dev/null &
  local pid=$!
  echo "$pid" >"$PF_PIDFILE"
  disown "$pid" 2>/dev/null || true

  # Wait for the listener: kubectl logs "Forwarding from" once it listens.
  # Until then, whatever else might grab the port could answer the probe.
  local code
  for _ in $(seq 1 20); do
    if ! kill -0 "$pid" 2>/dev/null; then
      rm -f "$PF_PIDFILE"
      echo "ERROR: the port-forward exited. Is port $PF_PORT in use? Try ${RUN_PREFIX}${PF_PORT_VAR}=<free port> $0" >&2
      tail -5 "$PF_LOG" >&2 || true
      exit 1
    fi
    code=""
    if grep -q "Forwarding from" "$PF_LOG" 2>/dev/null; then
      code="$(curl -sS -o /dev/null -w '%{http_code}' "http://127.0.0.1:${PF_PORT}${PF_HEALTH_PATH}" 2>/dev/null || true)"
    fi
    if [[ "$code" == "200" ]]; then
      echo "$PF_NAME is up at http://localhost:${PF_PORT}${PF_PAGE_PATH}  (no login)"
      echo "  Stop with: ${RUN_PREFIX}$0 --stop"
      return 0
    fi
    sleep 0.5
  done

  stop_pidfile "$PF_PIDFILE" "$PF_MATCH" || true
  echo "ERROR: $PF_NAME is not responding on port ${PF_PORT} after 10s. Log $PF_LOG:" >&2
  tail -10 "$PF_LOG" >&2 || true
  exit 1
}

# port_forward_main [start|--stop]: start the port-forward (the default) or
# stop it.
port_forward_main() {
  case "${1:-start}" in
    --stop|stop) port_forward_stop ;;
    start)       port_forward_start ;;
    *)           echo "usage: $0 [start|--stop]" >&2; exit 2 ;;
  esac
}
