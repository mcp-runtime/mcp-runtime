#!/usr/bin/env python3
"""Require real allowed and denied tool calls through a session-bound adapter."""
import json
import sys
import time
from urllib.error import HTTPError
from urllib.request import Request, urlopen


def call(url, method, params=None, session="", notify=False):
    headers = {"Content-Type": "application/json", "Accept": "application/json, text/event-stream",
               "MCP-Protocol-Version": "2025-06-18"}
    if session:
        headers["Mcp-Session-Id"] = session
    body = {"jsonrpc": "2.0", "method": method, "params": params or {}}
    if not notify:
        body["id"] = 1
    request = Request(url, data=json.dumps(body).encode(), headers=headers, method="POST")
    try:
        response = urlopen(request, timeout=15)
    except HTTPError as error:
        response = error
    with response:
        raw = response.read().decode()
        if "text/event-stream" in response.headers.get("Content-Type", ""):
            events = [line[5:].strip() for line in raw.splitlines() if line.startswith("data:")]
            raw = events[-1] if events else "{}"
        try:
            payload = json.loads(raw or "{}")
        except ValueError:
            if response.status not in (401, 403):
                raise
            payload = {}
        return response.status, payload, response.headers.get("Mcp-Session-Id", session)


def allowed(status, payload):
    return status == 200 and "result" in payload and not payload.get("error") and not payload["result"].get("isError")


def denied(status, payload):
    # A crash or protocol error is not proof of a policy denial.
    return status in (401, 403)


def check(adapter, public, attempts=36, delay=5):
    for attempt in range(attempts):
        try:
            status, payload, session = call(adapter, "initialize", {"protocolVersion": "2025-06-18", "capabilities": {},
                "clientInfo": {"name": "qa-audit-release", "version": "1"}})
            if not allowed(status, payload):
                raise ValueError("initialize failed")
            call(adapter, "notifications/initialized", session=session, notify=True)
            status, payload, _ = call(adapter, "tools/call", {"name": "aaa-ping", "arguments": {}}, session)
            if not allowed(status, payload) or "pong" not in json.dumps(payload["result"]).lower():
                raise ValueError(f"allowed tool failed (HTTP {status}); inspect gateway/session diagnostics")
            break
        except (OSError, ValueError) as error:
            if attempt == attempts - 1:
                raise
            time.sleep(delay)
    status, payload, _ = call(adapter, "tools/call", {"name": "add", "arguments": {"a": 1, "b": 2}}, session)
    if status != 403 or payload.get("error") not in {"tool_not_granted", "tool_denied", "tool_not_allowed"}:
        raise ValueError("ungranted tool did not produce a tool policy denial")
    status, payload, _ = call(public, "tools/call", {"name": "aaa-ping", "arguments": {}})
    if not denied(status, payload):
        raise ValueError("anonymous tool request did not produce an authorization denial")
    print("PASS: session-bound allowed call, ungranted tool denial and anonymous denial")


if __name__ == "__main__":
    check(sys.argv[1], sys.argv[2])
