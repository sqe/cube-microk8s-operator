#!/usr/bin/env bash
set -Eeuo pipefail

CLUSTER_NAME=${CLUSTER_NAME:-cube-demo}
command -v kind >/dev/null 2>&1 || { echo "Required command not found: kind" >&2; exit 1; }
if kind get clusters 2>/dev/null | grep -Fxq "$CLUSTER_NAME"; then
  kind delete cluster --name "$CLUSTER_NAME"
else
  echo "Kind cluster $CLUSTER_NAME does not exist; nothing to delete."
fi
