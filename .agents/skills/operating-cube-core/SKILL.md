---
name: operating-cube-core
description: "Inspects health, metadata, and query results from a self-hosted Cube Core REST API. Use when operating or debugging local/on-prem Cube Core, not Cube Cloud administration."
compatibility: Requires curl and jq plus CUBE_CORE_URL; authenticated API calls require CUBE_CORE_TOKEN.
---

# Operating Cube Core

Operate self-hosted Cube Core through its Core Data REST API. This skill is
read-only by default. It does not use the Cube CLI or Cube Cloud control plane.

## Safety rules

1. Require `CUBE_CORE_URL`; remove its trailing slash when composing paths.
2. Require `CUBE_CORE_TOKEN` for metadata and queries. Never print the token.
3. Send the Core API JWT directly as `Authorization: $CUBE_CORE_TOKEN` without
   a `Bearer` prefix.
4. Use `curl --fail --silent --show-error` with an explicit timeout. Show a
   redacted response error, not request headers.
5. Start with metadata and validate member names before querying. Apply a small
   `limit` to exploratory detail queries.
6. Treat `Continue wait` as a retryable response with bounded retries and a
   delay. Do not loop forever.
7. Do not claim this API can deploy Cube, mutate model files, administer users,
   or manage Cloud content. Make model changes in source control and use this
   repository's deployment process.

## Establish the endpoint

```bash
: "${CUBE_CORE_URL:?set CUBE_CORE_URL}"
CUBE_CORE_URL=${CUBE_CORE_URL%/}
```

For the Kind demo only, use `http://127.0.0.1:4000`. For a cluster deployment,
prefer its TLS ingress URL. An in-cluster caller can use the
`CubeCluster.status.endpoint` Service URL.

## Check health

Health endpoints are unprefixed and do not need a token:

```bash
curl --fail --silent --show-error --max-time 10 "$CUBE_CORE_URL/readyz" | jq -e '.health == "HEALTH"'
curl --fail --silent --show-error --max-time 10 "$CUBE_CORE_URL/livez"  | jq -e '.health == "HEALTH"'
```

`/readyz` tests startup and the default data-source connection. `/livez` tests
the health of existing data-source connections.

## Inspect the semantic model

```bash
: "${CUBE_CORE_TOKEN:?set a short-lived Cube API JWT}"
curl --fail --silent --show-error --max-time 30 \
  -H "Authorization: $CUBE_CORE_TOKEN" \
  "$CUBE_CORE_URL/cubejs-api/v1/meta" | jq '.cubes[] | {name, measures: [.measures[].name], dimensions: [.dimensions[].name]}'
```

Use exact member names from this response. Do not use Cube Cloud-only Metadata
API endpoints such as `/v1/entities` against local Core.

## Run a query

Prefer POST to avoid URL-length limits:

```bash
curl --fail --silent --show-error --max-time 95 \
  -H "Authorization: $CUBE_CORE_TOKEN" \
  -H 'Content-Type: application/json' \
  --data '{"query":{"measures":["KubernetesTelemetry.count"],"limit":10}}' \
  "$CUBE_CORE_URL/cubejs-api/v1/load" | jq .
```

Numerical values are commonly strings; parse them deliberately. Check `.error`
before consuming `.data`. For `Continue wait`, retry the same request up to a
declared limit (for example 20 attempts, five seconds apart), then report the
last response.

## Cube Cloud is separate

The official `cube-js/cube-agent-skills` invoke the current `cube` CLI, which
operates the Cube Cloud platform. Use those only for an explicitly configured
Cube Cloud tenant. They do not operate this local Cube Core deployment. See
`docs/agent-integration.md` for the optional Cloud flow.
