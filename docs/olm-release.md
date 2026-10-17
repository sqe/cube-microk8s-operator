# OLM release guide (maintainers)

User installation instructions live in the [README](../README.md#install-with-olm-or-operatorhub).
OLM consumes registry images; there is no separate "push to OLM" service.
Publishing a custom catalog does **not** publish an OperatorHub listing.

## Publish with GitHub Actions (recommended)

Push the code to GitHub, then push the `v0.1.0` tag pointing to that code, or
select **Actions → Publish OLM release → Run workflow** after it is on the
default branch. The workflow deliberately publishes only the initial v0.1.0
release; it does not pretend to support unreviewed upgrade graphs.

No registry secret or personal access token is required. The workflow uses
`GITHUB_TOKEN` with job-scoped `packages: write` and associates the images with
the source repository. It tests/vets Go, builds amd64/arm64 controller, bundle,
and catalog images, validates the bundle/catalog, pins controller and bundle
references by digest, and attaches SBOM/provenance attestations. Image digests
and generated packaging are saved as the `olm-release-v0.1.0` Actions artifact.

**One-time GHCR setup:** packages may initially be private. After the publish
job finishes, make `cube-operator`, `cube-operator-bundle`, and
`cube-operator-catalog` public in their GitHub package settings. The separate
verify job intentionally checks anonymous pulls; if it fails because packages
are private, change visibility and **re-run failed jobs**, not the successful
publish job. It then installs the published catalog through OLM on Kubernetes
1.34/Kind, waits for CSV `Succeeded` and controller readiness, and saves logs
and installation objects as `olm-install-evidence`. This CI runtime test uses
amd64; arm64 runtime validation remains a separate release check.

If an existing package rejects `GITHUB_TOKEN`, grant this repository Actions
access in that package's settings. Do not work around this by committing a
personal token. Do not manually dispatch another publication over an existing
release tag. Update the README's status only after verification succeeds.

OperatorHub submission remains a separate reviewed process described below;
the workflow does not open community PRs or claim a listing exists.

## Tools and credentials

The following tools and credentials are needed only for the local alternative
and additional release validation; GitHub Actions installs its own tools.

- Docker with Buildx and Linux amd64/arm64 builders.
- Python 3 with PyYAML (`python3 -m pip install PyYAML==6.0.3` in a virtualenv).
- Operator SDK v1.42.3 and opm v1.74.0 from their upstream releases.
- Kind and kubectl for an isolated installation test.
- A GHCR login with `write:packages` access to `sqe`. Authenticate locally using
  your credential manager or `docker login ghcr.io -u sqe --password-stdin`;
  never put a token in a file, command argument, or Git. GitHub CLI's ordinary
  repository token does not necessarily grant package publishing permissions.

On macOS, opm also needs a containers/image `policy.json` under
`$HOME/.config/containers/` (or `$XDG_CONFIG_HOME/containers/`). Configure it
according to your image-signature policy. A policy whose default is
`[{"type":"insecureAcceptAnything"}]` accepts unsigned images; use that only
when appropriate for your development environment, not as signature verification.

## Build and publish the initial release

The targets below build an **initial, single-version alpha catalog**. They do
not implement an upgrade graph. For subsequent releases, preserve previous
bundle entries and add reviewed `replaces`/`skips` edges before publishing a
catalog to existing subscribers. Never overwrite an existing release tag.

```bash
export VERSION=0.1.0
make image-push VERSION="$VERSION"

# Inspect the pushed multi-platform manifest and copy its sha256 digest.
docker buildx imagetools inspect "ghcr.io/sqe/cube-operator:v$VERSION"
export IMG='ghcr.io/sqe/cube-operator@sha256:REPLACE_WITH_CONTROLLER_DIGEST'
make bundle-validate VERSION="$VERSION" IMG="$IMG"
make bundle-push VERSION="$VERSION" IMG="$IMG"

docker buildx imagetools inspect "ghcr.io/sqe/cube-operator-bundle:v$VERSION"
export BUNDLE_IMG='ghcr.io/sqe/cube-operator-bundle@sha256:REPLACE_WITH_BUNDLE_DIGEST'
make catalog-push VERSION="$VERSION" BUNDLE_IMG="$BUNDLE_IMG"
docker buildx imagetools inspect "ghcr.io/sqe/cube-operator-catalog:v$VERSION"
```

`make bundle` generates the CSV, CRD, annotations, and bundle Dockerfile under
`dist/olm/bundle/`. Deployment settings, permissions, CRD schema, and the example
come from `config/`; edit those sources rather than the generated files. Only
the Go controller and the `AllNamespaces` install mode are packaged.
`make bundle-build` builds the bundle locally without publishing it.

`make catalog` renders the **published** bundle into a file-based catalog and
validates it with opm. It requires pull access to that bundle image.
All three image names can be overridden with `IMG`, `BUNDLE_IMG`, and
`CATALOG_IMG`; `PLATFORMS` defaults to `linux/amd64,linux/arm64`.
`OPERATOR_SDK` and `OPM` can point to explicit executable paths.

Make each GHCR package **public** in its package settings; public source code
does not automatically make container packages public. Verify pulls from all
three repositories without registry credentials, and record their digests in
the release notes. Pin `spec.image` in `config/olm/install.yaml` to the catalog
digest for an immutable installation. Sign images and produce/review SBOM and
provenance according to your release policy before announcing availability.

## Verify the public catalog on a clean cluster

Use a dedicated kubeconfig so no commands target an existing cluster:

```bash
export KUBECONFIG="$(mktemp)"
kind create cluster --name cube-olm --image kindest/node:v1.34.0
operator-sdk olm install --version 0.46.0 --timeout 5m
kubectl apply -f config/olm/install.yaml
kubectl -n cube-system wait --for=create \
  csv/cube-microk8s-operator.v0.1.0 --timeout=5m
kubectl -n cube-system wait --for=jsonpath='{.status.phase}'=Succeeded \
  csv/cube-microk8s-operator.v0.1.0 --timeout=5m
kubectl -n cube-system rollout status deployment/cube-operator --timeout=2m
kubectl -n cube-system get subscription,installplan,csv
```

Exercise a CubeCluster in a namespace **other than** `cube-system`, using the
README's configuration and storage prerequisites. Check owned workloads,
status updates, and logs for RBAC errors. A successful CSV proves operator
installation, not database connectivity or a working Cube query. Repeat the
installation on amd64 and arm64 before claiming both are runtime-tested.

Save the catalog, Subscription, InstallPlan, CSV, deployment, and reconciliation
results as release evidence. Then remove only the disposable test environment:

```bash
kind delete cluster --name cube-olm
rm "$KUBECONFIG"
unset KUBECONFIG
```

After public pull and installation checks pass, update the README's publication
status. Do not mark OperatorHub available based on this custom catalog test.

## Submit to OperatorHub.io

Follow the current contribution requirements in
[`k8s-operatorhub/community-operators`](https://github.com/k8s-operatorhub/community-operators).
Copy the generated `manifests/` and `metadata/` directories into
`operators/cube-microk8s-operator/0.1.0/` in a fork. Before submission:

1. Add the project's approved icon and a maintainer contact email to the CSV;
   the generator intentionally does not invent a contact or brand asset.
2. Resolve the project's licensing explicitly. The repository currently has no
   license file for the operator's own code; choose and add an appropriate
   open-source license before presenting the bundle for community distribution.
   Preserve the [Cube attribution and upstream license references](../README.md#upstream-attribution-and-licensing).
   Cube Client's MIT license and Cube Backend's Apache 2.0 license apply to
   those upstream components, not automatically to this operator.
3. Run the community repository's required bundle checks, metadata checks, and
   scorecard against the public images; include the clean-cluster evidence.
4. Open a submission PR and address review feedback. Wait for merge and catalog
   ingestion, then verify the actual OperatorHub listing and generated install
   command before changing the README to say it is available there.

For the OpenShift community catalog, follow the separate requirements in
[`redhat-openshift-ecosystem/community-operators-prod`](https://github.com/redhat-openshift-ecosystem/community-operators-prod).
An OperatorHub.io acceptance does not establish OpenShift compatibility.
