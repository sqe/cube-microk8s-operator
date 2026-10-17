# Cube Core MicroK8s Operator

A production-oriented Kubernetes operator for self-hosted Cube Core, with
Proxmox/MicroK8s infrastructure, selectable Go and Kopf controllers, and a
reproducible Kind telemetry demo. Go/controller-runtime is the default
production implementation. The Python/Kopf implementation is a separately
packaged, single-replica alternative for the same `CubeCluster` CRD.

> **One controller only:** never run the Go and Kopf controllers against the
> same cluster or resource at the same time. They own identical resources and
> status fields, so concurrent operation causes reconciliation races.

The API is `platform.cube.dev/v1alpha1`; test upgrades and recovery procedures
before production use.

## Contents

- [System architecture](#system-architecture)
- [Build and verify](#build-and-verify)
- [Deploy or switch controllers](#deploy-or-switch-controllers)
- [Configure Cube Core](#configure-cube-core)
- [Kind telemetry demo](#kind-telemetry-demo)
- [Use the Cube Core REST API](#use-the-cube-core-rest-api)
- [Agent integrations](#agent-integrations)
- [Storage, network, and security](#storage-network-and-security)
- [Proxmox and MicroK8s](#proxmox-and-microk8s)
- [Install with OLM or OperatorHub](#install-with-olm-or-operatorhub)
- [Troubleshooting](#troubleshooting)
- [Production checklist](#production-checklist)
- [Upstream attribution and licensing](#upstream-attribution-and-licensing)

Deeper references: [architecture and operations](docs/architecture.md),
[agent integration boundary](docs/agent-integration.md), and the
[production custom resource](config/samples/production.yaml).

## System architecture

### Proxmox, MicroK8s, operator, and Cube

Terraform preserves the original Proxmox/MicroK8s infrastructure boundary.
Application deployment remains a separate Kubernetes lifecycle.

```mermaid
flowchart TB
  Internet((Consumers)) -->|HTTPS / REST| Edge[Router / firewall / TLS ingress]
  DNS[ESDDNS-managed DNS] -. resolves .-> Edge
  Edge --> LB[MetalLB service IP]

  subgraph PVE[Proxmox VE]
    TF[Terraform + cloud-init] --> VM1[MicroK8s VM 1]
    TF --> VM2[MicroK8s VM 2]
    TF --> VM3[MicroK8s VM 3]
    VM1 <-->|dqlite HA| VM2
    VM2 <-->|dqlite HA| VM3
    VM3 <-->|dqlite HA| VM1
  end

  LB --> API[Cube API service]
  OP[Go controller-runtime<br/>or Python/Kopf] -->|reconciles| API
  OP --> RW[Refresh worker]
  OP --> CS[Cube Store router/workers]
  API --> DB[(Data source)]
  API --> CS
  RW --> DB
  RW --> CS
  CS --> RS[(S3 or shared PVC)]
```

MetalLB provides LAN reachability, not automatic public exposure. DNS, edge
port forwarding, ingress, certificates, and firewall policy remain explicit
operator responsibilities.

### Reconciliation and ownership

Both controller implementations enforce the same desired topology and owner
references. The Ready condition requires API, refresh worker, and Cube Store
replicas to be ready.

```mermaid
flowchart LR
  CR[CubeCluster] --> V{Validate references,<br/>storage, replicas}
  CM[Model ConfigMap] --> V
  SEC[Configuration Secret] --> V
  PVC[Existing PVC / S3 Secret] --> V
  V -->|valid| R[Reconcile desired objects]
  V -->|invalid| F[Ready=False<br/>InvalidConfiguration]
  R --> API[API Deployment + Service + PDB]
  R --> REF[Refresh Deployment]
  R --> STORE[Cube Store Deployment<br/>or router/worker StatefulSets]
  R --> NP[Cube Store NetworkPolicy]
  API --> S{All expected<br/>replicas ready?}
  REF --> S
  STORE --> S
  S -->|yes| Health{Cube API /readyz healthy?}
  Health -->|yes| T[Ready=True]
  Health -->|no| Unhealthy[Ready=False<br/>APIHealthCheckFailed]
  S -->|no| P[Ready=False<br/>Progressing]
  CR -. ownerReference / GC .-> API
  CR -. ownerReference / GC .-> REF
  CR -. ownerReference / GC .-> STORE
  CR -. ownerReference / GC .-> NP
```

### Controller anatomy and optional telemetry

The Go image has three independent entrypoints. Only the manager reconciles
`CubeCluster` resources; telemetry processes do not run inside reconciliation.

```mermaid
flowchart TB
  Binary[Go operator image] --> Manager[Default: controller-runtime manager]
  Binary --> Collector[telemetry-collector command]
  Binary --> Ingest[agent-ingest command]
  Kopf[Separate Kopf image<br/>one standalone replica] -. alternative to manager .-> Reconcile
  Manager --> Lease[Kubernetes Lease leader election]
  Lease --> Watch[Watch CubeClusters and owned workloads]
  Watch --> Reconcile[Validate references and desired topology]
  Reconcile --> Apply[Apply workloads and remove obsolete store topology]
  Apply --> Ready[Check replicas and Cube readiness<br/>update status and endpoint]
  Ready -. workload changes .-> Watch
  Collector -->|read-only Kubernetes API| Snapshots[Node and Pod snapshots]
  Snapshots --> PG[(PostgreSQL)]
  Producers[Instrumented agents] -->|separate ingestion token| Ingest
  Ingest --> Validate[Validate completed spans<br/>deduplicate event IDs]
  Validate --> PG
  Models[Demo model ConfigMap<br/>KubernetesTelemetry + AgentExecutions] --> Cube[Operator-managed Cube Core]
  PG --> Cube
  Cube -->|metadata and semantic queries| Clients[Clients with Cube JWT]
```

The demo supplies PostgreSQL, ingestion, collector, models, and credentials;
OLM installs the controller, not this optional telemetry stack. See
[agent-execution records and examples](docs/agent-executions.md).

### Cube query and refresh data flow

```mermaid
sequenceDiagram
  participant Client as REST client
  participant API as Cube API
  participant DB as Data source
  participant Refresh as Refresh worker
  participant Store as Cube Store
  participant Remote as S3 / shared remote storage

  Client->>API: GET /cubejs-api/v1/meta
  API-->>Client: cubes, measures, dimensions
  Client->>API: POST /cubejs-api/v1/load
  API->>Store: read pre-aggregation/cache
  alt cache or pre-aggregation hit
    Store-->>API: query result
  else source query required
    API->>DB: generated SQL
    DB-->>API: source rows
  end
  API-->>Client: JSON data
  loop refresh schedule
    Refresh->>DB: build/refresh data
    Refresh->>Store: ingest pre-aggregation
    Store->>Remote: persist metadata/data
  end
```

## Build and verify

See [Contributing and collaborating](CONTRIBUTING.md) for local Git hook setup,
controller parity, pull request checks, and CI troubleshooting. CI runs the same
pre-commit checks as local commits, including workflow lint with ShellCheck.

Prerequisites:

- Go 1.24+, Python 3 with PyYAML, Kustomize, and Terraform 1.7+.
- Install `demo/requirements-mcp.txt` in your Python environment for Kind E2E
  and MCP execution demos: `python -m pip install -r demo/requirements-mcp.txt`.
- Docker, Kind, kubectl, curl, and jq for the demo.
- Either an amd64 or Apple Silicon arm64 Docker host. `kind-up.sh` detects the
  Kind node architecture and selects the matching digest-pinned Cube Store
  image (`v1.7.20` for amd64, `arm64v8` for arm64).

```bash
make verify                 # Go tests/vet, Python tests, manifests, TF, shellcheck
make build                  # Go production-default binary
make test                   # Go and focused Kopf resource tests
make manifests-go           # render default Go installation
make manifests-kopf         # render optional Kopf installation

docker build -t cube-operator:v0.1.0 .
docker build -f python/Dockerfile -t cube-operator-kopf:v0.1.0 .
```

Cube and Cube Store runtime defaults are versioned and digest-pinned. The Go
operator release is already published as `ghcr.io/sqe/cube-operator:v0.1.0`;
use the [verified OLM installation](#install-with-olm-or-operatorhub) without
building or publishing anything yourself. The local tags above identify your
own builds; only custom builds need publishing to your registry before use
outside Kind.

### Automated verification and packages

CI runs Go/Python checks, PostgreSQL-backed ingestion tests, and Kind E2E tests
for both controllers, including repeated setup and ingestion pod replacement.
Images are built for amd64/arm64. Development images are available as
`ghcr.io/sqe/cube-operator:edge` and `ghcr.io/sqe/cube-operator-kopf:edge`;
use a versioned release for reproducible installations rather than a moving tag.
The agent-execution feature requires a build from these changes until its next
release is published; the existing v0.1.0 release does not include it.

```mermaid
flowchart LR
  Source[PR or main] --> Checks[Go and Python checks<br/>PostgreSQL tests + race detector<br/>workflow lint]
  Checks --> E2E[Kind matrix: Go and Kopf<br/>real ingestion to Cube queries<br/>pod replacement + repeated setup]
  E2E --> Builds[amd64 and arm64 builds]
  Builds --> Dev[Main only: development packages]
  Tag[Next release: v0.2.0 tag] --> ReleaseE2E[Same Go and Kopf E2E gates]
  ReleaseE2E --> Published[Versioned Go and Kopf images<br/>Go OLM bundle and catalog]
  Published --> OLM[Anonymous pulls and clean OLM installation]
  OLM --> Release[GitHub alpha prerelease<br/>digests + install manifest + checksums]
```

## Deploy or switch controllers

### Deploy Go (default)

Set the registry image in an overlay or update `config/default/kustomization.yaml`,
then render and review before applying:

```bash
kustomize build config/default > /tmp/cube-operator-go.yaml
kubectl apply --server-side --dry-run=server -f /tmp/cube-operator-go.yaml
kubectl apply -f /tmp/cube-operator-go.yaml
kubectl -n cube-system rollout status deployment/cube-operator
```

The Go manager uses Kubernetes Lease leader election and is the recommended
production path.

### Deploy Kopf instead

First stop the Go manager; wait for deletion before creating Kopf:

```bash
kubectl -n cube-system delete deployment cube-operator --ignore-not-found --wait=true
kustomize build config/kopf > /tmp/cube-operator-kopf.yaml
kubectl apply --server-side --dry-run=server -f /tmp/cube-operator-kopf.yaml
kubectl apply -f /tmp/cube-operator-kopf.yaml
kubectl -n cube-system rollout status deployment/cube-operator-kopf
```

Kopf runs one replica with `--standalone`. It does not provide the Go manager's
multi-replica leader-election path.

### Switch safely back to Go

```bash
kubectl -n cube-system delete deployment cube-operator-kopf --ignore-not-found --wait=true
kubectl apply -f /tmp/cube-operator-go.yaml
kubectl -n cube-system rollout status deployment/cube-operator
kubectl -n cube-system get deploy cube-operator cube-operator-kopf --ignore-not-found
```

```mermaid
stateDiagram-v2
  [*] --> NoController
  NoController --> GoActive: apply config/default
  NoController --> KopfActive: apply config/kopf
  GoActive --> NoController: delete Go Deployment and wait
  KopfActive --> NoController: delete Kopf Deployment and wait
  GoActive --> GoActive: leader-elected reconciliation
  KopfActive --> KopfActive: standalone reconciliation
  state "FORBIDDEN: reconciliation race" as GoAndKopf
  GoActive --> GoAndKopf: do not apply Kopf concurrently
  KopfActive --> GoAndKopf: do not apply Go concurrently
```

## Configure Cube Core

### Namespace, model, and configuration Secret

The operator reads references but never creates credentials. The following
creates a minimal PostgreSQL configuration without committing a Secret:

```bash
kubectl create namespace cube --dry-run=client -o yaml | kubectl apply -f -
kubectl -n cube create configmap cube-model \
  --from-file=KubernetesTelemetry.yml=demo/kind/model/KubernetesTelemetry.yml \
  --dry-run=client -o yaml | kubectl apply -f -

read -rsp 'Database password: ' CUBE_DB_PASS; echo
read -rsp 'Cube API signing secret: ' CUBE_API_SECRET; echo
kubectl -n cube create secret generic cube-configuration \
  --from-literal=CUBEJS_DB_TYPE=postgres \
  --from-literal=CUBEJS_DB_HOST=postgres.example.internal \
  --from-literal=CUBEJS_DB_PORT=5432 \
  --from-literal=CUBEJS_DB_NAME=analytics \
  --from-literal=CUBEJS_DB_USER=cube \
  --from-literal=CUBEJS_DB_PASS="$CUBE_DB_PASS" \
  --from-literal=CUBEJS_API_SECRET="$CUBE_API_SECRET" \
  --dry-run=client -o yaml | kubectl apply -f -
unset CUBE_DB_PASS CUBE_API_SECRET
```

Use External Secrets, Sealed Secrets, or your platform's secret manager in
production. Do not put values in Git, shell history, Terraform variables, or
shared state.

### Production S3 example

Prefer workload identity when available. If static S3 credentials are required,
create the referenced Secret out of band:

```bash
kubectl -n cube create secret generic cubestore-s3 \
  --from-literal=CUBESTORE_AWS_ACCESS_KEY_ID="$AWS_ACCESS_KEY_ID" \
  --from-literal=CUBESTORE_AWS_SECRET_ACCESS_KEY="$AWS_SECRET_ACCESS_KEY" \
  --dry-run=client -o yaml | kubectl apply -f -
```

```yaml
apiVersion: platform.cube.dev/v1alpha1
kind: CubeCluster
metadata: {name: analytics, namespace: cube}
spec:
  image: cubejs/cube:v1.7.20@sha256:7f14f4be9f3303afe48a16584480c8a9dc15f44c13daf66c2a5b31019025b71a
  modelConfigMap: cube-model
  configurationSecret: cube-configuration
  api: {replicas: 2}
  refreshWorker: {replicas: 1}
  cubeStore:
    mode: clustered
    image: cubejs/cubestore:v1.7.20@sha256:cd5fe68049204640704a6412a39e7a09eb391fc70890577dd21b5480d85cb219
    workers: 2
    remoteStorage:
      type: s3
      s3: {bucket: company-cube-prod, region: us-west-2, secretRef: cubestore-s3}
    scratch: {size: 100Gi}
  service: {type: LoadBalancer}
```

Tune resources from observed workload usage; the fuller starting point is
[`config/samples/production.yaml`](config/samples/production.yaml).

### Existing PVC example

PVC remote storage is most appropriate for a single Cube Store. A clustered
topology requires a genuinely shared, concurrently mountable filesystem; a
node-local or ordinary `ReadWriteOnce` claim is not sufficient.

```yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata: {name: cubestore-remote, namespace: cube}
spec:
  accessModes: [ReadWriteOnce]
  storageClassName: durable-block
  resources: {requests: {storage: 100Gi}}
---
apiVersion: platform.cube.dev/v1alpha1
kind: CubeCluster
metadata: {name: analytics, namespace: cube}
spec:
  modelConfigMap: cube-model
  configurationSecret: cube-configuration
  api: {replicas: 2}
  refreshWorker: {replicas: 1}
  cubeStore:
    mode: single
    remoteStorage:
      type: persistentVolume
      persistentVolume: {existingClaim: cubestore-remote}
    scratch: {size: 20Gi}
  service: {type: ClusterIP}
```

## Kind telemetry demo

The disposable demo samples Kubernetes Nodes and Pods and accepts agent-execution
telemetry through a separate authenticated ingestion service. Both are stored in
PostgreSQL and queried through Cube Core. The `AgentExecutions` model covers
agent runs, versioned skills, tool calls, handoffs, artifact references, validation
outcomes, and model/token/cost usage. Agents must emit their own execution records;
the operator does not inspect agents or collect private reasoning.

See [the agent-execution contract and examples](docs/agent-executions.md).

```mermaid
flowchart LR
  KAPI[Kubernetes API] -->|read-only list Nodes/Pods| Collector[Telemetry collector]
  Collector -->|COPY observations| PG[(PostgreSQL)]
  Agents[Instrumented agents] -->|completed spans + ingestion token| Ingest[Agent ingestion service]
  Ingest -->|validated records| PG
  Model[Generated model ConfigMap] --> Cube[Cube API]
  PG --> Cube
  Cube --> Store[Single Cube Store + PVC]
  Test[kind-e2e.sh] -->|/readyz + /livez| Cube
  Test -->|GET /cubejs-api/v1/meta| Cube
  Test -->|POST /cubejs-api/v1/load| Cube
  Cube -->|telemetry count| Test
```

Scripts always pass the `kind-cube-demo` context, generate credentials in
Kubernetes Secrets, reuse an existing cluster safely, and select an amd64 or
arm64 digest based on `.status.nodeInfo.architecture`.

```bash
make kind-up              # Go controller (default)
make kind-e2e
make kind-down

make kind-up-kopf         # same demo, Kopf controller
make kind-e2e
make kind-down
```

`kind-e2e` verifies `/readyz`, `/livez`, `/cubejs-api/v1/meta`, and
`POST /cubejs-api/v1/load`, including bounded retries for `Continue wait`.
It also submits a synthetic multi-agent execution and verifies its linked records,
exact usage/cost and outcome aggregates, ingestion authentication, validation,
and replay safety. No Temporal or Kafka service is required.
Port 4000 is bound locally for the lifetime of the Kind cluster.

## Use the Cube Core REST API

Health endpoints are unprefixed and unauthenticated. Core Data API calls use a
Cube JWT directly in `Authorization`—there is no `Bearer` prefix.

```bash
kubectl -n cube port-forward service/analytics-api 4000:4000
# In another shell:
export CUBE_CORE_URL=http://127.0.0.1:4000
curl --fail --silent "$CUBE_CORE_URL/readyz" | jq .
curl --fail --silent "$CUBE_CORE_URL/livez" | jq .
```

For this example, mint a five-minute HS256 JWT from the Kubernetes Secret. Avoid
putting production secrets or tokens on command lines or in logs:

```bash
API_SECRET=$(kubectl -n cube get secret cube-configuration \
  -o 'go-template={{index .data "CUBEJS_API_SECRET" | base64decode}}')
export CUBE_CORE_TOKEN=$(CUBE_API_SECRET="$API_SECRET" python3 <<'PY'
import base64, hashlib, hmac, json, os, time
enc = lambda b: base64.urlsafe_b64encode(b).rstrip(b"=")
head = enc(json.dumps({"alg":"HS256","typ":"JWT"}, separators=(",", ":")).encode())
body = enc(json.dumps({"iat":int(time.time()),"exp":int(time.time())+300}, separators=(",", ":")).encode())
signed = head + b"." + body
print((signed + b"." + enc(hmac.new(os.environ["CUBE_API_SECRET"].encode(), signed, hashlib.sha256).digest())).decode())
PY
)
unset API_SECRET
```

Inspect metadata, then query only member names returned by metadata:

```bash
curl --fail --silent --show-error --max-time 30 \
  -H "Authorization: $CUBE_CORE_TOKEN" \
  "$CUBE_CORE_URL/cubejs-api/v1/meta" | jq '.cubes[].name'

curl --fail --silent --show-error --max-time 95 \
  -H "Authorization: $CUBE_CORE_TOKEN" \
  -H 'Content-Type: application/json' \
  --data '{"query":{"measures":["KubernetesTelemetry.count"],"limit":10}}' \
  "$CUBE_CORE_URL/cubejs-api/v1/load" | jq .

unset CUBE_CORE_TOKEN
```

### REST operational contract

```mermaid
flowchart LR
  Health[Unauthenticated health] --> Ready[/readyz/]
  Health --> Live[/livez/]
  Token[Short-lived Cube JWT<br/>Authorization: TOKEN] --> Core[Self-hosted Core Data API]
  Core --> Meta[/cubejs-api/v1/meta/]
  Core --> Load[/cubejs-api/v1/load/]
  Load --> Retry{Continue wait?}
  Retry -->|yes, bounded delay| Load
  Retry -->|no| Result[Consume .data or report .error]
```

## Agent integrations

### Agent-execution telemetry

Instrument your agents to send completed spans to `POST /v1/executions` on the
optional `agent-ingest` service. Query `AgentExecutions` through Cube to see which
agents ran, which skills/tools they used, delegation, artifact references,
validation results, latency, and reported token/cost usage. This is separate from
the read-only Cube skill below. See [setup, schema, and queries](docs/agent-executions.md).
The shared-token Kind demo is not a multi-tenant authorization system; OIDC and
tenant enforcement must be configured separately before external use.

### Real weather agent: MCP execution to SQL and BI

The [MCP execution demo](demo/mcp_execution.py) ran a separate weather agent's
real `weather_current` tool through MCP and queried its persisted spans through
Cube. On October 17, 2026, Open-Meteo returned Reykjavik conditions of **9.7 °C,
91% humidity, and 45.0 km/h wind** (observation time 20:45 local Reykjavik time).
This was a host-executed agent tool call, not a synthetic response, Kafka task,
or LLM inference. PostgreSQL and Cube agreed on these execution metrics:

| Agent | Spans | Runs | Failed runs | Run duration | Model tokens / reported cost |
| --- | ---: | ---: | ---: | ---: | --- |
| weather | 3 | 1 | 0 | 2160.206 ms | 0 / $0 |

The captured trace was `mcp-bb768566719a424aba21b58af46a6c8b`. It contains
successful **run**, **tool**, and **artifact-reference** records. Results and
timings vary between runs; no model usage is invented for a non-LLM agent.

Execution fields are persisted in PostgreSQL; the raw weather response is a
local JSON artifact, not weather-observation SQL columns. The YAML semantic
model defines Cube measures/dimensions, and Cube generates SQL against those
rows. For example, this audit query returns the run count and average latency:

```sql
SELECT agent_name,
       count(*) FILTER (WHERE kind = 'run') AS runs,
       avg(duration_ms) FILTER (WHERE kind = 'run') AS average_run_ms
FROM public.agent_executions
WHERE trace_id = 'mcp-bb768566719a424aba21b58af46a6c8b'
GROUP BY agent_name;
```

BI applications can use the same definitions through Cube's REST API:

```json
{
  "query": {
    "dimensions": ["AgentExecutions.agent"],
    "measures": ["AgentExecutions.runs", "AgentExecutions.averageRunDurationMs"],
    "filters": [{"member": "AgentExecutions.traceId", "operator": "equals",
      "values": ["mcp-bb768566719a424aba21b58af46a6c8b"]}]
  }
}
```

See [reproduction, result artifacts, SQL auditing, generated SQL, and BI queries](docs/weather-mcp-demo.md).
No pre-aggregations are declared in this demo; model definitions are not
automatically materialized SQL tables. The private weather implementation is
not redistributed here. Public CI tests the same MCP → ingestion → Cube path
with a clearly labelled fixture instead of depending on private source or a
live weather provider. This does not instrument the entire running agent fleet.

### Example: harvesting a complete agent task for BI

The [E2E producer](demo/agent-executions.py) submits the following **synthetic**
task through the real ingestion → PostgreSQL → Cube path. It demonstrates the
captured records; it does not execute these agents, call a real model, or create
the referenced report. Actual agents must instrument their own runtime.

| Agent | Span kind | Captured work and outcome | Parent |
|---|---|---|---|
| `analytics-worker` | `run` | Succeeded in 20 seconds | Root |
| `analytics-worker` | `skill` | `operating-cube-core`, version `1.0.0`, 8 seconds | Worker run |
| `analytics-worker` | `tool` | `Cube query` via `http`, succeeded in 2 seconds | Skill |
| `analytics-worker` | `model` | `synthetic` / `demo-model`: 137 input + 41 output tokens, $0.0123 | Worker run |
| `analytics-worker` | `artifact` | JSON report reference: `urn:demo:<traceId>:report` | Worker run |
| `analytics-worker` | `handoff` | Delegated to `reviewer` | Worker run |
| `reviewer` | `run` | Failed in 6 seconds; `errorType = validation_failed` | Handoff |
| `reviewer` | `model` | `synthetic` / `demo-model`: 23 input + 7 output tokens, $0.0045 | Reviewer run |
| `reviewer` | `validation` | Failed check, 3 seconds | Reviewer run |

All nine records share a `traceId`. Each has its own `eventId`; `runId` identifies
the worker or reviewer execution, and `parentId` links the graph above. Common
dimensions include tenant, cluster, namespace, name, status, and timestamps.
Artifact URIs are references only: the service does not store or fetch reports,
SQL files, patches, screenshots, or other artifact contents.

Reproduce the harvest with the new source build:

```bash
make kind-up
make kind-e2e
```

The test prints a fresh trace ID and verifies this normalized summary through
Cube queries, both before and after replacing the ingestion pod:

```json
{
  "count": 9,
  "runs": 2,
  "failedRuns": 1,
  "failedValidations": 1,
  "averageRunDurationMs": 13000,
  "inputTokens": 160,
  "outputTokens": 48,
  "costUsd": 0.0168
}
```

Inspect `/cubejs-api/v1/meta` first, then POST this query to
`/cubejs-api/v1/load` with a Cube JWT directly in `Authorization`. Replace the
trace placeholder with the ID printed by your run; retained demo records expire
after seven days by default.

```json
{
  "query": {
    "measures": [
      "AgentExecutions.runs",
      "AgentExecutions.failedRuns",
      "AgentExecutions.averageRunDurationMs",
      "AgentExecutions.inputTokens",
      "AgentExecutions.outputTokens",
      "AgentExecutions.costUsd"
    ],
    "dimensions": ["AgentExecutions.agent"],
    "filters": [{
      "member": "AgentExecutions.traceId",
      "operator": "equals",
      "values": ["TRACE_ID_FROM_KIND_E2E"]
    }],
    "limit": 100
  }
}
```

That separates worker and reviewer outcomes and model usage. For the execution
audit trail, query `eventId`, `runId`, `parentId`, `kind`, `name`, and `status`
with the same trace filter. Add `skillVersion`, `tool`, `targetAgent`,
`artifactUri`, `artifactType`, and `validation` to inspect the actual evidence.
Use `kind = skill` when comparing skill names/versions across runs. Cube
responses use qualified member names and commonly return numbers as strings;
convert them deliberately when building dashboards. Overlapping child spans
are not summed into run latency, and only model-call spans carry usage/cost.

### Local self-hosted Cube Core skill

The project-local
[`operating-cube-core` skill](.agents/skills/operating-cube-core/SKILL.md)
uses only the self-hosted Core Data REST API. Provide `CUBE_CORE_URL` and a
short-lived `CUBE_CORE_TOKEN`, then use prompts such as:

- “Use operating-cube-core to verify readiness and list available cubes.”
- “Inspect metadata first, then query Kubernetes telemetry counts by namespace.”
- “Diagnose this `Continue wait` response with bounded retries; do not print my token.”

It intentionally cannot deploy Cube, edit model files, administer users, or
manage Cube Cloud content.

### Optional official Cube Cloud skills

The official [`cube-js/cube-agent-skills`](https://github.com/cube-js/cube-agent-skills)
drive the current `cube` CLI and Cube Cloud platform APIs. They **do not operate
local Cube Core**. Use them only for a separate Cube Cloud tenant:

```bash
# Agent Skills-compatible clients; review upstream instructions before install.
npx skills add cube-js/cube-agent-skills

# Install the official Cube CLI, optionally pinning CUBE_VERSION in automation.
curl -fsSL https://raw.githubusercontent.com/cube-js/cube/master/install-cli.sh | sh
cube login --url https://TENANT.cubecloud.dev

# Headless Cloud alternative:
export CUBE_API_URL=https://TENANT.cubecloud.dev
export CUBE_API_KEY='sk-...'
cube deployments list
```

Do not point `CUBE_API_URL` at the local service expecting Cloud control-plane
operations.

```mermaid
flowchart TB
  Agent[Coding agent]
  Agent --> LocalSkill[Project operating-cube-core skill]
  LocalSkill -->|Core JWT| LocalAPI[Self-hosted Cube Core<br/>meta + load only]
  Agent --> Official[Official cube-agent-skills]
  Official --> CLI[Cube CLI]
  CLI -->|Cloud API key / login| Cloud[Cube Cloud platform APIs]
  LocalAPI -. no Cloud control plane .- Cloud
```

See [agent integration](docs/agent-integration.md) for the exact boundary.

## Storage, network, and security

```mermaid
flowchart TB
  subgraph Public[External trust boundary]
    User[API client] -->|TLS| Ingress[Ingress / LoadBalancer]
  end
  subgraph Namespace[Cube namespace]
    Ingress --> API[Cube API :4000]
    Secret[Configuration Secret] --> API
    Secret --> Refresh[Refresh worker]
    Config[Model ConfigMap] --> API
    Config --> Refresh
    API -->|NetworkPolicy allowed| Store[Cube Store :3030/:9001/:9999]
    Refresh -->|NetworkPolicy allowed| Store
    Store --> Scratch[(Ephemeral /cube/data<br/>size-limited emptyDir)]
  end
  Store -->|restricted egress| S3[(S3 shared durable storage)]
  Store -->|alternative| SharedPVC[(Existing shared PVC)]
  API -->|restricted egress| DB[(Data source)]
  Refresh -->|restricted egress| DB
  Operator[Controller SA] -->|write owned workloads| Namespace
  Operator -->|read references only| Secret
```

- Cube Store has no external Service. Its ingress NetworkPolicy allows only
  pods belonging to the same Cube instance.
- Scratch space is ephemeral `emptyDir`; `scratch.size` is a limit, not durable
  capacity or a reservation.
- The operator does not provision, encrypt, or back up remote storage.
- Containers use RuntimeDefault seccomp, drop capabilities, and disable
  privilege escalation where compatible. Add admission policy and image
  verification for your environment.
- Add default-deny egress with explicit DNS, database, S3, and monitoring
  destinations; terminate TLS at a controlled ingress.
- RBAC permits writes to owned workload kinds, reads of referenced
  ConfigMaps/Secrets/PVCs, events, and Leases for Go leader election.

## Proxmox and MicroK8s

```bash
cp terraform.tfvars.example terraform.tfvars
export TF_VAR_proxmox_api_token='terraform@pve!cube=REPLACE_ME'
terraform init
terraform validate
terraform plan -out=tfplan
terraform apply tfplan

export SSH_PRIVATE_KEY="$HOME/.ssh/id_ed25519"
export METALLB_ADDRESS_POOL="192.168.1.200-192.168.1.220"
./scripts/bootstrap-microk8s.sh
export KUBECONFIG="$PWD/kubeconfig"
kubectl get nodes -o wide
```

Bootstrap waits for cloud-init, joins missing nodes, enables DNS, hostpath
storage, Helm and optional MetalLB, then writes a mode-0600 kubeconfig. Three
nodes enable MicroK8s HA. VM deletion protection defaults on. Before deliberate
destruction, back up data, set `protect_vms=false`, apply, and review a destroy
plan. Hostpath volumes are not durable across node loss.

## Install with OLM or OperatorHub

**OLM is ready to use:** the v0.1.0 **alpha** controller, bundle, and catalog
images are published publicly on GHCR. The
[successful release verification](https://github.com/sqe/cube-microk8s-operator/actions/runs/39060124635)
confirmed anonymous pulls and installation through the catalog on a clean
Kubernetes 1.34 cluster with OLM 0.46: the CSV reached `Succeeded` and the
controller became ready. Follow the OLM installation steps below.

**OperatorHub listing:** [submission #9651](https://github.com/k8s-operatorhub/community-operators/pull/9651)
is open; the listing is not available until community review, acceptance, and
catalog ingestion complete. The OLM catalog installation already works.

### OLM catalog installation

Use Kubernetes 1.34+ with **classic OLM** (`CatalogSource` and `Subscription`,
not OLM v1). This installs the Go controller in `cube-system` and watches all
namespaces. Do not install it alongside a directly deployed Go or Kopf
controller. Remove the previous installation's controller and RBAC before
handing ownership to OLM; do not delete the CRD or existing CubeClusters.

If OLM is not already installed, install it once with
[Operator SDK](https://sdk.operatorframework.io/docs/installation/):

```bash
operator-sdk olm install --version 0.46.0 --timeout 5m
```

Run from a checkout of this repository:

```bash
# CatalogSource + cluster-wide OperatorGroup + alpha Subscription.
kubectl apply -f config/olm/install.yaml
kubectl -n cube-system wait --for=create \
  csv/cube-microk8s-operator.v0.1.0 --timeout=5m
kubectl -n cube-system wait --for=jsonpath='{.status.phase}'=Succeeded \
  csv/cube-microk8s-operator.v0.1.0 --timeout=5m
kubectl -n cube-system rollout status deployment/cube-operator --timeout=2m
```

The manifest uses `ghcr.io/sqe/cube-operator-catalog:v0.1.0`, package
`cube-microk8s-operator`, and channel `alpha`. It assumes the OLM global catalog
namespace is `olm`; adapt `metadata.namespace` on the CatalogSource and
`spec.sourceNamespace` on the Subscription if yours differs. The Subscription
automatically approves installation; use `installPlanApproval: Manual` and
review/approve its InstallPlan if required by your change-control policy.
Do not create a second OperatorGroup in an existing operator namespace.

Next, [create the model, configuration Secret, and storage](#configure-cube-core),
adapt `config/samples/production.yaml` to your environment, and apply it:

```bash
kubectl apply -f config/samples/production.yaml
kubectl -n cube get cubecluster analytics
```

For installation failures, inspect `kubectl -n olm get catalogsource,pods` and
`kubectl -n cube-system get subscription,installplan,csv`.

### OperatorHub installation

**Submission in progress:** [PR #9651](https://github.com/k8s-operatorhub/community-operators/pull/9651)
is ready for community CI and review. It is not an accepted OperatorHub listing yet.
Once the community submission is accepted and appears in the public
catalog, find **Cube Core Operator** on [OperatorHub.io](https://operatorhub.io),
select **Install**, and follow that listing's generated commands. Use the
`alpha` channel and all-namespaces install mode. Do not install the custom
catalog Subscription as well; both routes install the same controller.

Verify the installed CSV and controller, then configure Cube Core as above.
OperatorHub.io is separate from OpenShift's community catalog; an OpenShift
console listing requires its own submission and validation.

## Troubleshooting

| Symptom | Checks and corrective action |
|---|---|
| `Ready=False, InvalidConfiguration` | Run `kubectl describe cubecluster -n NAMESPACE NAME`; verify the model ConfigMap, configuration Secret, PVC/S3 fields, replica counts, and scratch quantity. |
| `Ready=False, Progressing` | Inspect the condition message, then `kubectl get pods -n NAMESPACE` and logs for API, refresh, and Cube Store. Every component must be ready. |
| API probe fails | Call `/readyz` and `/livez`; readiness includes the default data-source connection. Verify Secret values, DNS, NetworkPolicy, and database TLS. |
| Cube Store worker never becomes ready | Workers are probed on TCP 9001; routers/single stores on 3030. Check selected image architecture and Cube Store environment. |
| `exec format error` in Kind | Re-run `kind-up.sh`; it patches `arm64v8@sha256:d925…` for arm64 and `v1.7.20@sha256:cd5…` for amd64. Confirm with `kubectl -n cube-demo get cube telemetry -o jsonpath='{.spec.cubeStore.image}'`. |
| REST returns `403` | Supply a valid Cube JWT directly in `Authorization` without `Bearer`; check expiry and signing secret. |
| REST returns `Continue wait` | Retry the identical request with a bounded delay; inspect API/refresh/Cube Store logs if it never resolves. |
| Controller fields flap | Ensure only one of `cube-operator` and `cube-operator-kopf` exists. Stop both, then deploy exactly one. |
| Kind port 4000 is busy | Stop the conflicting process or delete the existing Kind cluster with `make kind-down`; the demo mapping is intentionally fixed. |
| Terraform destroy is blocked | Protection is intentional. Back up data, set and apply `protect_vms=false`, then review `terraform plan -destroy`. |

Useful commands:

```bash
kubectl get cubeclusters -A
kubectl -n cube get cubecluster analytics -o yaml
kubectl -n cube get deploy,statefulset,service,pdb,networkpolicy
kubectl -n cube logs deployment/analytics-api
kubectl -n cube-system logs deployment/cube-operator       # Go
kubectl -n cube-system logs deployment/cube-operator-kopf  # Kopf
```

## Production checklist

This is a per-environment operations checklist, not a list of unfinished
release tasks. The alpha OLM images are already public and installation-tested;
production suitability, credentials, storage, and recovery still need validation
for your own deployment.

- [ ] Keep the Go controller as the default unless Kopf's one-replica tradeoff
      is deliberate; verify exactly one controller is active.
- [ ] Use the published operator release pinned to an immutable digest; publish
      to your own trusted registry only if you maintain a custom build.
- [ ] Keep Cube/Cube Store images digest-pinned and test upgrades in staging.
- [ ] Use at least two API replicas and size API, refresh, and Cube Store from
      observed CPU, memory, queue, and query behavior.
- [ ] Use clustered Cube Store plus shared durable remote storage for meaningful
      production concurrency; verify backup and restore.
- [ ] Supply credentials through an external secret manager or workload identity;
      rotate database, API signing, S3, DNS, and Proxmox credentials.
- [ ] Use TLS ingress, restricted external exposure, default-deny policies, and
      explicit egress to DNS/database/S3/monitoring.
- [ ] Add topology spread/anti-affinity and confirm the PodDisruptionBudget fits
      node-maintenance and failure requirements.
- [ ] Monitor `/readyz`, `/livez`, workload readiness, restarts, query latency,
      refresh jobs, Cube Store capacity, and remote-storage failures.
- [ ] Back up Kubernetes state, Cube Store remote data, source data as required,
      Terraform state, and recovery credentials; rehearse restore.
- [ ] Validate CRD/controller upgrades and rollback with representative models
      before production rollout.

## Upstream attribution and licensing

This project uses open-source software from
**[Cube](https://github.com/cube-js/cube)**. Credit for Cube Core, its client
libraries, backend, and Cube Store belongs to the upstream Cube authors and
contributors. This operator deploys the upstream `cubejs/cube` and
`cubejs/cubestore` container images and integrates with Cube's REST API; the
Go and Kopf operator images are built separately from this repository.

As documented in [Cube's v1.7.20 license statement](https://github.com/cube-js/cube/blob/v1.7.20/README.md#license):

- **Cube Client:** [MIT License](https://github.com/cube-js/cube/blob/v1.7.20/packages/cubejs-client-core/LICENSE).
- **Cube Backend:** [Apache License 2.0](https://github.com/cube-js/cube/blob/v1.7.20/packages/cubejs-server/LICENSE).

Those licenses apply to the respective upstream components, not automatically
to this operator's original code. Preserve applicable upstream copyright,
license, and NOTICE files when redistributing Cube code or modified images;
other dependencies retain their own licenses. This is an independent project,
not an official Cube product or a claim of Cube endorsement.

**Operator license:** the original code in this repository is licensed under
the [Apache License 2.0](LICENSE), copyright 2026 Aziz Kurbanov. See [NOTICE](NOTICE)
for attribution. This does not relicense Cube or other third-party components.

**Maintainer:** Aziz Kurbanov — [azizbek@gmail.com](mailto:azizbek@gmail.com).
