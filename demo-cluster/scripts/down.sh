#!/usr/bin/env bash
# Tear down everything up.sh and the trigger scripts start: the Grafana
# port-forward, a running OOMKill loop (trigger-oomkill.sh), and the kind
# cluster itself.
#
# Deletes only the kind cluster named $CLUSTER_NAME (default demo-cluster),
# through kind, so it cannot touch any other cluster in your kubeconfig.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "$SCRIPT_DIR/lib.sh"

# Background helpers first, so none of them is left pointing at a cluster
# that no longer exists.
if stop_pidfile "$GRAFANA_PF_PIDFILE" "$GRAFANA_PF_MATCH"; then
  echo "stopped the Grafana port-forward"
fi
if stop_pidfile "$OOM_LOOP_PIDFILE" "$OOM_LOOP_MATCH"; then
  echo "stopped the OOMKill loop"
fi

if kind_cluster_exists; then
  kind delete cluster --name "$CLUSTER_NAME"
else
  echo "kind cluster '$CLUSTER_NAME' not found; nothing to delete"
fi
