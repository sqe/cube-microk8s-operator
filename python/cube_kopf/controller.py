"""Kopf reconciler for the platform.cube.dev/v1alpha1 CubeCluster API."""

from datetime import datetime, timezone
from decimal import InvalidOperation
import logging

import kopf
from kubernetes import client, config, dynamic
from kubernetes.client.exceptions import ApiException
from kubernetes.config.config_exception import ConfigException
from kubernetes.utils.quantity import parse_quantity

from .resources import desired_objects, replicas, store_mode

LOG = logging.getLogger(__name__)
_dynamic_client = None
_resources = {}


def dynamic_client():
    global _dynamic_client
    if _dynamic_client is None:
        try:
            config.load_incluster_config()
        except ConfigException:
            config.load_kube_config()
        _dynamic_client = dynamic.DynamicClient(client.ApiClient())
    return _dynamic_client


def resource_for(api_version, kind):
    key = (api_version, kind)
    if key not in _resources:
        _resources[key] = dynamic_client().resources.get(
            api_version=api_version, kind=kind
        )
    return _resources[key]


def get_object(api_version, kind, name, namespace):
    return resource_for(api_version, kind).get(name=name, namespace=namespace)


def apply_object(desired, owner):
    metadata = desired["metadata"]
    metadata["ownerReferences"] = [
        {
            "apiVersion": "platform.cube.dev/v1alpha1",
            "kind": "CubeCluster",
            "name": owner["name"],
            "uid": owner["uid"],
            "controller": True,
            "blockOwnerDeletion": True,
        }
    ]
    resource = resource_for(desired["apiVersion"], desired["kind"])
    try:
        resource.get(name=metadata["name"], namespace=metadata["namespace"])
    except ApiException as error:
        if error.status != 404:
            raise
        resource.create(namespace=metadata["namespace"], body=desired)
        return
    resource.patch(
        name=metadata["name"],
        namespace=metadata["namespace"],
        body={"metadata": metadata, "spec": desired["spec"]},
        content_type="application/merge-patch+json",
    )


def delete_if_present(api_version, kind, name, namespace):
    try:
        resource_for(api_version, kind).delete(name=name, namespace=namespace)
    except ApiException as error:
        if error.status != 404:
            raise


def validate_references(spec, namespace):
    model = spec.get("modelConfigMap")
    configuration = spec.get("configurationSecret")
    if not model or not configuration:
        raise ValueError(
            "spec.modelConfigMap and spec.configurationSecret are required"
        )
    get_object("v1", "ConfigMap", model, namespace)
    get_object("v1", "Secret", configuration, namespace)

    if (
        replicas(spec.get("api", {}).get("replicas"), 2) < 1
        or replicas(spec.get("refreshWorker", {}).get("replicas"), 1) < 1
    ):
        raise ValueError("API and refresh worker replicas must be at least 1")
    mode = spec.get("cubeStore", {}).get("mode", "single")
    if mode not in ("single", "clustered"):
        raise ValueError(f"unsupported Cube Store mode {mode!r}")
    workers = replicas(spec.get("cubeStore", {}).get("workers"), 2)
    if mode == "clustered" and workers < 1:
        raise ValueError("clustered Cube Store workers must be at least 1")
    scratch_size = spec.get("cubeStore", {}).get("scratch", {}).get("size")
    if scratch_size:
        try:
            if parse_quantity(scratch_size) <= 0:
                raise ValueError("Cube Store scratch size must be positive")
        except (InvalidOperation, TypeError) as error:
            raise ValueError(
                f"invalid Cube Store scratch size: {scratch_size!r}"
            ) from error
    service = spec.get("service", {})
    if service.get("nodePort") and service.get("type", "ClusterIP") not in (
        "NodePort",
        "LoadBalancer",
    ):
        raise ValueError(
            "spec.service.nodePort requires service type NodePort or LoadBalancer"
        )

    storage = spec.get("cubeStore", {}).get("remoteStorage", {})
    if storage.get("type") == "persistentVolume":
        claim = storage.get("persistentVolume", {}).get("existingClaim")
        if not claim:
            raise ValueError("persistentVolume storage requires existingClaim")
        get_object("v1", "PersistentVolumeClaim", claim, namespace)
    elif storage.get("type") == "s3":
        s3 = storage.get("s3", {})
        if not s3.get("bucket") or not s3.get("region"):
            raise ValueError("s3 storage requires bucket and region")
        if s3.get("secretRef"):
            get_object("v1", "Secret", s3["secretRef"], namespace)
    else:
        raise ValueError(f"unsupported remote storage type {storage.get('type')!r}")


def workload_readiness(spec, name, namespace):
    def ready(api_version, kind, component):
        obj = get_object(api_version, kind, f"{name}-{component}", namespace)
        return int(obj.to_dict().get("status", {}).get("readyReplicas", 0) or 0)

    api_expected = replicas(spec.get("api", {}).get("replicas"), 2)
    refresh_expected = replicas(spec.get("refreshWorker", {}).get("replicas"), 1)
    api_ready = ready("apps/v1", "Deployment", "api")
    refresh_ready = ready("apps/v1", "Deployment", "refresh-worker")
    parts = [
        f"API {api_ready}/{api_expected}",
        f"refresh {refresh_ready}/{refresh_expected}",
    ]
    available = api_ready >= api_expected and refresh_ready >= refresh_expected
    if store_mode(spec) == "clustered":
        worker_expected = replicas(spec["cubeStore"].get("workers"), 2)
        router_ready = ready("apps/v1", "StatefulSet", "cubestore-router")
        worker_ready = ready("apps/v1", "StatefulSet", "cubestore-worker")
        parts.append(
            f"Cube Store router {router_ready}/1, workers {worker_ready}/{worker_expected}"
        )
        available = available and router_ready >= 1 and worker_ready >= worker_expected
    else:
        store_ready = ready("apps/v1", "Deployment", "cubestore")
        parts.append(f"Cube Store {store_ready}/1")
        available = available and store_ready >= 1
    return api_ready, available, "; ".join(parts)


def set_status(
    patch,
    old_status,
    generation,
    namespace,
    name,
    ready,
    condition_status,
    reason,
    message,
):
    previous = next(
        (
            item
            for item in old_status.get("conditions", [])
            if item.get("type") == "Ready"
        ),
        {},
    )
    unchanged = (
        previous.get("status") == condition_status
        and previous.get("reason") == reason
        and previous.get("message") == message
    )
    transition = (
        previous.get("lastTransitionTime")
        if unchanged
        else datetime.now(timezone.utc).isoformat().replace("+00:00", "Z")
    )
    patch.status.update(
        {
            "observedGeneration": generation,
            "readyAPIs": ready,
            "endpoint": f"http://{name}-api.{namespace}.svc:4000",
            "conditions": [
                {
                    "type": "Ready",
                    "status": condition_status,
                    "reason": reason,
                    "message": message,
                    "observedGeneration": generation,
                    "lastTransitionTime": transition,
                }
            ],
        }
    )


def reconcile(spec, name, namespace, meta, status, patch, **_):
    generation = meta.get("generation", 0)
    owner = {"name": name, "uid": meta["uid"]}
    try:
        validate_references(spec, namespace)
        for desired in desired_objects(name, namespace, spec):
            apply_object(desired, owner)
        if store_mode(spec) == "clustered":
            delete_if_present("apps/v1", "Deployment", f"{name}-cubestore", namespace)
            delete_if_present("v1", "Service", f"{name}-cubestore", namespace)
        else:
            for api_version, kind, component in (
                ("apps/v1", "StatefulSet", "cubestore-router"),
                ("apps/v1", "StatefulSet", "cubestore-worker"),
                ("v1", "Service", "cubestore-router"),
                ("v1", "Service", "cubestore-worker"),
            ):
                delete_if_present(api_version, kind, f"{name}-{component}", namespace)
        ready, available, message = workload_readiness(spec, name, namespace)
        if available:
            set_status(
                patch,
                status,
                generation,
                namespace,
                name,
                ready,
                "True",
                "Available",
                "Cube API, refresh worker, and Cube Store are ready",
            )
        else:
            set_status(
                patch,
                status,
                generation,
                namespace,
                name,
                ready,
                "False",
                "Progressing",
                message,
            )
    except ValueError as error:
        set_status(
            patch,
            status,
            generation,
            namespace,
            name,
            0,
            "False",
            "InvalidConfiguration",
            str(error),
        )
    except ApiException as error:
        if error.status == 404:
            set_status(
                patch,
                status,
                generation,
                namespace,
                name,
                0,
                "False",
                "InvalidConfiguration",
                error.reason or "required reference not found",
            )
            return
        LOG.exception("Kubernetes API reconciliation failed")
        raise kopf.TemporaryError(str(error), delay=10) from error


@kopf.on.create("platform.cube.dev", "v1alpha1", "cubeclusters")
@kopf.on.update("platform.cube.dev", "v1alpha1", "cubeclusters", field="spec")
@kopf.on.resume("platform.cube.dev", "v1alpha1", "cubeclusters")
def on_change(**kwargs):
    reconcile(**kwargs)


@kopf.timer("platform.cube.dev", "v1alpha1", "cubeclusters", interval=10.0)
def on_timer(**kwargs):
    reconcile(**kwargs)
