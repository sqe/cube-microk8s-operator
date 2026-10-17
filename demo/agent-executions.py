#!/usr/bin/env python3
"""Submit synthetic multi-agent telemetry and verify it through Cube Core.

Requires AGENT_INGEST_URL, AGENT_INGEST_TOKEN, CUBE_CORE_URL, CUBE_CORE_TOKEN.
No real agent work is performed: records demonstrate the instrumentation contract.
"""

import json
import math
import os
import time
import uuid
from datetime import datetime, timedelta, timezone
from pathlib import Path
from urllib.error import HTTPError
from urllib.request import Request, urlopen


def request(url, token, body=None, expected=200):
    headers = {"Authorization": token, "Content-Type": "application/json"}
    data = None if body is None else json.dumps(body).encode()
    try:
        with urlopen(Request(url, data=data, headers=headers), timeout=95) as response:
            status, raw = response.status, response.read()
    except HTTPError as error:
        status, raw = error.code, error.read()
    if status != expected:
        raise RuntimeError(f"HTTP {status}, expected {expected}: {raw.decode()[:1000]}")
    return json.loads(raw) if raw and expected < 400 else None


def main():
    ingest = os.environ["AGENT_INGEST_URL"].rstrip("/") + "/v1/executions"
    credential = "Bearer " + os.environ["AGENT_INGEST_TOKEN"]
    cube = os.environ["CUBE_CORE_URL"].rstrip("/")
    jwt = os.environ["CUBE_CORE_TOKEN"]
    meta = request(cube + "/cubejs-api/v1/meta", jwt)
    assert any(c["name"] == "AgentExecutions" for c in meta["cubes"]), "missing AgentExecutions model"
    trace = "demo-" + uuid.uuid4().hex
    start = datetime.now(timezone.utc) - timedelta(minutes=2)

    def span(suffix, kind, seconds, **fields):
        event = {
            "eventId": f"{trace}-{suffix}", "traceId": trace, "runId": f"{trace}-run",
            "tenantId": "demo", "cluster": "kind-cube-demo", "namespace": "cube-demo",
            "parentId": f"{trace}-run", "agent": "analytics-worker", "kind": kind,
            "name": suffix, "status": "succeeded", "startedAt": start.isoformat(),
            "endedAt": (start + timedelta(seconds=seconds)).isoformat(),
        }
        event.update(fields)
        return event

    events = [
        span("run", "run", 20, parentId=""),
        span("skill", "skill", 8, name="operating-cube-core", skillVersion="1.0.0"),
        span("tool", "tool", 2, parentId=f"{trace}-skill", name="Cube query", tool="http"),
        span("handoff", "handoff", 1, targetAgent="reviewer"),
        span("review", "run", 6, agent="reviewer", runId=f"{trace}-review", parentId=f"{trace}-handoff", status="failed", errorType="validation_failed"),
        span("artifact", "artifact", 1, artifactUri=f"urn:demo:{trace}:report", artifactType="application/json"),
        span("validation", "validation", 3, agent="reviewer", runId=f"{trace}-review", parentId=f"{trace}-review", status="failed", validation="failed"),
        span("model", "model", 4, model="demo-model", provider="synthetic", inputTokens=137, outputTokens=41, costUsd=0.0123),
        span("model-review", "model", 1, agent="reviewer", runId=f"{trace}-review", parentId=f"{trace}-review", model="demo-model", provider="synthetic", inputTokens=23, outputTokens=7, costUsd=0.0045),
    ]
    fixture = os.environ.get("AGENT_EVENT_FILE")
    if fixture:
        path = Path(fixture)
        if path.exists() and path.stat().st_size:
            events = json.loads(path.read_text())
            trace = events[0]["traceId"]
        else:
            path.write_text(json.dumps(events))
    request(ingest, "Bearer incorrect", events[0], expected=401)
    invalid = [dict(events[0], status="running"), dict(events[0], runId=""),
               dict(events[0], endedAt=events[0]["startedAt"], startedAt=events[0]["endedAt"]),
               dict(events[0], prompt="must-not-store"), dict(events[-1], costUsd=-1),
               dict(events[-1], outputTokens=-1), dict(events[0], inputTokens=1)]
    for kind in ["skill", "tool", "handoff", "artifact", "validation", "model", "unknown"]:
        invalid.append(dict(events[0], kind=kind))
    for event in invalid:
        request(ingest, credential, event, expected=400)
    # Different trace/tenant and non-failure outcomes are controls for filters.
    for suffix, kind, fields in [
        ("cancelled", "run", {"status": "cancelled"}),
        ("passed", "validation", {"validation": "passed"}),
        ("skipped", "validation", {"validation": "skipped"}),
    ]:
        control = dict(events[0], eventId=f"{trace}-control-{suffix}", traceId=f"{trace}-control",
                       runId=f"{trace}-control-cancelled", tenantId="control", name=suffix, kind=kind, **fields)
        request(ingest, credential, control, expected=204)
    # Children may arrive first; a retry must not inflate counts or usage.
    for event in reversed(events):
        request(ingest, credential, event, expected=204)
    request(ingest, credential, events[-1], expected=204)
    request(ingest, credential, dict(events[-1], outputTokens=999), expected=409)

    def query(fields, selected_trace=trace):
        fields.update(filters=[{"member": "AgentExecutions.traceId", "operator": "equals", "values": [selected_trace]}], limit=100)
        for _ in range(20):
            result = request(cube + "/cubejs-api/v1/load", jwt, {"query": fields})
            if result.get("error") == "Continue wait":
                time.sleep(2)
                continue
            if "error" in result:
                raise RuntimeError(result["error"])
            return result["data"]
        raise RuntimeError("Cube query timed out after bounded Continue wait retries")

    expected = {"count": 9, "runs": 2, "failedRuns": 1, "failedValidations": 1,
                "averageRunDurationMs": 13000, "inputTokens": 160, "outputTokens": 48, "costUsd": 0.0168}
    data = query({"measures": ["AgentExecutions." + name for name in expected]})
    for name, value in expected.items():
        actual = float(data[0]["AgentExecutions." + name])
        assert math.isclose(actual, value, abs_tol=1e-9), (name, actual, value)
    # Check the execution graph and detail fields, not just aggregate counts.
    names = ["eventId", "runId", "parentId", "agent", "kind", "name", "status", "skillVersion", "tool", "targetAgent", "artifactUri", "artifactType", "validation", "model", "provider", "errorType"]
    details = query({"dimensions": ["AgentExecutions." + name for name in names]})
    by_id = {row["AgentExecutions.eventId"]: row for row in details}
    assert len(by_id) == 9, details
    for event in events:
        for name in names:
            assert by_id[event["eventId"]]["AgentExecutions." + name] == event.get(name, ""), (name, event["eventId"])
    controls = query({"dimensions": ["AgentExecutions.tenantId"], "measures": ["AgentExecutions.count"]})
    assert controls == [{"AgentExecutions.tenantId": "demo", "AgentExecutions.count": "9"}], controls
    controls = query({"measures": ["AgentExecutions.count", "AgentExecutions.runs", "AgentExecutions.failedRuns", "AgentExecutions.failedValidations"]}, trace + "-control")[0]
    for name, value in {"count": 3, "runs": 1, "failedRuns": 0, "failedValidations": 0}.items():
        assert int(controls["AgentExecutions." + name]) == value, controls
    print(json.dumps({"traceId": trace, "verified": expected, "checks": ["auth", "validation", "out-of-order", "idempotency", "conflicting replay", "execution graph", "Cube aggregates"]}, indent=2))


if __name__ == "__main__":
    main()
