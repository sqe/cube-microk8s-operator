"""Pure Kubernetes object builders shared by the Kopf controller tests."""

DEFAULT_CUBE_IMAGE = "cubejs/cube:v1.7.20@sha256:7f14f4be9f3303afe48a16584480c8a9dc15f44c13daf66c2a5b31019025b71a"
DEFAULT_STORE_IMAGE = "cubejs/cubestore:v1.7.20@sha256:cd5fe68049204640704a6412a39e7a09eb391fc70890577dd21b5480d85cb219"


def replicas(value, fallback):
    return fallback if value is None else value


def store_mode(spec):
    return (
        "clustered"
        if spec.get("cubeStore", {}).get("mode") == "clustered"
        else "single"
    )


def labels(name, component):
    result = {
        "app.kubernetes.io/name": "cube",
        "app.kubernetes.io/instance": name,
        "app.kubernetes.io/component": component,
        "app.kubernetes.io/part-of": "cube",
    }
    if component.startswith("cubestore"):
        result["platform.cube.dev/store"] = "true"
    return result


def pod_security_context():
    return {"seccompProfile": {"type": "RuntimeDefault"}}


def container_security_context():
    return {"allowPrivilegeEscalation": False, "capabilities": {"drop": ["ALL"]}}


def cube_store_host(name, spec):
    suffix = "cubestore-router" if store_mode(spec) == "clustered" else "cubestore"
    return f"{name}-{suffix}"


def cube_container(name, spec, component, workload):
    env = [
        {"name": "CUBEJS_DEV_MODE", "value": "false"},
        {"name": "CUBEJS_CUBESTORE_HOST", "value": cube_store_host(name, spec)},
        {"name": "CUBEJS_TELEMETRY", "value": "false"},
    ]
    if component == "refresh-worker":
        env.append({"name": "CUBEJS_REFRESH_WORKER", "value": "true"})
    return {
        "name": component,
        "image": spec.get("image") or DEFAULT_CUBE_IMAGE,
        "imagePullPolicy": spec.get("imagePullPolicy") or "IfNotPresent",
        "env": env,
        "envFrom": [{"secretRef": {"name": spec["configurationSecret"]}}],
        "ports": [{"name": "http", "containerPort": 4000}],
        "resources": workload.get("resources", {}),
        "securityContext": container_security_context(),
        "volumeMounts": [
            {"name": "model", "mountPath": "/cube/conf/model", "readOnly": True}
        ],
        "readinessProbe": http_probe("/readyz"),
        "livenessProbe": http_probe("/livez"),
    }


def http_probe(path):
    return {
        "httpGet": {"path": path, "port": "http"},
        "initialDelaySeconds": 10,
        "periodSeconds": 10,
        "timeoutSeconds": 5,
        "failureThreshold": 6,
    }


def tcp_probe(port):
    return {
        "tcpSocket": {"port": port},
        "initialDelaySeconds": 5,
        "periodSeconds": 10,
        "failureThreshold": 6,
    }


def deployment(name, namespace, spec, component, workload):
    count = replicas(workload.get("replicas"), 2 if component == "api" else 1)
    match_labels = labels(name, component)
    return {
        "apiVersion": "apps/v1",
        "kind": "Deployment",
        "metadata": {
            "name": f"{name}-{component}",
            "namespace": namespace,
            "labels": match_labels,
        },
        "spec": {
            "replicas": count,
            "selector": {"matchLabels": match_labels},
            "template": {
                "metadata": {"labels": match_labels},
                "spec": {
                    "securityContext": pod_security_context(),
                    "containers": [cube_container(name, spec, component, workload)],
                    "volumes": [
                        {"name": "model", "configMap": {"name": spec["modelConfigMap"]}}
                    ],
                },
            },
        },
    }


def storage_configuration(spec):
    store = spec["cubeStore"]
    storage = store["remoteStorage"]
    empty_dir = {}
    if store.get("scratch", {}).get("size"):
        empty_dir["sizeLimit"] = store["scratch"]["size"]
    env, env_from = [], []
    volumes = [{"name": "scratch", "emptyDir": empty_dir}]
    mounts = [{"name": "scratch", "mountPath": "/cube/data"}]
    if storage["type"] == "s3":
        s3 = storage["s3"]
        env.extend(
            [
                {"name": "CUBESTORE_S3_BUCKET", "value": s3["bucket"]},
                {"name": "CUBESTORE_S3_REGION", "value": s3["region"]},
            ]
        )
        if s3.get("secretRef"):
            env_from.append({"secretRef": {"name": s3["secretRef"]}})
    else:
        claim = storage["persistentVolume"]["existingClaim"]
        volumes.append(
            {"name": "remote", "persistentVolumeClaim": {"claimName": claim}}
        )
        mounts.append({"name": "remote", "mountPath": "/cube/remote"})
        env.append({"name": "CUBESTORE_REMOTE_DIR", "value": "/cube/remote"})
    return env, env_from, volumes, mounts


def worker_addresses(name, count):
    return ",".join(
        f"{name}-cubestore-worker-{index}.{name}-cubestore-worker:9001"
        for index in range(count)
    )


def store_container(name, spec, role, role_env):
    env, env_from, volumes, mounts = storage_configuration(spec)
    env.extend(role_env)
    probe_port = "worker" if role == "worker" else "http"
    return (
        {
            "name": role,
            "image": spec["cubeStore"].get("image") or DEFAULT_STORE_IMAGE,
            "imagePullPolicy": spec.get("imagePullPolicy") or "IfNotPresent",
            "env": env,
            "envFrom": env_from,
            "ports": [
                {"name": "http", "containerPort": 3030},
                {"name": "meta", "containerPort": 9999},
                {"name": "worker", "containerPort": 9001},
            ],
            "resources": spec["cubeStore"].get("resources", {}),
            "securityContext": container_security_context(),
            "volumeMounts": mounts,
            "readinessProbe": tcp_probe(probe_port),
            "livenessProbe": tcp_probe(probe_port),
        },
        volumes,
    )


def store_service(name, namespace, component, ports, headless=False):
    spec = {"selector": labels(name, component), "ports": ports}
    if headless:
        spec["clusterIP"] = "None"
    return {
        "apiVersion": "v1",
        "kind": "Service",
        "metadata": {
            "name": f"{name}-{component}",
            "namespace": namespace,
            "labels": labels(name, component),
        },
        "spec": spec,
    }


def store_workload(name, namespace, spec, component, count, role, role_env, stateful):
    container, volumes = store_container(name, spec, role, role_env)
    match_labels = labels(name, component)
    workload_spec = {
        "replicas": count,
        "selector": {"matchLabels": match_labels},
        "template": {
            "metadata": {"labels": match_labels},
            "spec": {
                "securityContext": pod_security_context(),
                "containers": [container],
                "volumes": volumes,
            },
        },
    }
    kind = "StatefulSet" if stateful else "Deployment"
    if stateful:
        workload_spec.update(
            {"serviceName": f"{name}-{component}", "podManagementPolicy": "Parallel"}
        )
    return {
        "apiVersion": "apps/v1",
        "kind": kind,
        "metadata": {
            "name": f"{name}-{component}",
            "namespace": namespace,
            "labels": match_labels,
        },
        "spec": workload_spec,
    }


def desired_objects(name, namespace, spec):
    service_type = spec.get("service", {}).get("type") or "ClusterIP"
    api_port = {"name": "http", "port": 4000}
    if spec.get("service", {}).get("nodePort"):
        api_port["nodePort"] = spec["service"]["nodePort"]
    result = [
        {
            "apiVersion": "v1",
            "kind": "Service",
            "metadata": {
                "name": f"{name}-api",
                "namespace": namespace,
                "labels": labels(name, "api"),
                "annotations": spec.get("service", {}).get("annotations", {}),
            },
            "spec": {
                "type": service_type,
                "selector": labels(name, "api"),
                "ports": [api_port],
            },
        }
    ]
    result.extend(
        [
            deployment(name, namespace, spec, "api", spec.get("api", {})),
            deployment(
                name, namespace, spec, "refresh-worker", spec.get("refreshWorker", {})
            ),
            {
                "apiVersion": "policy/v1",
                "kind": "PodDisruptionBudget",
                "metadata": {"name": f"{name}-api", "namespace": namespace},
                "spec": {
                    "minAvailable": 1,
                    "selector": {"matchLabels": labels(name, "api")},
                },
            },
            {
                "apiVersion": "networking.k8s.io/v1",
                "kind": "NetworkPolicy",
                "metadata": {"name": f"{name}-cubestore", "namespace": namespace},
                "spec": {
                    "podSelector": {
                        "matchLabels": {
                            "app.kubernetes.io/instance": name,
                            "platform.cube.dev/store": "true",
                        }
                    },
                    "policyTypes": ["Ingress"],
                    "ingress": [
                        {
                            "from": [
                                {
                                    "podSelector": {
                                        "matchLabels": {
                                            "app.kubernetes.io/instance": name
                                        }
                                    }
                                }
                            ],
                            "ports": [{"port": port} for port in (3030, 9001, 9999)],
                        }
                    ],
                },
            },
        ]
    )
    if store_mode(spec) == "single":
        result.append(
            store_service(
                name, namespace, "cubestore", [{"name": "http", "port": 3030}]
            )
        )
        result.append(
            store_workload(
                name, namespace, spec, "cubestore", 1, "cubestore", [], False
            )
        )
    else:
        worker_count = replicas(spec["cubeStore"].get("workers"), 2)
        addresses = worker_addresses(name, worker_count)
        result.extend(
            [
                store_service(
                    name,
                    namespace,
                    "cubestore-router",
                    [{"name": "http", "port": 3030}, {"name": "meta", "port": 9999}],
                ),
                store_service(
                    name,
                    namespace,
                    "cubestore-worker",
                    [{"name": "worker", "port": 9001}],
                    True,
                ),
                store_workload(
                    name,
                    namespace,
                    spec,
                    "cubestore-router",
                    1,
                    "router",
                    [
                        {
                            "name": "CUBESTORE_SERVER_NAME",
                            "value": f"{name}-cubestore-router:9999",
                        },
                        {"name": "CUBESTORE_META_PORT", "value": "9999"},
                        {"name": "CUBESTORE_WORKERS", "value": addresses},
                    ],
                    True,
                ),
                store_workload(
                    name,
                    namespace,
                    spec,
                    "cubestore-worker",
                    worker_count,
                    "worker",
                    [
                        {
                            "name": "POD_NAME",
                            "valueFrom": {"fieldRef": {"fieldPath": "metadata.name"}},
                        },
                        {
                            "name": "CUBESTORE_SERVER_NAME",
                            "value": f"$(POD_NAME).{name}-cubestore-worker:9001",
                        },
                        {"name": "CUBESTORE_WORKER_PORT", "value": "9001"},
                        {
                            "name": "CUBESTORE_META_ADDR",
                            "value": f"{name}-cubestore-router:9999",
                        },
                        {"name": "CUBESTORE_WORKERS", "value": addresses},
                    ],
                    True,
                ),
            ]
        )
    return result
