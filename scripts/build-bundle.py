#!/usr/bin/env python3
"""Build an OLM bundle from the controller's CRD, deployment, and RBAC."""

import base64
import json
import os
from pathlib import Path
import shutil

import yaml

ROOT = Path(__file__).resolve().parent.parent
version = os.environ.get("VERSION", "0.1.0")
image = os.environ.get("IMG", f"ghcr.io/sqe/cube-operator:v{version}")
output = ROOT / "dist/olm/bundle"
(output / "manifests").mkdir(parents=True, exist_ok=True)
(output / "metadata").mkdir(exist_ok=True)


def load(path):
    return list(yaml.safe_load_all((ROOT / path).read_text()))


deployment = next(doc for doc in load("config/manager/manager.yaml") if doc["kind"] == "Deployment")
deployment["spec"]["template"]["spec"]["containers"][0]["image"] = image
rules = load("config/rbac/role.yaml")[0]["rules"]
example = load("config/samples/production.yaml")[0]
example["metadata"].pop("namespace", None)
csv = {
    "apiVersion": "operators.coreos.com/v1alpha1",
    "kind": "ClusterServiceVersion",
    "metadata": {
        "name": f"cube-microk8s-operator.v{version}",
        "annotations": {
            "alm-examples": json.dumps([example]),
            "capabilities": "Basic Install",
            "categories": "Database,Big Data",
            "description": "Independent Kubernetes operator for self-hosted Cube Core and Cube Store.",
            "containerImage": image,
            "repository": "https://github.com/sqe/cube-microk8s-operator",
            "support": "https://github.com/sqe/cube-microk8s-operator/issues",
            "operators.operatorframework.io/arch.amd64": "supported",
            "operators.operatorframework.io/arch.arm64": "supported",
        },
    },
    "spec": {
        "displayName": "Cube Core Operator",
        "description": """## Independent community operator for Cube Core

Manages self-hosted [Cube Core](https://github.com/cube-js/cube) APIs, refresh
workers, and Cube Store. This is not an official Cube product or an endorsement
by Cube Dev, Inc.

### Features and usage

Create a `platform.cube.dev/v1alpha1` CubeCluster to reconcile API and refresh
Deployments, single or clustered Cube Store, Services, PodDisruptionBudgets,
and NetworkPolicies. The operator reports readiness in CubeCluster status.
Only the Go controller is packaged, with cluster-wide AllNamespaces mode.
Do not run another Go or Kopf controller alongside this installation.

### Prerequisites

Use Kubernetes 1.32.8 or later. Before creating a CubeCluster, create its model
ConfigMap, configuration Secret, and durable storage (an existing PVC or S3
bucket with any required credentials). Adapt the example bucket and resource
requests to your environment. The operator does not create credentials,
provision remote storage, or provide data backups.

See the [installation and configuration guide](https://github.com/sqe/cube-microk8s-operator#configure-cube-core).
On arm64, choose an arm64-compatible Cube Store image as described in the guide.

### License and attribution

Original operator code: Apache-2.0, copyright 2026 Aziz Kurbanov.
Uses upstream Cube software: Cube Client is MIT licensed; Cube Backend is
Apache-2.0 licensed. Their licenses and copyrights remain separate.
""",
        "version": version,
        "maturity": "alpha",
        "minKubeVersion": "1.32.8",
        "provider": {"name": "Aziz Kurbanov", "url": "https://github.com/sqe"},
        "maintainers": [{"name": "Aziz Kurbanov", "email": "azizbek@gmail.com"}],
        "icon": [{
            "base64data": base64.b64encode((ROOT / "config/olm/icon.svg").read_bytes()).decode(),
            "mediatype": "image/svg+xml",
        }],
        "keywords": ["cube", "analytics", "semantic-layer", "cubestore"],
        "links": [{"name": "Documentation", "url": "https://github.com/sqe/cube-microk8s-operator"}],
        "installModes": [
            {"type": mode, "supported": mode == "AllNamespaces"}
            for mode in ["OwnNamespace", "SingleNamespace", "MultiNamespace", "AllNamespaces"]
        ],
        "customresourcedefinitions": {"owned": [{
            "name": "cubeclusters.platform.cube.dev", "version": "v1alpha1",
            "kind": "CubeCluster", "displayName": "Cube Cluster",
            "description": "A self-hosted Cube Core and Cube Store deployment.",
            "resources": [
                {"name": name, "kind": kind, "version": "v1"}
                for name, kind in [
                    ("deployments", "Deployment"), ("statefulsets", "StatefulSet"),
                    ("services", "Service"), ("poddisruptionbudgets", "PodDisruptionBudget"),
                    ("networkpolicies", "NetworkPolicy"),
                ]
            ],
            "specDescriptors": [
                {"path": path, "displayName": title, "description": description}
                for path, title, description in [
                    ("image", "Cube image", "Cube API and refresh worker container image."),
                    ("imagePullPolicy", "Image pull policy", "Pull policy for managed container images."),
                    ("modelConfigMap", "Model ConfigMap", "Existing ConfigMap containing Cube models."),
                    ("configurationSecret", "Configuration Secret", "Existing Secret containing Cube configuration."),
                    ("api", "API", "API replicas and container resource requirements."),
                    ("refreshWorker", "Refresh worker", "Refresh replicas and container resource requirements."),
                    ("cubeStore", "Cube Store", "Store topology, image, scratch space, and durable remote storage."),
                    ("service", "API service", "Service type, annotations, and optional node port."),
                ]
            ],
            "statusDescriptors": [
                {"path": path, "displayName": title, "description": description}
                for path, title, description in [
                    ("conditions", "Conditions", "Readiness and configuration validation conditions."),
                    ("observedGeneration", "Observed generation", "Last reconciled desired-state generation."),
                    ("readyAPIs", "Ready APIs", "Number of ready Cube API replicas."),
                    ("endpoint", "Endpoint", "In-cluster Cube API endpoint."),
                ]
            ],
        }]},
        "install": {"strategy": "deployment", "spec": {
            "deployments": [{"name": deployment["metadata"]["name"], "spec": deployment["spec"]}],
            "clusterPermissions": [{"serviceAccountName": "cube-operator", "rules": rules}],
        }},
    },
}
(output / "manifests/cube-microk8s-operator.clusterserviceversion.yaml").write_text(yaml.safe_dump(csv, sort_keys=False))
shutil.copyfile(ROOT / "config/crd/bases/platform.cube.dev_cubeclusters.yaml", output / "manifests/platform.cube.dev_cubeclusters.yaml")
labels = {
    "operators.operatorframework.io.bundle.mediatype.v1": "registry+v1",
    "operators.operatorframework.io.bundle.manifests.v1": "manifests/",
    "operators.operatorframework.io.bundle.metadata.v1": "metadata/",
    "operators.operatorframework.io.bundle.package.v1": "cube-microk8s-operator",
    "operators.operatorframework.io.bundle.channels.v1": "alpha",
    "operators.operatorframework.io.bundle.channel.default.v1": "alpha",
    "operators.operatorframework.io.test.mediatype.v1": "scorecard+v1",
    "operators.operatorframework.io.test.config.v1": "tests/scorecard/",
}
(output / "metadata/annotations.yaml").write_text(yaml.safe_dump({"annotations": labels}, sort_keys=False))
(output / "tests/scorecard").mkdir(parents=True, exist_ok=True)
shutil.copyfile(ROOT / "config/olm/scorecard.yaml", output / "tests/scorecard/config.yaml")
for name in ("LICENSE", "NOTICE"):
    shutil.copyfile(ROOT / name, output / name)
(output / "Dockerfile").write_text(
    "FROM scratch\nCOPY manifests /manifests/\nCOPY metadata /metadata/\n"
    "COPY tests /tests/\n"
    "COPY LICENSE NOTICE /licenses/\nLABEL org.opencontainers.image.licenses=Apache-2.0\n"
    + "".join(f"LABEL {key}={json.dumps(value)}\n" for key, value in labels.items())
)
print(output)
