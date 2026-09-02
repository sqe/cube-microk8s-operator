import unittest

from cube_kopf.resources import DEFAULT_CUBE_IMAGE, desired_objects


def base_spec():
    return {
        "modelConfigMap": "model",
        "configurationSecret": "configuration",
        "api": {"replicas": 1},
        "refreshWorker": {"replicas": 1},
        "cubeStore": {
            "mode": "single",
            "remoteStorage": {
                "type": "persistentVolume",
                "persistentVolume": {"existingClaim": "remote"},
            },
            "scratch": {"size": "2Gi"},
        },
    }


class ResourceTests(unittest.TestCase):
    def test_api_and_refresh_contracts(self):
        objects = desired_objects("example", "test", base_spec())
        api = next(
            item
            for item in objects
            if item["kind"] == "Deployment"
            and item["metadata"]["name"] == "example-api"
        )
        refresh = next(
            item
            for item in objects
            if item["kind"] == "Deployment"
            and item["metadata"]["name"] == "example-refresh-worker"
        )
        container = api["spec"]["template"]["spec"]["containers"][0]
        self.assertEqual(DEFAULT_CUBE_IMAGE, container["image"])
        self.assertIn("@sha256:", container["image"])
        self.assertEqual("/readyz", container["readinessProbe"]["httpGet"]["path"])
        self.assertEqual("/livez", container["livenessProbe"]["httpGet"]["path"])
        self.assertIn(
            {"name": "CUBEJS_REFRESH_WORKER", "value": "true"},
            refresh["spec"]["template"]["spec"]["containers"][0]["env"],
        )

    def test_clustered_worker_topology_and_probe(self):
        spec = base_spec()
        spec["cubeStore"].update({"mode": "clustered", "workers": 3})
        spec["cubeStore"]["remoteStorage"] = {
            "type": "s3",
            "s3": {"bucket": "cube", "region": "us-west-2"},
        }
        objects = desired_objects("example", "test", spec)
        workers = next(
            item
            for item in objects
            if item["kind"] == "StatefulSet"
            and item["metadata"]["name"] == "example-cubestore-worker"
        )
        container = workers["spec"]["template"]["spec"]["containers"][0]
        self.assertEqual(3, workers["spec"]["replicas"])
        self.assertEqual("worker", container["readinessProbe"]["tcpSocket"]["port"])
        addresses = next(
            item["value"]
            for item in container["env"]
            if item["name"] == "CUBESTORE_WORKERS"
        )
        self.assertEqual(3, len(addresses.split(",")))
        self.assertNotIn(
            "CUBESTORE_REMOTE_DIR", {item["name"] for item in container["env"]}
        )

    def test_single_store_scratch_limit(self):
        objects = desired_objects("example", "test", base_spec())
        store = next(
            item
            for item in objects
            if item["kind"] == "Deployment"
            and item["metadata"]["name"] == "example-cubestore"
        )
        volumes = store["spec"]["template"]["spec"]["volumes"]
        self.assertEqual("2Gi", volumes[0]["emptyDir"]["sizeLimit"])


if __name__ == "__main__":
    unittest.main()
