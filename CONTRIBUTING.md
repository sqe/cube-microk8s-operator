# Contributing and collaborating

Contributions to the Cube Core Kubernetes operator are welcome. This is an
independent community project, not an official Cube product. Use issues to
discuss changes that affect the CRD, topology, ingestion contract, or supported
deployment paths before investing in a large implementation.

## Set up local checks

Use Go 1.24+, Python 3.10+, Kustomize, and the tools listed in the README's
[build and verify section](README.md#build-and-verify). Install the Git hooks
once per checkout:

```bash
python3 -m venv .venv
source .venv/bin/activate
python -m pip install pre-commit==4.7.0 PyYAML==6.0.3
pre-commit install
pre-commit run --all-files
```

The first run downloads isolated, pinned hook environments. Commits check
staged files for merge conflicts and invalid YAML, lint GitHub Actions with
actionlint **and ShellCheck**, lint changed shell scripts, and run the Kopf
resource tests when Python sources change. Kubernetes multi-document YAML is
supported. Hook installation is local; cloning the repository does not enable
hooks automatically. Reactivate `.venv` to run `pre-commit` manually later.

CI runs the same hooks across all tracked files. Keep the actionlint and
ShellCheck pins in `.pre-commit-config.yaml` in sync when updating tooling.
Hooks are quick feedback, not a replacement for the full verification below.

## Change ownership and controller parity

| Area | Source of truth |
| --- | --- |
| CubeCluster API and generated CRD | `api/v1alpha1/`, `config/crd/` |
| Default Go controller and Kubernetes resources | `internal/controller/` |
| Alternative Kopf controller and resources | `python/cube_kopf/` |
| Agent ingestion, validation, and persistence | `internal/telemetry/` |
| Semantic models and executable demo | `demo/kind/model/`, `demo/agent-executions.py` |
| Installation and runtime contracts | `config/`, `docs/`, `README.md` |
| Verification and publication | `scripts/`, `.github/workflows/` |

- Preserve equivalent Go/Kopf behavior for shared CubeCluster fields and
  topology changes. Never run both controllers against the same resources.
- For API changes, update validation, defaults, documentation, and examples;
  run `make generate` and review the generated CRD/RBAC diff.
- For ingestion or semantic-model changes, preserve the documented replay,
  attribution, and accounting contracts. Update the executable sample and
  expected query results together; do not count run totals as extra model cost.
- Keep unrelated edits out of a contribution. When collaborating in one
  checkout, agree on file ownership and never reset another person's changes.
- Do not include credentials, JWTs, private traces, or artifact contents in
  issues, fixtures, logs, screenshots, or CI artifacts. Use synthetic examples.

## Verify changes

Run the fast checks before opening a pull request:

```bash
pre-commit run --all-files
make verify
go test -race ./...
```

`make verify` checks Go formatting/tests/vet, Python tests, manifest rendering,
and Terraform/ShellCheck when those tools are installed. A local skip is not
evidence that the skipped check passed. PostgreSQL-backed tests additionally
need `TEST_DATABASE_URL` pointing to a **disposable test database**, never a
production database.

For controller, model, demo, or ingestion changes, exercise the real stack for
each controller in a separate disposable Kind cluster:

```bash
CONTROLLER=go make kind-up
make kind-e2e
CONTROLLER=go make kind-up   # repeated setup must preserve data and reload models
make kind-e2e
make kind-down

CONTROLLER=kopf make kind-up
make kind-e2e
CONTROLLER=kopf make kind-up
make kind-e2e
make kind-down
```

These commands build local images and create/delete the `cube-demo` cluster.
They verify health, model metadata, real Cube queries, synthetic agent spans,
replay/conflict handling, aggregates, and persistence after ingestion pod
replacement. See [agent executions](docs/agent-executions.md) for the contract.
If Docker or another prerequisite is unavailable, say exactly what was not run
and wait for the matching CI jobs rather than claiming end-to-end success.

## Pull requests and CI failures

Describe the user-visible outcome, compatibility implications, and checks
actually run. A bug fix should include a focused regression test where practical.
Document new behavior and update architecture diagrams when the flow changes.

CI requires static/unit/database/race checks and both Go/Kopf Kind E2E jobs
before publishing development images. Do not bypass a failing gate to ship.
For failures, include the run URL, controller, failing step, and sanitized
diagnostics from the `kind-e2e-go` or `kind-e2e-kopf` artifact, when available.
Distinguish transport timeouts, HTTP/schema errors, and incorrect query results.
Bounded startup retries must not turn persistent failures into successful tests.

## Release and licensing responsibilities

Maintainers handle version tags and publication through GitHub Actions. A
merged change, a green local test, or a published container alone does not mean
a release or OperatorHub listing is available. Release readiness requires the
CI gates and release workflow's installation checks; describe unreleased
features as unreleased until publication is confirmed. OperatorHub acceptance
is separate from publishing an OLM bundle/catalog.

Contributions are under this project's [Apache-2.0 license](LICENSE). Preserve
copyright and license notices for reused code and update [NOTICE](NOTICE) when
appropriate. Cube Client is MIT-licensed and Cube Backend is Apache-2.0-licensed;
our project license does not relicense upstream code or imply Cube endorsement.
