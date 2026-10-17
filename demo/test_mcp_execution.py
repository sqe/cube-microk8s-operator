"""Public MCP protocol fixtures: no private agent code or live weather dependency."""

import json
import os
import subprocess
import sys
import tempfile
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path


class MCPExecutionTests(unittest.TestCase):
    def invoke(self, output, *options, extra_env=None):
        return subprocess.run(
            [sys.executable, str(Path(__file__).with_name("mcp_execution.py")),
             "--output", output, *options, "--", sys.executable, __file__, "--serve-fixture"],
            env={**os.environ, **(extra_env or {})},
            capture_output=True, text=True, timeout=30,
        )

    def test_success_and_failure_are_real_completed_mcp_calls(self):
        for location, expected_status, expected_kinds in [
            ("fixture-only", "succeeded", {"run", "tool", "artifact"}),
            ("fail", "failed", {"run", "tool"}),
        ]:
            with self.subTest(location=location), tempfile.TemporaryDirectory() as output:
                process = self.invoke(output, "--execute-only", "--arguments",
                                      json.dumps({"location": location}), extra_env={
                                          "AGENT_INGEST_TOKEN": "ingestion-only",
                                          "CUBE_CORE_TOKEN": "query-only",
                                      })
                self.assertEqual(process.returncode, 0 if expected_status == "succeeded" else 1,
                                 process.stderr)
                summary = json.loads(process.stdout)
                self.assertFalse(summary["cubeVerified"])
                self.assertEqual(summary["status"], expected_status)
                events = json.loads(Path(summary["executionArtifact"]).read_text())
                self.assertEqual({event["kind"] for event in events}, expected_kinds)
                for event in events:
                    self.assertEqual(event["status"], expected_status)
                    self.assertLessEqual(event["startedAt"], event["endedAt"])
                    self.assertEqual(event["parentId"], "" if event["kind"] == "run"
                                     else f'{summary["traceId"]}-run')
                    self.assertNotIn("inputTokens", event)
                    self.assertNotIn("costUsd", event)
                if expected_status == "succeeded":
                    result = json.loads(Path(summary["resultArtifact"]).read_text())
                    payload = result["structuredContent"] or json.loads(result["content"][0]["text"])
                    self.assertEqual(payload["temperature_c"], 12.5)
                    self.assertFalse(payload["credentials_leaked"])
                    artifact_event = next(e for e in events if e["kind"] == "artifact")
                    self.assertEqual(artifact_event["artifactUri"],
                                     Path(summary["resultArtifact"]).as_uri())

    def test_unadvertised_tool_does_not_claim_an_execution(self):
        with tempfile.TemporaryDirectory() as output:
            process = self.invoke(output, "--execute-only", "--tool", "missing")
            self.assertEqual(process.returncode, 1, process.stderr)
            summary = json.loads(process.stdout)
            events = json.loads(Path(summary["executionArtifact"]).read_text())
            self.assertEqual([event["kind"] for event in events], ["run"])
            self.assertEqual(events[0]["status"], "failed")
            self.assertIsNone(summary["resultArtifact"])

    def test_ingestion_and_cube_verification_reject_corrupt_results(self):
        for corrupt in [False, True]:
            events = []
            test = self

            class Handler(BaseHTTPRequestHandler):
                def log_message(self, *_):
                    pass

                def do_POST(self):
                    body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
                    if self.path == "/v1/executions":
                        test.assertEqual(self.headers["Authorization"], "Bearer " + "x" * 32)
                        events.append(body)
                        self.send_response(204)
                        self.end_headers()
                        return
                    test.assertEqual(self.path, "/cubejs-api/v1/load")
                    test.assertEqual(self.headers["Authorization"], "cube-query-only")
                    test.assertEqual(body["query"]["filters"][0]["values"], [events[0]["traceId"]])
                    # Ingestion persists absent optional string fields as empty strings.
                    rows = [{member: event.get(member.split(".")[1], "")
                             for member in body["query"]["dimensions"]} for event in events]
                    if corrupt:
                        rows[0]["AgentExecutions.status"] = "failed"
                    self.send_response(200)
                    self.end_headers()
                    self.wfile.write(json.dumps({"data": rows}).encode())

            with self.subTest(corrupt=corrupt), tempfile.TemporaryDirectory() as output:
                server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
                thread = threading.Thread(target=server.serve_forever, daemon=True)
                thread.start()
                try:
                    base = f"http://127.0.0.1:{server.server_port}"
                    process = self.invoke(output, extra_env={
                        "AGENT_INGEST_URL": base, "AGENT_INGEST_TOKEN": "x" * 32,
                        "CUBE_CORE_URL": base, "CUBE_CORE_TOKEN": "cube-query-only",
                    })
                    self.assertEqual(process.returncode, 1 if corrupt else 0, process.stderr)
                    self.assertEqual({event["kind"] for event in events}, {"run", "tool", "artifact"})
                    if not corrupt:
                        self.assertTrue(json.loads(process.stdout)["cubeVerified"])
                finally:
                    server.shutdown()
                    thread.join()
                    server.server_close()


def serve_fixture():
    from mcp.server.fastmcp import FastMCP

    server = FastMCP("deterministic-weather-test-fixture")

    @server.tool()
    def weather_current(location: str) -> dict:
        """Return deterministic test data, not live weather."""
        if location == "fail":
            raise ValueError("Deliberate fixture failure")
        return {"source": "deterministic test fixture", "temperature_c": 12.5,
                "credentials_leaked": any(os.environ.get(name) for name in
                                          ["AGENT_INGEST_TOKEN", "CUBE_CORE_TOKEN"])}

    server.run(transport="stdio")


if __name__ == "__main__":
    if sys.argv[1:] == ["--serve-fixture"]:
        serve_fixture()
    else:
        unittest.main()
