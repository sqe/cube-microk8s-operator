#!/usr/bin/env python3
"""Build an OLM bundle from the controller's CRD, deployment, and RBAC."""

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
            "containerImage": image,
            "repository": "https://github.com/sqe/cube-microk8s-operator",
            "support": "https://github.com/sqe/cube-microk8s-operator/issues",
            "operators.operatorframework.io/arch.amd64": "supported",
            "operators.operatorframework.io/arch.arm64": "supported",
        },
    },
    "spec": {
        "displayName": "Cube Core Operator",
        "description": "Manages self-hosted Cube Core APIs, refresh workers, and Cube Store. "
        "Create the model ConfigMap, configuration Secret, and durable storage before creating a CubeCluster. "
        "Installs the Go controller cluster-wide; do not run another Go or Kopf controller alongside it.",
        "version": version,
        "maturity": "alpha",
        "minKubeVersion": "1.34.0",
        "provider": {"name": "Aziz Kurbanov", "url": "https://github.com/sqe"},
        "links": [{"name": "Documentation", "url": "https://github.com/sqe/cube-microk8s-operator"}],
        "installModes": [
            {"type": mode, "supported": mode == "AllNamespaces"}
            for mode in ["OwnNamespace", "SingleNamespace", "MultiNamespace", "AllNamespaces"]
        ],
        "customresourcedefinitions": {"owned": [{
            "name": "cubeclusters.platform.cube.dev", "version": "v1alpha1",
            "kind": "CubeCluster", "displayName": "Cube Cluster",
            "description": "A self-hosted Cube Core and Cube Store deployment.",
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
}
(output / "metadata/annotations.yaml").write_text(yaml.safe_dump({"annotations": labels}, sort_keys=False))
(output / "Dockerfile").write_text(
    "FROM scratch\nCOPY manifests /manifests/\nCOPY metadata /metadata/\n"
    + "".join(f"LABEL {key}={json.dumps(value)}\n" for key, value in labels.items())
)
print(output)
