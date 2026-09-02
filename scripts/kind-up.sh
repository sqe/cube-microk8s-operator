#!/usr/bin/env bash
set -Eeuo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
CLUSTER_NAME=${CLUSTER_NAME:-cube-demo}
CONTEXT="kind-$CLUSTER_NAME"
OPERATOR_IMAGE=${OPERATOR_IMAGE:-cube-operator:v0.1.0}
KOPF_IMAGE=${KOPF_IMAGE:-cube-operator-kopf:v0.1.0}
CONTROLLER=${CONTROLLER:-go}

need() {
  command -v "$1" >/dev/null 2>&1 || { echo "Required command not found: $1" >&2; exit 1; }
}
for command in docker kind kubectl python3; do need "$command"; done
if [[ "$CONTROLLER" != "go" && "$CONTROLLER" != "kopf" ]]; then
  echo "CONTROLLER must be 'go' or 'kopf'." >&2
  exit 1
fi
if ! docker info >/dev/null 2>&1; then
  echo "Docker is installed but its daemon is unavailable." >&2
  exit 1
fi

if ! kind get clusters 2>/dev/null | grep -Fxq "$CLUSTER_NAME"; then
  kind create cluster --name "$CLUSTER_NAME" --config "$ROOT_DIR/demo/kind/kind.yaml"
else
  echo "Kind cluster $CLUSTER_NAME already exists; reusing it."
fi

docker build --pull -t "$OPERATOR_IMAGE" "$ROOT_DIR"
kind load docker-image --name "$CLUSTER_NAME" "$OPERATOR_IMAGE"
if [[ "$CONTROLLER" == "kopf" ]]; then
  docker build --pull -f "$ROOT_DIR/python/Dockerfile" -t "$KOPF_IMAGE" "$ROOT_DIR"
  kind load docker-image --name "$CLUSTER_NAME" "$KOPF_IMAGE"
  kubectl --context "$CONTEXT" -n cube-system delete deployment cube-operator --ignore-not-found
  kubectl --context "$CONTEXT" apply -k "$ROOT_DIR/config/kopf"
  kubectl --context "$CONTEXT" -n cube-system rollout status deployment/cube-operator-kopf --timeout=5m
else
  kubectl --context "$CONTEXT" -n cube-system delete deployment cube-operator-kopf --ignore-not-found
  kubectl --context "$CONTEXT" apply -k "$ROOT_DIR/config/default"
  kubectl --context "$CONTEXT" -n cube-system rollout status deployment/cube-operator --timeout=5m
fi

kubectl --context "$CONTEXT" create namespace cube-demo --dry-run=client -o yaml |
  kubectl --context "$CONTEXT" apply -f -

random_hex() {
  python3 -c 'import secrets; print(secrets.token_hex(24))'
}
secret_value() {
  local secret=$1 key=$2
  kubectl --context "$CONTEXT" -n cube-demo get secret "$secret" \
    -o "go-template={{index .data \"$key\" | base64decode}}" 2>/dev/null || true
}

postgres_password=$(secret_value postgres POSTGRES_PASSWORD)
[[ -n "$postgres_password" ]] || postgres_password=$(random_hex)
api_secret=$(secret_value cube-configuration CUBEJS_API_SECRET)
[[ -n "$api_secret" ]] || api_secret=$(random_hex)
database_url="postgres://cube:${postgres_password}@postgres:5432/telemetry?sslmode=disable"

kubectl --context "$CONTEXT" -n cube-demo create secret generic postgres \
  --from-literal=POSTGRES_DB=telemetry \
  --from-literal=POSTGRES_USER=cube \
  --from-literal=POSTGRES_PASSWORD="$postgres_password" \
  --dry-run=client -o yaml | kubectl --context "$CONTEXT" apply -f -

kubectl --context "$CONTEXT" -n cube-demo create secret generic cube-configuration \
  --from-literal=CUBEJS_DB_TYPE=postgres \
  --from-literal=CUBEJS_DB_HOST=postgres \
  --from-literal=CUBEJS_DB_PORT=5432 \
  --from-literal=CUBEJS_DB_NAME=telemetry \
  --from-literal=CUBEJS_DB_USER=cube \
  --from-literal=CUBEJS_DB_PASS="$postgres_password" \
  --from-literal=CUBEJS_API_SECRET="$api_secret" \
  --from-literal=DATABASE_URL="$database_url" \
  --dry-run=client -o yaml | kubectl --context "$CONTEXT" apply -f -

kubectl --context "$CONTEXT" apply -k "$ROOT_DIR/demo/kind"
node_architecture=$(kubectl --context "$CONTEXT" get nodes -o jsonpath='{.items[0].status.nodeInfo.architecture}')
case "$node_architecture" in
  amd64)
    cube_store_image="cubejs/cubestore:v1.7.20@sha256:cd5fe68049204640704a6412a39e7a09eb391fc70890577dd21b5480d85cb219"
    ;;
  arm64)
    cube_store_image="cubejs/cubestore:arm64v8@sha256:d9254a2166513e99f888da6f85362362357805116cd4d70f2b22e318e6ca5007"
    ;;
  *)
    echo "Unsupported Kind node architecture: $node_architecture (expected amd64 or arm64)." >&2
    exit 1
    ;;
esac
kubectl --context "$CONTEXT" -n cube-demo patch cubecluster telemetry --type=merge \
  -p "{\"spec\":{\"cubeStore\":{\"image\":\"$cube_store_image\"}}}"
echo "Selected digest-pinned Cube Store image for $node_architecture."
kubectl --context "$CONTEXT" -n cube-demo rollout status statefulset/postgres --timeout=5m
kubectl --context "$CONTEXT" -n cube-demo rollout status deployment/telemetry-collector --timeout=5m
kubectl --context "$CONTEXT" -n cube-demo wait --for=condition=Ready cubecluster/telemetry --timeout=15m

echo "Cube Core demo is ready at http://127.0.0.1:4000 (local machine only)."
echo "Run scripts/kind-e2e.sh to verify the health, metadata, and telemetry query contracts."
