#!/usr/bin/env bash
set -Eeuo pipefail

CLUSTER_NAME=${CLUSTER_NAME:-cube-demo}
CONTEXT="kind-$CLUSTER_NAME"
CUBE_URL=${CUBE_URL:-http://127.0.0.1:4000}

need() {
  command -v "$1" >/dev/null 2>&1 || { echo "Required command not found: $1" >&2; exit 1; }
}
for command in curl jq kubectl python3; do need "$command"; done
kubectl config get-contexts "$CONTEXT" >/dev/null 2>&1 || {
  echo "Kind context $CONTEXT does not exist; run scripts/kind-up.sh first." >&2
  exit 1
}

kubectl --context "$CONTEXT" -n cube-demo wait --for=condition=Ready cubecluster/telemetry --timeout=2m
for endpoint in readyz livez; do
  response=$(curl --fail --silent --show-error --max-time 10 "$CUBE_URL/$endpoint")
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

meta=$(curl --fail --silent --show-error --max-time 30 \
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
    echo "Kind end-to-end demo passed."
    exit 0
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

echo "Cube query did not return telemetry data after 30 attempts." >&2
cat "$response_file" >&2
exit 1
