#!/usr/bin/env bash
set -Eeuo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
CLUSTER_NAME=${CLUSTER_NAME:-cube-demo}
CONTEXT="kind-$CLUSTER_NAME"
CUBE_URL=${CUBE_URL:-http://127.0.0.1:4000}

need() {
  command -v "$1" >/dev/null 2>&1 || { echo "Required command not found: $1" >&2; exit 1; }
}

# Pod readiness can precede the exposed Kind port becoming reachable after a
# rollout. Retry transport failures only; HTTP and response-contract errors fail.
cube_startup_get() {
  local attempt response result
  for attempt in $(seq 1 6); do
    if response=$(curl --fail --silent --show-error --connect-timeout 5 "$@"); then
      printf '%s\n' "$response"
      return 0
    else
      result=$?
    fi
    case "$result" in
      7|28|52|56)
        if [[ "$attempt" -lt 6 ]]; then
          echo "Cube transport failed (curl $result); retrying ($attempt/6)." >&2
          sleep 2
          continue
        fi
        ;;
    esac
    return "$result"
  done
}

for command in curl jq kubectl python3; do need "$command"; done
kubectl config get-contexts "$CONTEXT" >/dev/null 2>&1 || {
  echo "Kind context $CONTEXT does not exist; run scripts/kind-up.sh first." >&2
  exit 1
}

kubectl --context "$CONTEXT" -n cube-demo wait --for=condition=Ready cubecluster/telemetry --timeout=2m
for endpoint in readyz livez; do
  response=$(cube_startup_get --max-time 10 "$CUBE_URL/$endpoint")
  jq -e '.health == "HEALTH"' <<<"$response" >/dev/null
  echo "/$endpoint: healthy"
done

api_secret=$(kubectl --context "$CONTEXT" -n cube-demo get secret cube-configuration \
  -o 'go-template={{index .data "CUBEJS_API_SECRET" | base64decode}}')
token=$(CUBE_API_SECRET="$api_secret" python3 <<'PY'
import base64, hashlib, hmac, json, os, time
encode = lambda value: base64.urlsafe_b64encode(value).rstrip(b"=")
header = encode(json.dumps({"alg": "HS256", "typ": "JWT"}, separators=(",", ":")).encode())
payload = encode(json.dumps({"iat": int(time.time()), "exp": int(time.time()) + 300}, separators=(",", ":")).encode())
body = header + b"." + payload
signature = encode(hmac.new(os.environ["CUBE_API_SECRET"].encode(), body, hashlib.sha256).digest())
print((body + b"." + signature).decode())
PY
)
unset api_secret

meta=$(cube_startup_get --max-time 30 \
  -H "Authorization: $token" "$CUBE_URL/cubejs-api/v1/meta")
jq -e '.cubes | any(.name == "KubernetesTelemetry")' <<<"$meta" >/dev/null
echo "/cubejs-api/v1/meta: KubernetesTelemetry model found"

response_file=$(mktemp)
trap 'rm -f "$response_file"' EXIT
query='{"query":{"measures":["KubernetesTelemetry.count"]}}'
for attempt in $(seq 1 30); do
  code=$(curl --silent --show-error --max-time 95 -o "$response_file" -w '%{http_code}' \
    -H "Authorization: $token" -H 'Content-Type: application/json' \
    --data "$query" "$CUBE_URL/cubejs-api/v1/load") || code="curl-error"
  if [[ "$code" == 200 ]] && jq -e '.data[0]["KubernetesTelemetry.count"] | tonumber > 0' "$response_file" >/dev/null 2>&1; then
    count=$(jq -r '.data[0]["KubernetesTelemetry.count"]' "$response_file")
    echo "/cubejs-api/v1/load: returned $count Kubernetes telemetry observations"
    break
  fi
  if jq -e '.error == "Continue wait"' "$response_file" >/dev/null 2>&1; then
    echo "Cube requested Continue wait ($attempt/30)."
    sleep 5
    continue
  fi
  echo "Cube query failed (HTTP $code):" >&2
  jq . "$response_file" >&2 2>/dev/null || cat "$response_file" >&2
  exit 1
done

if [[ "$attempt" == 30 ]] && ! jq -e '.data[0]["KubernetesTelemetry.count"] | tonumber > 0' "$response_file" >/dev/null 2>&1; then
  echo "Cube query did not return telemetry data after 30 attempts." >&2
  cat "$response_file" >&2
  exit 1
fi

forward_log=$(mktemp)
event_file=$(mktemp)
mcp_output=$(mktemp -d)
kubectl --context "$CONTEXT" -n cube-demo port-forward --address 127.0.0.1 service/agent-ingest :8082 >"$forward_log" 2>&1 &
forward_pid=$!
trap 'kill "$forward_pid" 2>/dev/null || true; wait "$forward_pid" 2>/dev/null || true; rm -f "$response_file" "$forward_log" "$event_file"; rm -rf "$mcp_output"' EXIT
for _ in $(seq 1 50); do
  grep -q 'Forwarding from' "$forward_log" && break
  sleep 0.2
done
ingest_port=$(sed -n 's/.*127.0.0.1:\([0-9]*\) ->.*/\1/p' "$forward_log" | head -1)
[[ -n "$ingest_port" ]] || { cat "$forward_log" >&2; exit 1; }
ingest_token=$(kubectl --context "$CONTEXT" -n cube-demo get secret agent-ingest \
  -o 'go-template={{index .data "token" | base64decode}}')
AGENT_INGEST_URL="http://127.0.0.1:$ingest_port" AGENT_INGEST_TOKEN="$ingest_token" \
  AGENT_EVENT_FILE="$event_file" CUBE_CORE_URL="$CUBE_URL" CUBE_CORE_TOKEN="$token" python3 "$ROOT_DIR/demo/agent-executions.py"
# A pod replacement must retain persisted records and replay protection.
kill "$forward_pid"
wait "$forward_pid" 2>/dev/null || true
kubectl --context "$CONTEXT" -n cube-demo rollout restart deployment/agent-ingest
kubectl --context "$CONTEXT" -n cube-demo rollout status deployment/agent-ingest --timeout=2m
kubectl --context "$CONTEXT" -n cube-demo port-forward --address 127.0.0.1 service/agent-ingest "$ingest_port:8082" >"$forward_log" 2>&1 &
forward_pid=$!
for _ in $(seq 1 50); do
  grep -q 'Forwarding from' "$forward_log" && break
  sleep 0.2
done
AGENT_INGEST_URL="http://127.0.0.1:$ingest_port" AGENT_INGEST_TOKEN="$ingest_token" \
  AGENT_EVENT_FILE="$event_file" CUBE_CORE_URL="$CUBE_URL" CUBE_CORE_TOKEN="$token" python3 "$ROOT_DIR/demo/agent-executions.py"
# Public CI uses a labelled fixture, not a private agent or a live weather API.
AGENT_INGEST_URL="http://127.0.0.1:$ingest_port" AGENT_INGEST_TOKEN="$ingest_token" \
  CUBE_CORE_URL="$CUBE_URL" CUBE_CORE_TOKEN="$token" python3 "$ROOT_DIR/demo/mcp_execution.py" \
  --agent weather-fixture --output "$mcp_output" -- \
  python3 "$ROOT_DIR/demo/test_mcp_execution.py" --serve-fixture
echo "Kind end-to-end demo passed: Kubernetes, agent telemetry, and MCP-to-Cube verification."
