#!/usr/bin/env bash
# Full reset: delete the cluster and build it again (down.sh, then up.sh).
# Acts only on the $CLUSTER_PROVIDER (kind or k3d) cluster named
# $CLUSTER_NAME (default demo-cluster). soft-reset.sh is faster and keeps
# the cluster.
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
"$SCRIPT_DIR/down.sh"
"$SCRIPT_DIR/up.sh"
