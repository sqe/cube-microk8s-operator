# Cube agent integration

## Self-hosted Cube Core

This repository's [`operating-cube-core`](../.agents/skills/operating-cube-core/SKILL.md)
skill uses the Cube Core Data REST API directly. Set a base URL and a short-lived
Cube API JWT, then ask a compatible coding agent to inspect metadata or run a
query:

```bash
export CUBE_CORE_URL=https://cube.example.com
export CUBE_CORE_TOKEN='short-lived-jwt'
```

The token is sent directly in `Authorization` (not `Bearer`). Health checks are
unprefixed and unauthenticated; metadata and data use
`/cubejs-api/v1/meta` and `/cubejs-api/v1/load`. Keep production tokens out of
prompts, logs, shell history, source control, and artifacts. The skill defaults
to read-only discovery/query behavior and does not pretend that the Core Data
API can manage deployments or edit model files.

## Optional Cube Cloud flow

The official [`cube-js/cube-agent-skills`](https://github.com/cube-js/cube-agent-skills)
drive the current Cube CLI for Cube Cloud platform resources such as deployments,
branches, workbooks, agents, and administration. Cube's CLI documentation states
that it works with the Cube cloud platform and is not required for local Cube
Core. Therefore those skills and the CLI are **not** a local operator interface.

For a separate Cube Cloud account, follow the official project and authenticate
to the tenant:

```bash
# Install using the official, reviewed instructions and pin a CLI release in CI.
cube login --url https://TENANT.cubecloud.dev
# Headless alternative:
export CUBE_API_URL=https://TENANT.cubecloud.dev
export CUBE_API_KEY='sk-...'
```

Do not point `CUBE_API_URL` at this self-hosted service expecting Cloud platform
operations to work. A semantic model can be compatible between Cube Core and
Cube Cloud, but deployment/control-plane APIs are distinct from Core's Data REST
API.
