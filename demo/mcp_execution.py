#!/usr/bin/env python3
"""Execute a real stdio MCP tool and optionally verify its telemetry in Cube."""

import argparse
import asyncio
import json
import os
from datetime import datetime, timezone
from pathlib import Path
from uuid import uuid4

import httpx
from mcp import ClientSession, StdioServerParameters
from mcp.client.stdio import stdio_client


def now():
    return datetime.now(timezone.utc).isoformat()


async def execute(args):
    trace = "mcp-" + uuid4().hex
    output = Path(args.output).resolve()
    output.mkdir(parents=True, exist_ok=True)
    artifact = output / f"{trace}.result.json"
    events = []
    started = now()

    def span(kind, start, status="succeeded", **fields):
        event = {
            "eventId": f"{trace}-{kind}", "traceId": trace, "runId": f"{trace}-run",
            "parentId": "" if kind == "run" else f"{trace}-run",
            "tenantId": args.tenant, "cluster": args.cluster, "namespace": args.namespace,
            "agent": args.agent, "kind": kind, "name": args.tool,
            "status": status, "startedAt": start, "endedAt": now(),
        }
        event.update(fields)
        events.append(event)

    tool_started = None
    error = None
    try:
        # Do not give the tool server our ingestion credential or Cube JWT.
        environment = {"PYTHONPATH": os.environ["PYTHONPATH"]} if "PYTHONPATH" in os.environ else {}
        server = StdioServerParameters(command=args.command[0], args=args.command[1:], env=environment)
        async with stdio_client(server) as streams:
            async with ClientSession(*streams) as session:
                await session.initialize()
                tools = await session.list_tools()
                if args.tool not in {tool.name for tool in tools.tools}:
                    raise ValueError("Requested tool was not advertised by the MCP server")
                tool_started = now()
                result = await session.call_tool(args.tool, args.arguments)
                if result.isError:
                    raise RuntimeError("MCP tool returned an error")
                span("tool", tool_started, tool=args.tool)
                tool_started = None
                artifact_started = now()
                artifact.write_text(json.dumps(result.model_dump(mode="json"), indent=2) + "\n")
                span("artifact", artifact_started,
                     artifactUri=artifact.as_uri(), artifactType="application/json")
    except Exception as exc:
        error = type(exc).__name__
        if tool_started is not None:
            span("tool", tool_started, status="failed", tool=args.tool, errorType=error)
    span("run", started, status="failed" if error else "succeeded",
         **({"errorType": error} if error else {}))
    event_path = output / f"{trace}.executions.json"
    event_path.write_text(json.dumps(events, indent=2) + "\n")
    return trace, events, artifact, event_path, error


async def publish_and_verify(events, trace):
    ingest = os.environ["AGENT_INGEST_URL"].rstrip("/") + "/v1/executions"
    ingest_headers = {"Authorization": "Bearer " + os.environ["AGENT_INGEST_TOKEN"]}
    cube = os.environ["CUBE_CORE_URL"].rstrip("/") + "/cubejs-api/v1/load"
    cube_headers = {"Authorization": os.environ["CUBE_CORE_TOKEN"]}
    async with httpx.AsyncClient(timeout=95) as client:
        for event in events:
            # Retrying preserves both the event ID and its immutable payload.
            for attempt in range(5):
                try:
                    response = await client.post(ingest, json=event, headers=ingest_headers)
                    if response.status_code != 503:
                        response.raise_for_status()
                        if response.status_code != 204:
                            raise RuntimeError("Ingestion did not acknowledge a durable write")
                        break
                except httpx.TransportError:
                    if attempt == 4:
                        raise
                if attempt == 4:
                    raise RuntimeError("Ingestion remained unavailable after five attempts")
                await asyncio.sleep(2)

        names = ["eventId", "parentId", "kind", "status", "tool", "artifactUri"]
        query = {"query": {
            "dimensions": ["AgentExecutions." + name for name in names],
            "filters": [{"member": "AgentExecutions.traceId", "operator": "equals", "values": [trace]}],
            "limit": 100,
        }}
        for _ in range(20):
            response = await client.post(cube, json=query, headers=cube_headers)
            response.raise_for_status()
            result = response.json()
            if result.get("error") == "Continue wait":
                await asyncio.sleep(2)
                continue
            if "error" in result:
                raise RuntimeError("Cube rejected the trace query")
            rows = {row["AgentExecutions.eventId"]: row for row in result["data"]}
            if set(rows) != {event["eventId"] for event in events}:
                raise RuntimeError("Cube returned missing, duplicate, or unexpected trace events")
            if len(result["data"]) != len(events):
                raise RuntimeError("Cube returned duplicate trace events")
            for event in events:
                for name in names:
                    if rows[event["eventId"]].get("AgentExecutions." + name) != event.get(name, ""):
                        raise RuntimeError(f"Cube trace mismatch: {name}")
            return result["data"]
        raise RuntimeError("Cube did not finish the trace query within the retry limit")


async def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--agent", default="weather")
    parser.add_argument("--tool", default="weather_current")
    parser.add_argument("--arguments", type=json.loads, default={"location": "Reykjavik"})
    parser.add_argument("--tenant", default="local")
    parser.add_argument("--cluster", default="local-host")
    parser.add_argument("--namespace", default="demo")
    parser.add_argument("--output", required=True)
    parser.add_argument("--execute-only", action="store_true",
                        help="Save actual results/spans without claiming ingestion or Cube verification")
    parser.add_argument("command", nargs=argparse.REMAINDER, help="-- MCP_SERVER_COMMAND [ARGS...]")
    args = parser.parse_args()
    if args.command[:1] == ["--"]:
        args.command = args.command[1:]
    if not args.command:
        parser.error("an MCP server command is required after --")
    if not isinstance(args.arguments, dict):
        parser.error("--arguments must be a JSON object")
    if not args.execute_only:
        for name in ["AGENT_INGEST_URL", "AGENT_INGEST_TOKEN", "CUBE_CORE_URL", "CUBE_CORE_TOKEN"]:
            if not os.environ.get(name):
                parser.error(f"{name} is required unless --execute-only is selected")
    trace, events, artifact, event_path, error = await execute(args)
    rows = None if args.execute_only else await publish_and_verify(events, trace)
    print(json.dumps({"traceId": trace, "status": "failed" if error else "succeeded",
                      "resultArtifact": str(artifact) if artifact.exists() else None,
                      "executionArtifact": str(event_path), "spanCount": len(events),
                      "cubeVerified": rows is not None, "cubeRows": rows}, indent=2))
    if error:
        raise SystemExit("MCP execution failed; its completed failure spans were saved")


if __name__ == "__main__":
    asyncio.run(main())
